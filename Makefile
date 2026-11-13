.PHONY: build windows release vet lint vuln fmt fmt-check check audit clean ui

VERSION := $(shell sed -n 's/^const Version = "\(.*\)"/\1/p' pkg/app/app.go)
GOFLAGS_REL := -buildvcs=false -trimpath -ldflags "-s -w"
GOFLAGS_GUI := -buildvcs=false -trimpath -ldflags "-s -w -H windowsgui -X main.launch=desktop"
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64
WINDOWS := amd64 arm64

build:
	mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS_REL) -o bin/macron ./cmd/macron

windows:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS_REL) -o bin/macron.exe ./cmd/macron
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS_GUI) -o bin/macron-desktop.exe ./cmd/macron

ui:
	cd pkg/desk/ui && npm ci --no-fund --no-audit && npm run build

release:
	mkdir -p dist
	for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; name=macron-$(VERSION)-$$os-$$arch; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_REL) -o dist/$$name/macron ./cmd/macron || exit 1; \
		cp README.md README.en.md SERVER.md LICENSE config.env.example requirements-tools.txt requirements-browser-agent.txt requirements-tools.lock requirements-browser-agent.lock dist/$$name/; \
		cp -R assets docs dist/$$name/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
	done
	for arch in $(WINDOWS); do \
		name=macron-$(VERSION)-windows-$$arch; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch go build $(GOFLAGS_REL) -o dist/$$name/macron.exe ./cmd/macron || exit 1; \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch go build $(GOFLAGS_GUI) -o dist/$$name/macron-desktop.exe ./cmd/macron || exit 1; \
		cp README.md README.en.md LICENSE config.env.example dist/$$name/; \
		cp -R assets dist/$$name/; \
		(cd dist && rm -f $$name.zip && zip -qr $$name.zip $$name) || exit 1; \
	done
	cd dist && (sha256sum *.tar.gz *.zip 2>/dev/null || shasum -a 256 *.tar.gz *.zip) > SHA256SUMS
	@echo "dist/ ready for $(VERSION)"

vet:
	go vet ./...

lint:
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

check: fmt-check vet build

audit: lint vuln

clean:
	go clean
	@echo "remove bin/ and dist/ by hand if you want them gone"
