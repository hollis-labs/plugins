BINARY := tangent-plugin-messaging
ID := tangent.plugin.messaging
DIST := ../dist/$(ID)
BUILDINFO := github.com/hollis-labs/plugins/tangent-messaging/internal/buildinfo
# A release tag belongs to this Go submodule; archives can pass VERSION explicitly.
VERSION ?= $(shell git describe --exact-match --match 'tangent-messaging/v*' HEAD 2>/dev/null | sed 's|^tangent-messaging/||')

# Native Go VCS metadata can be absent in worktrees. Stamp the same facts explicitly.
define stamp
revision=$$(git rev-parse --verify HEAD 2>/dev/null || true); modified=; \
if [ -n "$$revision" ]; then \
 status=$$(git status --porcelain); modified=false; \
 if [ -n "$$status" ]; then modified=true; fi; \
fi; \
ldflags="-X $(BUILDINFO).Version=$(VERSION) -X $(BUILDINFO).Revision=$$revision -X $(BUILDINFO).Modified=$$modified";
endef

.PHONY: test lint build dist
test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	golangci-lint run --config ../.golangci.yml ./...

build:
	@set -eu; $(stamp) go build -buildvcs=true -ldflags "$$ldflags" ./...

# Tests run separately in CI and the author gate; packaging starts no plugin,
# endpoint or provider. The declaration hashes this exact native payload.
dist: build
	@set -eu; $(stamp) \
	mkdir -p "$$(dirname "$(DIST)")"; \
	stage=$$(mktemp -d "$(DIST).build.XXXXXX"); \
	trap 'rm -rf "$$stage"' EXIT; \
	trap 'exit 1' HUP INT TERM; \
	mkdir -p "$$stage/bin"; \
	go build -buildvcs=true -ldflags "$$ldflags" -o "$$stage/bin/$(BINARY)" ./cmd/$(BINARY); \
	"$$stage/bin/$(BINARY)" --manifest > "$$stage/plugin.yaml"; \
	rm -rf "$(DIST)"; \
	mv "$$stage" "$(DIST)"; \
	echo "installable plugin at $(DIST)"

# Real compiled consumer and the pinned public Tangent host; only test-owned
# databases, sockets and synthetic channel/gateway/dispatcher traffic.
.PHONY: smoke-stage1
smoke-stage1:
	go test -race -count=1 -timeout=240s -v ./integration -run '^TestStage1'
