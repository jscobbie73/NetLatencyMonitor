.PHONY: all build test lint tidy clean agent controller cover install templ

GO          ?= go
BIN_DIR     := bin
PKGS        := ./...
PREFIX      ?= /usr/local
SYSTEMD_DIR ?= /etc/systemd/system

all: lint test build

build: agent controller

agent:
	$(GO) build -trimpath -o $(BIN_DIR)/nlm-agent ./cmd/nlm-agent

controller:
	$(GO) build -trimpath -o $(BIN_DIR)/nlm-controller ./cmd/nlm-controller

test:
	$(GO) test -race -count=1 $(PKGS)

cover:
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -html=coverage.out -o coverage.html

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; skipping"

templ:
	go run github.com/a-h/templ/cmd/templ@v0.3.898 generate ./internal/ui/

tidy:
	$(GO) mod tidy

install: build
	install -D -m 755 $(BIN_DIR)/nlm-agent      $(DESTDIR)$(PREFIX)/bin/nlm-agent
	install -D -m 755 $(BIN_DIR)/nlm-controller  $(DESTDIR)$(PREFIX)/bin/nlm-controller
	install -D -m 644 deploy/systemd/nlm-controller.service      $(DESTDIR)$(SYSTEMD_DIR)/nlm-controller.service
	install -D -m 644 deploy/systemd/nlm-litestream.service      $(DESTDIR)$(SYSTEMD_DIR)/nlm-litestream.service
	install -D -m 644 deploy/systemd/nlm-agent-hub.service       $(DESTDIR)$(SYSTEMD_DIR)/nlm-agent-hub.service
	install -D -m 644 deploy/systemd/nlm-agent-listener.service  $(DESTDIR)$(SYSTEMD_DIR)/nlm-agent-listener.service
	install -D -m 644 deploy/systemd/nlm-agent-spoke.service     $(DESTDIR)$(SYSTEMD_DIR)/nlm-agent-spoke.service
	install -D -m 644 deploy/systemd/nlm-agent-spoke.timer       $(DESTDIR)$(SYSTEMD_DIR)/nlm-agent-spoke.timer
	@echo "Run: systemctl daemon-reload"

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html
