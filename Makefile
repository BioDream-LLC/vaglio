GO      ?= go
NPM     ?= npm
BIN     := bin/vaglio
PKG     := ./cmd/vaglio
VERSION ?= v0.0.1
EXT     := .vaglio
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build web test vet fmt check check-policies serve clean

all: check build

# Pure Go, no cgo: one static binary with the console embedded, as Perfuse ships.
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

# Rebuilds the console into internal/web/dist, which is committed so `go build` works without Node.
web:
	cd web && $(NPM) ci && $(NPM) run build

test:
	$(GO) test -race ./...
	cd web && $(NPM) test

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w cmd internal examples library tools conformance

check-policies: build
	$(BIN) check policies/*$(EXT) library/*$(EXT)

# check prints its marker only if everything above it passed; an absent marker is a failure, whatever else the output
# says (Perfuse's Makefile records why).
check:
	@test -z "$$(gofmt -l cmd internal examples library tools conformance)" || (gofmt -l cmd internal examples library tools conformance; echo "gofmt: files above need formatting"; exit 1)
	$(GO) vet ./...
	$(GO) test -race ./...
	cd web && $(NPM) run typecheck && $(NPM) test
	$(MAKE) check-policies
	@echo "CHECK PASSED"

serve: build
	$(BIN) serve -data data

clean:
	rm -rf bin
