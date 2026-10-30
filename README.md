# LUMO

LUMO is a self-contained reverse-tunneling tool: it exposes services behind NAT or a firewall on the
public internet. The server `lumos` runs on a host with a public IP, the client `lumoc` runs inside your
network; they build a control channel over an authenticated handshake and open data connections on demand.

- **Zero third-party dependencies** — Go standard library only, small supply-chain surface
- **Own protocol** — length-framed JSON control messages with Ed25519 signatures; not interoperable with
  other tunneling tools
- **Bilingual consoles** — built-in server and client web panels (English / Chinese), live status,
  traffic and runtime events
- **Optional secrets** — sealed tunnels (`sealed-tcp` / `sealed-udp`) expose nothing publicly, and
  `punch` establishes peer-to-peer links with automatic relay fallback

## Proxy types (v0.1.0)

| Type | Description |
|---|---|
| `tcp` | Remote port mapping to an internal TCP service |
| `udp` | Remote port mapping, replies routed per visitor address |
| `http` | Virtual host routed by domain (`vhostHTTPPort`) |
| `https` | Routed by TLS SNI, TLS stays end-to-end at the local service |
| `sealed-tcp` | Secret-key tunnel, no public port |
| `sealed-udp` | Secret-key tunnel over UDP, no public port |
| `punch` | Peer-to-peer after address exchange, relays if punching fails |
| `mux` | HTTP CONNECT multiplexed entry port |

Also included: token authentication (Ed25519 challenge), automatic reconnect with backoff, config
hot-reload, `verify` for config files, TOTP two-factor login, traffic accounting, runtime events and a
multi-connection manager.

## Multi-connection console (recommended)

Instead of hand-writing config files, open the GUI console with `lumoc ui`: create, edit, start and stop
any number of tunnels, all stored in one local JSON file and hosted by a single process.

```bash
./bin/lumoc ui                          # default http://127.0.0.1:7400
./bin/lumoc ui -addr 0.0.0.0:7400 -user admin -pass 'a-strong-one' -store /data/configs.json
```

Click "New connection", fill in the server address, secret and port mappings. The switch starts or stops a
tunnel immediately — no process restart.

Useful subcommands:

```bash
./bin/lumoc init   --store c.json --server 203.0.113.10 --port 58619 --token <secret>
./bin/lumoc server --out lumos.toml --bind-port 58619 --kcp-port 47011 --web-port 46351
./bin/lumoc discover -c lumoc.toml      # list sealed tunnels other clients registered
```

## Build

Requires Go 1.26+ and Node.js 20+ (the Go binaries do not embed an unbuilt frontend).

```bash
# 1. build both Vue 3 consoles (web/ is an npm workspace)
cd web && npm ci && npm run build && cd ..

# 2. compile the binaries (the panels are embedded via //go:embed)
make build        # or: go build -trimpath -ldflags "-s -w" -o bin/lumos ./cmd/lumos
```

Backend only, without embedding the panels:

```bash
go build -tags noweb -o bin/lumos ./cmd/lumos
go build -tags noweb -o bin/lumoc ./cmd/lumoc
```

Full development flow:

```bash
make all      # env + fmt + web + build
make alltest  # vet + unit tests + e2e
```

## Quick start

Server `lumos.toml`:

```toml
bindPort = 58619
kcpPort = 47011
vhostHTTPPort = 18080

[auth]
# derive this from your secret, or keep a plain auth.token during testing
pubKey = "<Ed25519 public key>"

[webServer]
addr = "0.0.0.0"
port = 46351
user = "admin"
password = "change-me"
```

`./bin/lumoc server --out lumos.toml ...` generates this file and prints the matching secret.

Client `lumoc.toml`:

```toml
serverAddr = "203.0.113.10"
serverPort = 58619
protocol = "kcp"
kcpPort = 47011

[auth]
token = "same secret as the server"

# TCP tunnel
[[proxies]]
name = "ssh"
type = "tcp"
localIP = "127.0.0.1"
localPort = 22
remotePort = 6022

# HTTP tunnel routed by domain
[[proxies]]
name = "site"
type = "http"
localPort = 8080
customDomains = ["app.example.com"]

# Secret-key tunnel: no public port at all
[[proxies]]
name = "nas-admin"
type = "sealed-tcp"
localIP = "127.0.0.1"
localPort = 5666
sk = "a-long-secret"
```

Start and verify:

```bash
./bin/lumos verify -c lumos.toml   # validate only
./bin/lumos -c lumos.toml
./bin/lumoc -c lumoc.toml
```

Then open `http://127.0.0.1:46351` (server console) or `http://127.0.0.1:7400` (client console) and sign in
with the credentials from `webServer`.

## Security notes

- `auth.pubKey` is derived from the tunnel secret, so the secret itself never has to be stored on the
  server; keep it 32+ characters
- Panels listen on `127.0.0.1` by default — change the password before exposing them, and consider TOTP
- Prefer `sealed-tcp` / `sealed-udp` over public port mappings for anything sensitive
- Logs mask addresses by default (`log.MaskAddr`), so no raw IPs end up in your journal

## Layout

```
cmd/lumos            server entrypoint
cmd/lumoc            client entrypoint (ui / engine / discover / init / server)
pkg/config           config load, validation and write-back
pkg/toml             TOML subset parser
pkg/proto            wire protocol: framing, authentication, packets
pkg/relay            bidirectional copy, rate limiting, traffic counters
pkg/server           server: sessions, registration, forwarding, vhost, console
pkg/client           client: control loop, work connections, visitors, punching
pkg/manager          multi-connection manager and client console
pkg/auth             web sessions and login rate limiting
pkg/token            Ed25519 token derivation and verification
pkg/totp             RFC 6238 one-time passwords
pkg/qrcode           standalone QR encoder (for 2FA enrolment)
pkg/udpstream        KCP reliable UDP transport
pkg/stun             STUN reflexive address discovery (for punching)
pkg/log              logging, address masking, runtime event ring buffer
web/lumos            server console (Vue 3 + Vite)
web/lumoc            client console (Vue 3 + Vite)
web/shared           i18n and helpers shared by both consoles
```

## Contributing

LUMO is open for collaboration:

- Bugs and feature requests: [Issues](../../issues)
- Development setup and PR flow: [CONTRIBUTING.md](CONTRIBUTING.md)
- fnOS client package: [lumo-fpk](https://github.com/j353719624/lumo-fpk)

## License

**PolyForm Noncommercial License 1.0.0** — free to use, study, modify and share, **not for commercial use**.

Commercial use requires the author's permission; see [LICENSING.md](LICENSING.md) for what is allowed and
how to obtain a license. Full text: [LICENSE](LICENSE).
