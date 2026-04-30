# Architecture

## Overview

httpmux is a single-binary Go application that acts as a browser-accessible gateway to remote machines. It has no external runtime dependencies — all frontend assets (HTML templates, CSS, JavaScript libraries) are compiled into the binary via `//go:embed`.

The system has four layers:

```
┌─────────────────────────────────────────────────────────┐
│  Browser Layer                                          │
│  xterm.js (terminal)  |  noVNC (desktop)  |  dashboard  │
└──────────────────────────┬──────────────────────────────┘
                           │ WebSocket (wss://)
┌──────────────────────────┴──────────────────────────────┐
│  Web Layer                                              │
│  Go net/http ServeMux  |  Auth middleware  |  Templates  │
│  internal/web/                                          │
└──────────────────────────┬──────────────────────────────┘
                           │ Go function calls
┌──────────────────────────┴──────────────────────────────┐
│  Session Layer                                          │
│  Session Manager  |  Terminal sessions  |  VNC sessions  │
│  internal/session/ + internal/terminal/ + internal/vnc/  │
└──────────────────────────┬──────────────────────────────┘
                           │ SSH protocol
┌──────────────────────────┴──────────────────────────────┐
│  SSH Layer                                              │
│  Bastion Pool  |  Proxy-Jump  |  PTY  |  TCP tunnels    │
│  internal/ssh/                                          │
└─────────────────────────────────────────────────────────┘
```

## Project Structure

```
httpmux/
├── cmd/httpmux/main.go              # Entry point, wiring, signal handling
├── internal/
│   ├── config/config.go             # YAML config types, loading, validation
│   ├── auth/auth.go                 # Cookie auth, bcrypt, HMAC signing
│   ├── ssh/
│   │   ├── bastion.go               # Bastion connection pool, proxy-jump
│   │   ├── agent.go                 # SSH agent (SSH_AUTH_SOCK) support
│   │   ├── session.go               # PTY allocation for terminal sessions
│   │   └── tunnel.go                # TCP dial through SSH for VNC
│   ├── terminal/terminal.go         # tmux session + WebSocket-PTY bridge
│   ├── vnc/vnc.go                   # VNC WebSocket-TCP relay
│   ├── session/manager.go           # Session lifecycle, cleanup
│   └── web/
│       ├── server.go                # HTTP server, routing, embedded assets
│       ├── handlers.go              # HTTP + WebSocket handlers
│       ├── ws.go                    # WebSocket accept helper
│       ├── templates/               # Go html/template files (embedded)
│       └── static/                  # CSS, JS, vendor libs (embedded)
├── configs/httpmux.example.yaml
├── Makefile
└── go.mod
```

All packages under `internal/` are unexported — httpmux is an application, not a library.

## Traffic Flow

### Terminal Session

```
1. Browser loads /terminal/{name}
2. Page loads xterm.js and connects WebSocket to /ws/terminal/{name}
3. Browser sends initial resize message: [0x01, {"cols":120,"rows":40}]
4. Server receives resize, calls session.Manager.GetOrCreateTerminal()
   a. Manager checks for existing session for this target
   b. If none: BastionPool.DialTarget() → SSH through bastion → target
   c. ssh.OpenPTY() on target with dimensions from browser
   d. Runs: tmux new-session -A -s {default_session}
5. terminal.Session.Attach(ws) starts two goroutines:
   a. PTY stdout → WebSocket (binary messages, no framing)
   b. WebSocket → PTY stdin (0x00 prefix = data, 0x01 prefix = resize)
6. xterm.js renders output. Keystrokes sent as [0x00, ...bytes]
7. On disconnect: session stays alive for 5 min grace period
8. On reconnect: same tmux session reattached
```

### VNC Desktop Session

