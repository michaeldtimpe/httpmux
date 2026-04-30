# Agents

httpmux uses an agent model to manage remote connections. Each connection to a target machine is handled by a **session agent** — a server-side process that maintains the SSH tunnel and bridges it to the browser. This document describes how agents are created, managed, and cleaned up.

## Agent Types

### Terminal Agent

A terminal agent manages a tmux session on a remote host.

**Identity**: One terminal agent per target. If multiple browser tabs open `/terminal/dev-server`, they all share the same agent (and therefore the same tmux session).

**Lifecycle**:

```
Browser connects to /ws/terminal/{name}
        │
        ▼
┌─ Session Manager ─────────────────────────────┐
│  Does an agent for this target already exist?  │
│                                                │
│  YES → Return existing agent                   │
│  NO  → Create new agent:                       │
│        1. BastionPool.DialTarget()             │
│        2. ssh.OpenPTY(xterm-256color)          │
│        3. Start: tmux new-session -A -s {name} │
│        4. Store in termSessions map            │
└────────────────────────────────────────────────┘
        │
        ▼
  Agent.Attach(websocket)
  ┌──────────────────────┐
  │  PTY stdout → WS     │ goroutine 1
  │  WS → PTY stdin      │ goroutine 2
  └──────────────────────┘
        │
        ▼ (browser disconnects)
  Agent stays alive for grace period (5 min)
        │
        ├─ Browser reconnects → Agent.Attach() again
        │
        └─ Grace period expires → Agent.Close()
           └─ SSH session closed
           └─ tmux detaches (stays alive on target)
```

**Key properties**:
- **Persistent**: The agent survives browser disconnects. The tmux session on the target survives agent shutdown.
- **Shared**: Multiple WebSocket connections attach to the same agent (same tmux session).
- **Reconnectable**: Closing a tab and reopening it seamlessly reattaches to the running tmux.
- **Idempotent creation**: `GetOrCreateTerminal()` is safe to call concurrently — it uses double-checked locking to avoid creating duplicate agents.

**What happens on server restart**: The agent is destroyed, but the tmux session on the target continues running. When httpmux starts again and the user reconnects, a new agent is created and `tmux new-session -A -s {name}` reattaches to the existing tmux session.

### VNC Agent

A VNC agent manages a VNC desktop connection to a remote host.

**Identity**: One VNC agent per WebSocket connection. Each browser tab gets its own agent.

**Lifecycle**:

```
Browser connects to /ws/desktop/{name}
        │
        ▼
┌─ Session Manager ─────────────────────────┐
│  Always creates a new agent:              │
│  1. BastionPool.DialTarget()              │
│  2. ssh.DialRemote("localhost:{vnc_port}")│
└───────────────────────────────────────────┘
        │
        ▼
  Agent.Bridge(websocket)
  ┌──────────────────────┐
  │  VNC TCP → WS        │ goroutine 1
  │  WS → VNC TCP        │ goroutine 2
  └──────────────────────┘
        │
        ▼ (browser disconnects)
  Agent.Close() immediately
  └─ VNC TCP connection closed
  └─ SSH tunnel closed
```

**Key properties**:
- **Ephemeral**: Created per-connection, torn down when the WebSocket closes.
- **Not shared**: Each browser tab has its own independent VNC connection.
- **Transparent**: The agent does not parse the VNC (RFB) protocol. It copies bytes between the WebSocket and the TCP tunnel. noVNC in the browser handles all protocol logic.

## Agent-to-Infrastructure Mapping

Each agent holds references to two SSH resources:

```
Terminal Agent                     VNC Agent
  ├── *ssh.Client (to target)       ├── *ssh.Client (to target)
  └── *ssh.Session (PTY)            └── net.Conn (TCP tunnel to VNC port)
```

Both `*ssh.Client` instances are obtained via `BastionPool.DialTarget()`, which means they are multiplexed over bastion SSH connections. The bastion pool tracks how many channels each bastion connection has open.

```
BastionPool
  ├── bastionConn #1 (channels: 3/8)
  │     ├── target-A ssh.Client → Terminal Agent
  │     ├── target-B ssh.Client → Terminal Agent
  │     └── target-A ssh.Client → VNC Agent
  ├── bastionConn #2 (channels: 1/8)
  │     └── target-C ssh.Client → VNC Agent
  └── (new connections created on demand)
```

When a target `*ssh.Client` closes (agent shutdown), the bastion channel is released. When all channels on a bastion connection are released and the connection is marked dead, it is eligible for garbage collection.

