#!/bin/sh
set -e
cd "$(dirname "$0")/.."

# Run the end-to-end suite. Extra arguments are passed to `go test`.
exec go test -count=1 ./test/e2e/... "$@"
