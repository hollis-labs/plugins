BINARY := loom
DIST := ../dist/loom

.PHONY: dist

dist:
	mkdir -p $(DIST)/bin
	GOWORK=off CGO_ENABLED=0 go build -trimpath -o $(DIST)/bin/$(BINARY) .
	$(DIST)/bin/$(BINARY) --manifest > $(DIST)/plugin.yaml
