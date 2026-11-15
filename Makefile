GO ?= go
NODE ?= node
BIN := bin/opticdeck
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DATADIR ?= $(PREFIX)/share

.PHONY: all build install test race vet smoke frontend-test clean

all: build

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -o $(BIN) ./cmd/semadesk
	ln -sfn opticdeck bin/semadesk

install: build
	install -Dm755 $(BIN) "$(DESTDIR)$(BINDIR)/opticdeck"
	ln -sfn opticdeck "$(DESTDIR)$(BINDIR)/semadesk"
	install -Dm644 packaging/semadesk.desktop "$(DESTDIR)$(DATADIR)/applications/opticdeck.desktop"
	install -Dm644 packaging/semadesk.svg "$(DESTDIR)$(DATADIR)/icons/hicolor/scalable/apps/opticdeck.svg"

test:
	$(GO) test ./...

race:
	CGO_ENABLED=1 $(GO) test -race ./...

vet:
	$(GO) vet ./...

smoke: build
	./scripts/smoke.sh

frontend-test:
	$(NODE) tests/frontend.cjs

clean:
	rm -rf bin
