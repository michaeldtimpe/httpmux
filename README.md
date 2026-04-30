# httpmux

A self-hosted, browser-based remote access gateway. Access terminal sessions (tmux) and remote desktops (VNC) on your machines from any browser, tunneled securely through an SSH bastion host.

Single binary. No agents. No VPN required.

## How It Works

httpmux sits between your browser and your infrastructure. It maintains SSH connections through a bastion host to reach target machines, then bridges browser WebSockets to either tmux terminal sessions (via xterm.js) or VNC desktop sessions (via noVNC).

```
Browser                httpmux Server              Bastion              Target
  |                         |                         |                    |
  |------- wss:// --------->|                         |                    |
  |    (TLS encrypted)      |------- SSH tunnel ----->|                    |
  |                         |    (SSH encrypted)      |--- SSH channel --->|
  |                         |                         |  (SSH encrypted)   |
  |  xterm.js / noVNC       |  WebSocket-PTY bridge   |                   tmux / VNC
```

All traffic is encrypted. The browser-to-server leg uses TLS. The server-to-bastion-to-target legs use SSH. There is no cleartext segment in production.

## Quick Start

### 1. Build

```sh
make build
```

This produces `bin/httpmux` — a ~15MB binary with all frontend assets embedded.

### 2. Configure

```sh
cp configs/httpmux.example.yaml httpmux.yaml
```

Edit `httpmux.yaml`:

- Set `auth.session.secret` to a random string:
  ```sh
  openssl rand -hex 32
  ```

- Generate a password hash for your user:
  ```sh
  htpasswd -nbBC 10 "" 'your-password' | cut -d: -f2
  ```

- Point `bastion` at your SSH jump host
- Point `ssh.known_hosts` at your known_hosts file
- Define your targets with SSH credentials and which services (terminal/desktop) to expose

### 3. Run

```sh
# Development (no TLS)
./bin/httpmux -config httpmux.yaml

# Production (with TLS)
# Uncomment the tls section in your config first
./bin/httpmux -config httpmux.yaml
```

Navigate to `https://your-host:8443`, log in, and click **Terminal** or **Desktop** on any target.

## Configuration Reference

```yaml
server:
  listen: ":8443"                    # Bind address
  tls:                               # Required for production
    cert: /path/to/cert.pem
    key: /path/to/key.pem

auth:
  users:
    - username: admin
      password_hash: "$2a$10$..."    # bcrypt hash
  session:
    secret: "hex-string"             # HMAC signing key for cookies
    max_age: 86400                   # Session lifetime in seconds (default: 24h)

ssh:
  use_agent: false                   # Use SSH_AUTH_SOCK instead of key files
  known_hosts: ~/.ssh/known_hosts    # Fallback known_hosts for all connections

bastion:
  host: "bastion:22"                 # Bastion/jump host address
  user: "jump_user"                  # SSH user on the bastion
  private_key: /path/to/key          # SSH private key file
  passphrase: ""                     # Key passphrase (if encrypted)
  known_hosts: ~/.ssh/known_hosts    # Override for bastion specifically
  keepalive: 30                      # Seconds between keepalive probes
  max_sessions: 8                    # Channels per connection before opening a new one

targets:
  - name: "my-server"               # URL-safe identifier
    host: "10.0.1.10:22"            # Target host address (reached via bastion)
    user: "deploy"                   # SSH user on the target
    private_key: /path/to/key        # SSH private key for this target
    host_key_fingerprint: "SHA256:..." # Optional: pin host key instead of known_hosts
    terminal:
      enabled: true
      default_session: "main"        # tmux session name to attach/create
    desktop:
      enabled: true
      vnc_port: 5901                 # VNC server port on the target
```

### Configuration Details

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `server.listen` | Yes | — | Bind address (e.g., `:8443`) |
| `server.tls.cert` / `server.tls.key` | No | — | TLS certificate and key. Required for production |
| `auth.session.secret` | Yes | — | HMAC key for signing session cookies |
| `auth.session.max_age` | No | `86400` | Cookie lifetime in seconds |
| `bastion.host` | Yes | — | Bastion SSH address (`host:port`) |
| `bastion.private_key` | Yes* | — | SSH key file. *Not required if `ssh.use_agent` is true |
| `bastion.known_hosts` | Yes** | — | **Falls back to `ssh.known_hosts` if unset |
| `bastion.keepalive` | No | `30` | Keepalive interval in seconds |
| `bastion.max_sessions` | No | `8` | SSH channels per connection |
| `targets[].name` | Yes | — | Unique target identifier |
| `targets[].host` | Yes | — | Target SSH address (`host:port`) |
| `targets[].terminal.default_session` | No | `"main"` | tmux session name |
| `targets[].desktop.vnc_port` | Yes* | — | *Required when desktop is enabled |

## Features

### Terminal Sessions
- Full terminal emulation in the browser via [xterm.js](https://xtermjs.org/)
- Attaches to tmux sessions on remote hosts — sessions persist across disconnects
- Window resize propagation
- Automatic reconnection on connection loss

### Desktop Sessions
- Remote desktop via VNC, rendered in the browser with [noVNC](https://novnc.com/)
- VNC password prompt handled in-browser
- Scales to viewport with configurable resize behavior
- VNC traffic tunneled through SSH (never exposed directly)

### Security
- All connections route through a fixed SSH bastion — no direct target exposure
- Host key verification via `known_hosts` (never uses `InsecureIgnoreHostKey`)
- Optional per-target host key fingerprint pinning
- HMAC-SHA256 signed session cookies with bcrypt password hashing
- TLS for the browser-to-server connection
- `SetNoDelay(true)` on all TCP connections for low-latency interaction

### Session Management
- Terminal sessions are reusable — closing and reopening a tab reconnects to the same tmux session
- Idle terminal sessions cleaned up after 5 minutes of inactivity
- VNC sessions are per-connection (torn down when the WebSocket closes)
- Dashboard shows active sessions with manual kill capability
- SSH bastion connections are pooled with automatic scaling (new connections opened when channel limits are approached)

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Dashboard |
| `GET` | `/login` | Login page |
| `POST` | `/login` | Authenticate |
| `POST` | `/logout` | Log out |
| `GET` | `/terminal/{name}` | Terminal page for a target |
| `GET` | `/desktop/{name}` | Desktop page for a target |
| `GET` | `/ws/terminal/{name}` | WebSocket: terminal session |
| `GET` | `/ws/desktop/{name}` | WebSocket: VNC session |
| `GET` | `/api/targets` | List configured targets (JSON) |
| `GET` | `/api/sessions` | List active sessions (JSON) |
| `DELETE` | `/api/sessions/{id}` | Kill a session |

## Prerequisites

- **Go 1.22+** (uses new `ServeMux` routing patterns)
- **SSH bastion host** with key-based authentication
- **tmux** installed on target hosts (for terminal sessions)
- **VNC server** running on target hosts (for desktop sessions, e.g., TigerVNC, x11vnc)
- Target hosts reachable from the bastion via SSH

## License

MIT
