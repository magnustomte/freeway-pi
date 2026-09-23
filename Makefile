# arm64 is the primary target: Pi 3 and later on 64-bit Raspberry Pi OS. armv7
# is built too, so a Pi 2 remains a working fallback rather than a theoretical
# one.

# Three parts rather than one. "git describe" on an untagged repository gives
# a bare hash like 03ecb66-dirty, which is the right thing in a log and a
# baffling thing to read at the top of a web page. So the release name, the
# commit and the build date are separate, and the interface shows the one that
# means something with the others as fine print.
VERSION := $(shell git describe --tags --exact-match 2>/dev/null || echo 0.1)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)$(shell git diff --quiet 2>/dev/null || echo +dirty)
BUILT   := $(shell date -u +%Y-%m-%d)
GOFLAGS := -trimpath
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.built=$(BUILT)

.PHONY: all test vet fmt fwprobe freewayd dist clean deploy

all: test vet

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Native builds, for developing on the laptop.
fwprobe:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/fwprobe ./cmd/fwprobe

freewayd:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/freewayd ./cmd/freewayd

# Cross-compiled binaries to copy to the Pi. No cgo, so this needs no
# cross-toolchain at all.
dist:
	mkdir -p dist
	for cmd in fwprobe freewayd; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
			go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/$$cmd-linux-arm64 ./cmd/$$cmd || exit 1; \
		CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
			go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/$$cmd-linux-armv7 ./cmd/$$cmd || exit 1; \
	done

clean:
	rm -rf bin dist

# Copy a build and the packaging to the Pi and install it there. HOST is not
# stored in the repository. ARCH is arm64 for a Pi 3 and later on 64-bit
# Raspberry Pi OS, armv7 for a Pi 2 or a 32-bit install — install the wrong one
# and the binary will not start.
ARCH ?= arm64
deploy: dist
	@test -n "$(HOST)" || { echo "usage: make deploy HOST=user@address [ARCH=arm64|armv7]"; exit 2; }
	ssh $(HOST) 'mkdir -p /tmp/freeway-install'
	scp dist/freewayd-linux-$(ARCH) dist/fwprobe-linux-$(ARCH) packaging/* \
		$(HOST):/tmp/freeway-install/
	ssh $(HOST) 'cd /tmp/freeway-install && chmod +x install.sh && sudo ./install.sh freewayd-linux-$(ARCH)'
