# Synology NAS Deployment Plan

Deploy `httpmux` as a container on a Synology NAS (DSM 7.2+ with Container Manager) and expose it to the internet so you can reach LAN machines from outside.

## Repo gaps to fix first

- **No Dockerfile.** Add the one in step 1 below.
- **No `.dockerignore`.** Recommended to avoid shipping the local `httpmux` / `bin/httpmux` binaries into the build context.
- **Bastion is mandatory.** `internal/config/config.go:116-127` rejects configs without `bastion.host` + key + known_hosts. If you don't have a separate jump host, point `bastion.host` at any always-on LAN box that can SSH into your targets — the NAS itself is fine.
- **Architecture.** Confirm CPU on the NAS (`uname -m` over SSH). Most modern Synology units are `linux/amd64`; some Realtek/ARM units (e.g., DS220j, DS124) are `linux/arm64`. Build for the right platform.

## Step 1 — Add a Dockerfile

Create `Dockerfile` at repo root:

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/httpmux ./cmd/httpmux

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tini && adduser -D -u 1000 httpmux
USER httpmux
COPY --from=build /out/httpmux /usr/local/bin/httpmux
EXPOSE 8443
ENTRYPOINT ["/sbin/tini","--","/usr/local/bin/httpmux","-config","/etc/httpmux/httpmux.yaml"]
```

Create `.dockerignore`:

```
bin/
httpmux
*.yaml
!configs/*.yaml
.git/
```

## Step 2 — Build the image for the NAS architecture

On your Mac:

```sh
docker buildx build --platform linux/amd64 -t httpmux:latest --load .
docker save httpmux:latest -o httpmux.tar
```

(Use `linux/arm64` if your NAS is ARM.) Copy `httpmux.tar` to a NAS share (e.g., over SMB to `/volume1/docker/httpmux/`).

## Step 3 — Prepare host config on the NAS

Create the directory tree:

```
/volume1/docker/httpmux/
├── config/
│   └── httpmux.yaml          # from configs/httpmux.example.yaml
├── ssh/
│   ├── bastion_key           # chmod 600
│   ├── target_key            # chmod 600
│   └── known_hosts           # populated with bastion + every target
└── tls/
    ├── cert.pem
    └── key.pem
```

TLS cert: easiest path is **DSM → Control Panel → Security → Certificate** with Let's Encrypt tied to your DDNS hostname, then **Export** and drop the files into `tls/`. Re-export on each renewal (or symlink from DSM's cert store and restart the container on renewal).

Edit `httpmux.yaml`:

- `server.listen: ":8443"`
- Uncomment the `tls:` block, point at `/etc/httpmux/tls/cert.pem` + `/etc/httpmux/tls/key.pem`.
- `auth.session.secret`: generate with `openssl rand -hex 32`.
- `auth.users[*].password_hash`: generate with `htpasswd -nbBC 10 "" 'YOURPASS' | cut -d: -f2`.
- `bastion.private_key: /etc/httpmux/ssh/bastion_key`
- `bastion.known_hosts: /etc/httpmux/ssh/known_hosts`
- Each `targets[*].private_key`: `/etc/httpmux/ssh/target_key`

Populate `known_hosts` from a Linux/Mac with SSH access:

```sh
ssh-keyscan -H bastion.lan > known_hosts
ssh-keyscan -H 10.0.1.10 >> known_hosts
# repeat for every target host
```

## Step 4 — Import and run via Container Manager

In DSM:

1. **Container Manager → Image → Add → Add from file** → select `httpmux.tar`.
2. **Container → Create**, image = `httpmux:latest`.
3. **Volumes** (read-only where noted):
   - `/volume1/docker/httpmux/config` → `/etc/httpmux` (ro)
   - `/volume1/docker/httpmux/ssh` → `/etc/httpmux/ssh` (ro)
   - `/volume1/docker/httpmux/tls` → `/etc/httpmux/tls` (ro)
4. **Port settings**: host `8443` → container `8443` (TCP).
5. **Network**: `bridge` is fine. If you point `bastion.host` at the NAS itself, switch to `host` networking *or* use the NAS's LAN IP (never `127.0.0.1` from inside a bridged container).
6. **Restart policy**: `unless-stopped`.

Alternative: use Container Manager **Project** feature with a `docker-compose.yml` for reproducibility:

```yaml
services:
  httpmux:
    image: httpmux:latest
    restart: unless-stopped
    ports:
      - "8443:8443"
    volumes:
      - ./config:/etc/httpmux:ro
      - ./ssh:/etc/httpmux/ssh:ro
      - ./tls:/etc/httpmux/tls:ro
```

## Step 5 — Expose to the internet

Pick **one** of these:

### Option A: Router port-forward direct to container
- Set up DSM **DDNS** (free `*.synology.me` works) so your TLS cert's CN/SAN matches.
- On your router: forward TCP 443 (external) → NAS LAN IP : 8443 (or forward 8443→8443 and use `:8443` in the URL).

### Option B: DSM reverse proxy (recommended)
- **Control Panel → Login Portal → Advanced → Reverse Proxy → Create**.
  - Source: `https://httpmux.yourddns.synology.me:443`
  - Destination: `https://localhost:8443`
- **Custom Header** tab → use the **WebSocket** preset (adds `Upgrade` / `Connection: upgrade`). Without this, terminal/VNC WebSockets break.
- Router: forward only 443 → NAS 443. Container's 8443 stays LAN-only.
- This lets DSM manage cert renewal automatically — no manual export step.

## Step 6 — Lock it down

- **DSM Control Panel → Security → Firewall**: allow 443 only from expected source ranges if you can; otherwise rely on bcrypt + signed session cookies.
- **Auto-Block** (Control Panel → Security → Account): tighten threshold for repeated 401s on the proxy.
- **2FA on the DSM admin account** itself (separate from httpmux's own auth).
- Confirm `known_hosts` is fully populated — the code never falls back to `InsecureIgnoreHostKey`, so a missing entry = startup or connect failure (good).
- Optionally set per-target `host_key_fingerprint` pins in the YAML for belt-and-braces.

## Step 7 — Smoke test

From a network *outside* your LAN (phone on cellular works):

1. Browse to `https://httpmux.yourddns.synology.me/`.
2. Log in.
3. Click **Terminal** on a target — should land in tmux.
4. Click **Desktop** on a VNC-enabled target — should render the desktop.
5. In Container Manager, tail the container logs and watch for SSH handshake / known_hosts errors on first connect.

## Operational notes

- **Cert renewal**: if using Option A (mounted cert files), restart the container after each Let's Encrypt renewal so it re-reads the cert. Option B sidesteps this.
- **Session persistence**: tmux sessions live on the *target* host, not in the container, so container restarts don't kill terminal state. VNC sessions are per-WebSocket and will drop on restart.
- **Updates**: rebuild image → `docker save` → re-import → recreate container with the same volumes.
- **Logs**: `slog` writes to stderr; visible in Container Manager → Container → Details → Log.