```
1. Browser loads /desktop/{name}
2. Page loads noVNC and connects WebSocket to /ws/desktop/{name}
   (subprotocol: "binary")
3. Server calls session.Manager.CreateVNC()
   a. BastionPool.DialTarget() → SSH through bastion → target
   b. ssh.DialRemote("localhost:{vnc_port}") through the SSH connection
4. vnc.Session.Bridge(ws) starts two goroutines:
   a. VNC TCP → WebSocket (raw bytes, 32KB buffer)
   b. WebSocket → VNC TCP (raw bytes)
5. noVNC handles RFB protocol negotiation, encoding, rendering
   httpmux never parses VNC protocol — pure byte relay
6. On disconnect: VNC session torn down immediately (no persistence)
```

## Component Design

### SSH Bastion Pool (`internal/ssh/bastion.go`)

The bastion pool is the central infrastructure component. Every connection to a target machine flows through it.

**Connection pooling**: The pool maintains multiple SSH connections to the bastion host. Each SSH connection can multiplex up to `max_sessions` channels (default 8, under OpenSSH's default limit of 10). When all connections are saturated, a new TCP+SSH connection is established automatically.

**Proxy-jump**: `DialTarget()` implements the equivalent of `ssh -J bastion target`:
1. Pick the least-loaded bastion connection (fewest active channels)
2. `bastionClient.Dial("tcp", target.Host)` — opens an SSH channel tunneled through the bastion, returning a `net.Conn` to the target
3. `ssh.NewClientConn()` over that `net.Conn` — performs a second SSH handshake with the target
4. Returns a standard `*ssh.Client` — downstream code is unaware of the bastion

**Channel lifecycle**: When a target `*ssh.Client` is created, a goroutine waits on `targetClient.Wait()`. When the target connection closes, the bastion channel is released. This prevents channel leaks.

**Keepalive**: Each bastion connection runs a goroutine sending `keepalive@openssh.com` requests at the configured interval. If a keepalive fails, the connection is marked dead and will not be reused. New connections are created on demand.

**Host key verification**: Uses `golang.org/x/crypto/ssh/knownhosts` to verify host keys against a `known_hosts` file. Targets can optionally pin a specific SHA256 fingerprint. `InsecureIgnoreHostKey` is never used.

**Nagle's algorithm**: `SetNoDelay(true)` is called on every TCP connection (bastion dial, tunnel dial, VNC dial) to minimize latency for interactive sessions.

### Terminal Sessions (`internal/terminal/terminal.go`)

A terminal session wraps an SSH PTY connected to tmux on a remote host.

**tmux integration**: The command `tmux new-session -A -s {name}` is used to either attach to an existing tmux session or create a new one. This means:
- Multiple browser tabs can view the same tmux session
- The tmux session survives server restarts (it lives on the target)
- Reconnecting after a disconnect reattaches to the same session

**WebSocket protocol**: Binary WebSocket messages with a 1-byte type prefix:
- `0x00` + payload → write to PTY stdin (terminal input)
- `0x01` + JSON → resize PTY (`{"cols": N, "rows": N}`)

Server-to-browser messages are raw PTY output with no framing (xterm.js handles ANSI escape sequences).

**Initial resize**: The browser must send a resize message before any data. The server waits for this first message to determine PTY dimensions before allocating the SSH session. This prevents garbled display from mismatched geometry.

**Session reuse**: The session manager keeps terminal sessions alive after the last WebSocket disconnects. A 5-minute grace period allows for reconnection. After the grace period, the SSH session is closed (tmux detaches on the target and continues running).

### VNC Sessions (`internal/vnc/vnc.go`)

A VNC session is a bidirectional byte relay between a browser WebSocket and a VNC server's TCP port, tunneled through SSH.

**No protocol awareness**: httpmux does not parse the RFB (VNC) protocol. noVNC in the browser handles all protocol negotiation, authentication, encoding, and rendering. The server is a transparent proxy.

**WebSocket subprotocol**: noVNC requires the `"binary"` WebSocket subprotocol. The server explicitly accepts this during the WebSocket upgrade.

**No persistence**: Unlike terminal sessions, VNC sessions are not reusable. Each WebSocket connection gets a fresh SSH tunnel and VNC connection. When the WebSocket closes, everything is torn down.

### Session Manager (`internal/session/manager.go`)

The session manager is the coordination layer between the web handlers and the SSH/terminal/VNC subsystems.

**Terminal sessions**: Keyed by target name. `GetOrCreateTerminal()` returns an existing session if one is active, or creates a new one. This is how browser tab reconnection works — the same `terminal.Session` object is shared across multiple WebSocket connections.

**VNC sessions**: Created fresh per connection via `CreateVNC()`. Not tracked in the manager after creation — the web handler owns the lifecycle.

**Cleanup**: A background goroutine runs every 30 seconds, scanning for:
- Closed sessions (SSH connection dropped) → removed from the map
- Idle sessions (no WebSocket activity for 5 minutes) → closed and removed

**Shutdown**: `Close()` tears down all active sessions. Called via `defer` in `main.go` after the HTTP server stops.

### Web Server (`internal/web/server.go`)

Uses Go 1.22+'s enhanced `http.ServeMux` with method+path pattern routing. No third-party router.

**Embedded assets**: Templates and static files are compiled into the binary via `//go:embed`. At runtime, templates are parsed once at startup. Static files are served via `http.FileServer` over the embedded filesystem.

**Authentication**: HMAC-SHA256 signed cookies. The cookie value is `username|expiry_unix|hmac_signature`. On each request, the middleware verifies the HMAC and checks expiry. No server-side session store needed. WebSocket auth works automatically because the browser sends cookies on same-origin WebSocket connections.

**No read/write timeouts**: The HTTP server does not set `ReadTimeout` or `WriteTimeout` because WebSocket connections are long-lived. Individual handlers manage their own deadlines.

## Key Dependencies

| Dependency | Purpose |
|------------|---------|
| `golang.org/x/crypto/ssh` | SSH client, tunneling, PTY allocation, key parsing |
| `golang.org/x/crypto/ssh/knownhosts` | Host key verification |
| `golang.org/x/crypto/ssh/agent` | SSH agent support |
| `golang.org/x/crypto/bcrypt` | Password hashing |
| `github.com/coder/websocket` | WebSocket server (formerly `nhooyr.io/websocket`) |
| `gopkg.in/yaml.v3` | YAML config parsing |
| xterm.js (vendored JS) | Browser terminal emulation |
| noVNC (vendored JS) | Browser VNC client |

## Startup Sequence

```
main()
  ├── config.Load()           — parse and validate YAML
  ├── ssh.NewBastionPool()    — load known_hosts, prepare pool
  ├── auth.New()              — initialize authenticator
  ├── session.NewManager()    — create session manager
  ├── web.New()               — parse templates, register routes
  ├── mgr.StartCleanup()     — start background session cleanup
  └── srv.ListenAndServe()   — start HTTP(S) server
```

## Shutdown Sequence

```
SIGTERM / SIGINT received
  ├── ctx.Done() triggers
  ├── http.Server.Shutdown()  — drain active HTTP connections
  ├── WebSocket connections close → sessions detach
  ├── session.Manager.Close() — close all terminal sessions
  └── ssh.BastionPool.Close() — close all bastion connections
```

Order matters: HTTP connections must drain before SSH connections are torn down, otherwise in-flight WebSocket handlers will panic on closed SSH sessions.

## Security Model

- **Network**: All traffic encrypted (TLS for browser leg, SSH for infrastructure leg)
- **Authentication**: bcrypt password hashing, HMAC-signed session cookies, HttpOnly + Secure flags
- **SSH keys**: Loaded from disk paths or SSH agent. Never stored in environment variables
- **Host verification**: `known_hosts` file required. Optional per-target fingerprint pinning. No insecure callbacks
- **Authorization**: All routes except `/login` are behind auth middleware. WebSocket endpoints inherit cookie auth
- **Session isolation**: Each target connection has its own SSH client. Sessions are scoped to authenticated users
