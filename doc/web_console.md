# Web Console

LUMO ships two embedded consoles, both built from the `web/` workspace and
served by the binaries.

## lumos console

Enabled with `webServer.port` in `lumos.toml`, default port 7500.

- Visit `http://127.0.0.1:7500/`; unauthenticated visitors are redirected to
  `/login`.
- The console reads `/api/serverinfo`, `/api/proxies` and `/api/logs` and
  shows live status, traffic and the server event stream.

## lumoc console

Started with `lumoc ui` (default `127.0.0.1:7400`):

```bash
./lumoc ui -addr 127.0.0.1:7400 -user admin -pass change-me -store configs.json
```

- Connection configs are stored in the JSON file given by `-store` and can be
  created, edited, started and stopped from the console.
- Each enabled config runs as an independent tunnel connection inside the
  single `lumoc` process; per-connection events appear in the log view.

## Authentication

Both consoles authenticate through `POST /api/login` and issue an HttpOnly
session cookie. Five consecutive failures lock the origin out for ten
minutes. HTTP basic auth keeps working for scripts and monitoring.
