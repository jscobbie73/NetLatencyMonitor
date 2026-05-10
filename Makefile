.PHONY: all build test lint tidy clean agent controller cover

GO        ?= go
BIN_DIR   := bin
PKGS      := ./...

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

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html
