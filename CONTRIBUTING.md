# Contributing to LUMO

LUMO is better with help. Bug reports, feature requests, documentation fixes and pull requests are all
welcome.

## Before you start

- **Standard library only.** Neither the server nor the client pulls in third-party modules; a new
  dependency needs a strong justification.
- **The wire protocol is a contract.** Changing framing or message fields in `pkg/proto` is a breaking
  change — explain the compatibility plan in your PR.
- **Keep the consoles consistent.** Both panels share i18n keys and a visual language; UI changes should
  ship with a screenshot and both `en` and `zh` strings.

## Local setup

```bash
# requires Go 1.26+ and Node.js 20+

# consoles (web/ is an npm workspace)
cd web && npm ci && npm run build && cd ..

# build
make build          # or: go build -trimpath -ldflags "-s -w" -o bin/lumos ./cmd/lumos

# backend only, skip the embedded panel
go build -tags noweb -o bin/lumos ./cmd/lumos
go build -tags noweb -o bin/lumoc ./cmd/lumoc

# tests
go test ./...
./hack/run-e2e.sh   # spins up a server and a client and runs one tunnel end to end
```

## Before you commit

```bash
gofmt -l .          # must print nothing
go vet ./...
go test ./...
```

One commit does one thing. Write the subject as `type: summary` using `feat`, `fix`, `perf`, `refactor`,
`style`, `docs`, `chore` or `test`; explain the motivation in the body.

## Pull requests

1. Branch from `dev`: `feat/...` or `fix/...`
2. Describe the motivation and the blast radius; if behaviour changes, say how you verified it
3. User-facing changes need a screenshot or command output
4. Target `dev`

## Reporting issues

Please include:

- what happened and what you expected
- reproduction steps
- version (`lumos version` / `lumoc version`) and platform
- relevant logs — **strip IPs, tokens and secrets first**

## Security

Please do not open a public issue for a vulnerability; contact the maintainer privately through the
repository profile. LUMO masks addresses in logs by default — keep that habit when pasting logs.

## Ground rules

- No code or binaries of unknown origin
- Never commit real tokens, keys or server addresses
- Use the RFC 5737 documentation ranges for sample addresses (`192.0.2.0/24`, `198.51.100.0/24`,
  `203.0.113.0/24`)

## License and copyright

This project uses the **PolyForm Noncommercial License 1.0.0**: free to use, modify and share,
**commercial use requires the author's permission** (see [LICENSING.md](LICENSING.md)).

By opening a pull request you agree that:

1. your contribution is merged under the same license;
2. you have the right to submit that code, with no conflicting terms attached;
3. the author may also license versions containing your contribution under other terms, including
   commercial ones.

Point 3 is what makes commercial licensing of the project possible — please make sure you are comfortable
with it before submitting.
