BINARY  := cerberus-namecheap-plugin
DIST    := ../dist/namecheap

.PHONY: test build dist clean

test:
	go test ./...

build:
	go build ./...

# plugin.yaml is generated from the connector definition by the binary itself,
# so the manifest the host installs cannot drift from the operations we serve.
dist: test
	mkdir -p $(DIST)/bin
	go build -o $(DIST)/bin/$(BINARY) ./cmd/$(BINARY)
	$(DIST)/bin/$(BINARY) write-dist $(DIST)
	@echo "installable plugin at $(DIST)"

clean:
	rm -rf $(DIST)