## WebSocket Protocol

### Terminal Agent Protocol

Binary WebSocket messages with a 1-byte type prefix.

**Browser → Server**:
| Byte 0 | Payload | Description |
|--------|---------|-------------|
| `0x00` | Raw bytes | Terminal input (keystrokes) |
| `0x01` | JSON `{"cols":N,"rows":N}` | Resize request |

**Server → Browser**:
| Format | Description |
|--------|-------------|
| Raw bytes (no prefix) | Terminal output (ANSI escape sequences) |

The first message from the browser **must** be a resize (`0x01`) so the server knows what PTY dimensions to allocate.

### VNC Agent Protocol

No custom protocol. noVNC speaks RFB directly over the WebSocket. The server copies binary frames verbatim between the WebSocket and the TCP VNC connection. The WebSocket uses the `"binary"` subprotocol.

## Session Manager

The session manager (`internal/session/manager.go`) coordinates agent lifecycles.

### State

```go
type Manager struct {
    termSessions map[string]*terminal.Session  // keyed by target name
    pool         *ssh.BastionPool
    config       *config.Config
    terminalGrace time.Duration                // default: 5 minutes
}
```

Terminal agents are tracked in a map. VNC agents are not tracked — the web handler owns them directly (via `defer vncSession.Close()`).

### Operations

| Method | Behavior |
|--------|----------|
| `GetOrCreateTerminal(target, cols, rows)` | Return existing agent or create one. Thread-safe with double-checked locking. |
| `CreateVNC(target)` | Always creates a new agent. Returns ownership to caller. |
| `RemoveTerminal(target)` | Force-closes and removes a terminal agent. |
| `List()` | Returns info about all active terminal agents. |
| `Close()` | Shuts down all agents. Called during server shutdown. |

### Cleanup Loop

A background goroutine runs every 30 seconds:

1. **Closed sessions**: If the SSH connection dropped (bastion failure, target reboot), the agent's `IsClosed()` returns true. Remove it from the map.
2. **Idle sessions**: If no WebSocket has been attached for longer than `terminalGrace` (5 minutes), close the agent and remove it. The tmux session on the target survives.

### Concurrency

The manager uses a `sync.RWMutex`. Reads (`List()`) take a read lock. Writes (`GetOrCreateTerminal`, `RemoveTerminal`, `cleanup`) take a write lock. The `GetOrCreateTerminal` method uses a double-checked locking pattern: it checks the map under lock, releases the lock to create the SSH session (which is slow), then re-checks the map before inserting.

## Failure Modes

| Failure | Terminal Agent | VNC Agent |
|---------|---------------|-----------|
| Browser tab closes | Agent stays alive (grace period) | Agent destroyed immediately |
| Network blip | WebSocket closes, browser auto-reconnects, reattaches to same agent | WebSocket closes, browser reloads, creates new agent |
| Bastion connection drops | Agent marked closed at next keepalive. Cleanup removes it. tmux survives on target. | Bridge goroutine gets read error, agent destroyed. |
| Target reboots | SSH session ends, agent marked closed. tmux session lost. | VNC TCP connection drops, agent destroyed. |
| httpmux server restarts | All agents destroyed. tmux sessions survive. Reconnection creates new agents that reattach to existing tmux. | All agents destroyed. Browser reloads, creates fresh sessions. |
| Bastion MaxSessions hit | Pool opens new TCP+SSH connection to bastion. Transparent to agents. | Same. |

## Monitoring

httpmux logs agent lifecycle events via `log/slog`:

```
INFO starting httpmux listen=:8443
INFO connecting to bastion host=bastion:22
INFO created terminal session target=dev-server session_id=dev-server-1234567890
INFO created VNC session target=dev-server session_id=vnc-dev-server-1234567890
INFO closed VNC session target=dev-server session_id=vnc-dev-server-1234567890
INFO cleaned up idle terminal session target=dev-server
WARN bastion keepalive failed, marking connection dead error=...
```

Active sessions can also be queried via the REST API:

```sh
curl -b cookies.txt https://localhost:8443/api/sessions
```

```json
[
  {
    "id": "dev-server-1714500000000000000",
    "type": 0,
    "target_name": "dev-server",
    "created_at": "2026-04-30T12:00:00Z"
  }
]
```

Type `0` = terminal, type `1` = VNC (though VNC sessions are ephemeral and rarely visible in this list).
