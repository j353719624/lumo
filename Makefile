export PATH := $(PATH):`go env GOPATH`/bin
export GO111MODULE=on
LDFLAGS := -s -w
NOWEB_TAG = $(shell [ ! -d web/lumos/dist ] || [ ! -d web/lumoc/dist ] && echo ',noweb')

.PHONY: all env fmt fmt-more gci vet web web-ci lumos-web lumoc-web lumos lumoc build test gotest e2e alltest clean

all: env fmt web build

build: lumos lumoc

env:
	@go version

web: lumos-web lumoc-web

lumos-web:
	$(MAKE) -C web/lumos build

lumoc-web:
	$(MAKE) -C web/lumoc build

web-ci:
	cd web && npm ci && npm run test:unit && npm run build

fmt:
	go fmt ./...

fmt-more:
	gofumpt -l -w .

gci:
	gci write -s standard -s default -s "prefix(github.com/j353719624/lumo/)" ./

vet:
	go vet -tags "$(NOWEB_TAG)" ./...

lumos:
	env CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -tags "lumos$(NOWEB_TAG)" -o bin/lumos ./cmd/lumos

lumoc:
	env CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -tags "lumoc$(NOWEB_TAG)" -o bin/lumoc ./cmd/lumoc

test: gotest

gotest:
	go test -tags "$(NOWEB_TAG)" ./...

e2e:
	go test -tags "$(NOWEB_TAG)" -count=1 ./test/e2e/...

alltest: vet gotest e2e

clean:
	rm -rf ./bin ./release ./web/lumos/dist ./web/lumoc/dist
