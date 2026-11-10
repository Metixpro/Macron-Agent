.PHONY: build release vet test lint vuln fmt fmt-check check audit clean

VERSION := $(shell sed -n 's/^const Version = "\(.*\)"/\1/p' pkg/app/app.go)
GOFLAGS_REL := -buildvcs=false -trimpath -ldflags "-s -w"
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64

build:
	mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS_REL) -o bin/macron ./cmd/macron

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
	cd dist && (sha256sum *.tar.gz 2>/dev/null || shasum -a 256 *.tar.gz) > SHA256SUMS
	@echo "dist/ ready for $(VERSION)"

vet:
	go vet ./...

test:
	go test ./...

lint:
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

check: fmt-check vet test build

audit: lint vuln

clean:
	go clean
	@echo "remove bin/ and dist/ by hand if you want them gone"
