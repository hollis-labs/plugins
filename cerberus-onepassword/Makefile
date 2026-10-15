BINARY  := cerberus-onepassword-plugin
DIST    := ../dist/onepassword

.PHONY: test build dist clean

test:
	go test ./...

build:
	go build ./...

# CGO off: the SDK's desktop-app integration loads the 1Password app's native
# library and exists only in a cgo build. This plugin authenticates with a
# service account alone, so the binary cannot reach that path at all.
#
# plugin.yaml is generated from the connector definition by the binary itself,
# so the manifest the host installs cannot drift from what we serve.
dist: test
	mkdir -p $(DIST)/bin
	CGO_ENABLED=0 go build -o $(DIST)/bin/$(BINARY) ./cmd/$(BINARY)
	$(DIST)/bin/$(BINARY) write-dist $(DIST)
	@echo "installable plugin at $(DIST)"

clean:
	rm -rf $(DIST)
