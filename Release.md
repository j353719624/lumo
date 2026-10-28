## Features

- First public release of LUMO, a self-contained reverse proxy and tunneling tool.
- Proxy types: TCP, UDP, HTTP (host routing) and HTTPS (SNI routing).
- HMAC-SHA256 token authentication with challenge-response handshake; the token never crosses the wire.
- LUMO server console (lumos): live status band, KPI counters, traffic sparkline, proxy table, service configuration and an event stream fed by the server log ring.
- LUMO client console (`lumoc ui`): multi-connection manager with create/edit/start/stop, per-connection event logs, lumoc.toml preview and hot apply without restarting the process.
- Form login with HttpOnly session cookies on both consoles; 5 consecutive failures lock an origin for 10 minutes. HTTP basic auth remains available for scripts.
- `verify` subcommand on both binaries validates configuration files.

## Fixes

- No fixes yet — this is the first release.
