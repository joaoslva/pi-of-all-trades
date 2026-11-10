GO ?= go
BIN = rootfs/usr/local/bin/pihub

.PHONY: build test
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags='-s -w' -o $(BIN) .

test:
	$(GO) vet ./... && $(GO) test ./...
