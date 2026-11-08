.PHONY: build release test vet fmt fmt-check check clean

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
		cp README.md README.en.md LICENSE config.env.example requirements-tools.txt requirements-browser-agent.txt requirements-tools.lock requirements-browser-agent.lock dist/$$name/; \
		cp -R assets dist/$$name/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
	done
	cd dist && (sha256sum *.tar.gz 2>/dev/null || shasum -a 256 *.tar.gz) > SHA256SUMS
	@echo "dist/ ready for $(VERSION)"

test:
	go test -count=1 ./...
	python3 scripts/test_install.py

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

check: fmt-check vet test

clean:
	go clean
	@echo "remove bin/ and dist/ by hand if you want them gone"
