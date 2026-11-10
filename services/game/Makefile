GO ?= go
BIN = rootfs/usr/local/bin/guessing-day
SHARE = rootfs/usr/share/guessing-day

.PHONY: build test
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags='-s -w' -o $(BIN) .
	rm -rf $(SHARE) && mkdir -p $(SHARE)
	cp -r web content $(SHARE)/
	rm -rf $(SHARE)/web/mock $(SHARE)/content/characters.draft.json

test:
	$(GO) vet ./... && $(GO) test ./...
