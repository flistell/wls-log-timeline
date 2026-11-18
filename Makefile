GO ?= go

.PHONY: build test check parity release
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o wls-events-timeline .

test:
	$(GO) test ./...

check: test
	$(GO) vet ./...

parity: build
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/check-parity.py

release:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 $(GO) build -trimpath -ldflags="-s -w" -o dist/wls-events-timeline-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOARM64=v8.0 $(GO) build -trimpath -ldflags="-s -w" -o dist/wls-events-timeline-linux-arm64 .
	cd dist && sha256sum wls-events-timeline-linux-amd64 wls-events-timeline-linux-arm64 > SHA256SUMS
