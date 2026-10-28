# AGENTS.md

## Development Commands

### Build
- `make build` - Build both lumos and lumoc binaries
- `make lumos` - Build server binary only
- `make lumoc` - Build client binary only
- `make all` - Build everything (fmt, web assets, binaries)

### Testing
- `make test` - Run unit tests
- `make e2e` - Run end-to-end tests
- `make alltest` - Run vet, unit tests, and e2e

### Code Quality
- `make fmt` - Run go fmt
- `make fmt-more` - Run gofumpt for stricter formatting
- `make gci` - Run gci import organizer
- `make vet` - Run go vet
- `golangci-lint run` - Comprehensive linting (configured in .golangci.yml)

### Web assets
- `make web` - Build both console dashboards (npm workspaces + Vite)
- `make web-ci` - npm ci, unit tests and build (used by CI)

### Configuration helpers
- `./bin/lumos verify -c lumos.toml` - Validate a server config
- `./bin/lumoc verify -c lumoc.toml` - Validate a client config

### Cleanup
- `make clean` - Remove binaries, release output and console builds

## Layout

- `cmd/lumos`, `cmd/lumoc`: entry points only.
- `pkg/`: implementation packages (server, client, manager, config, proto, relay, auth, log, toml, version).
- `web/`: npm workspace with the two Vue 3 + Vite consoles; each builds `dist/` which is embedded through `//go:embed`. Use `-tags noweb` to build without assets.
- `conf/`: example configuration files shipped in release archives.

## Conventions

- Standard library only for Go code; no third-party Go dependencies.
- Wire format changes must stay backward compatible within a major version; message fields are additive.
- Keep the console API contract in sync with `web/*/src/api.ts` when touching `pkg/server` or `pkg/manager` handlers.
