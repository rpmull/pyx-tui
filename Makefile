# Pixra build. Needs only Go (1.22+); every target is a static binary (no cgo).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64

.PHONY: build test dist clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o pixra$(if $(filter windows,$(shell go env GOOS)),.exe,) ./cmd/pixra

test:
	go vet ./...
	go test ./...

# Cross-compile every platform into dist/.
dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/pixra-$(VERSION)-$$os-$$arch$$ext ./cmd/pixra || exit 1; \
	done

clean:
	rm -rf dist pixra pixra.exe
