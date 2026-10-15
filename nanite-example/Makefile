BINARY := example
DIST := ../dist/example

.PHONY: dist

dist:
	mkdir -p $(DIST)/bin $(DIST)/ui
	GOWORK=off CGO_ENABLED=0 go build -trimpath -o $(DIST)/bin/$(BINARY) .
	$(DIST)/bin/$(BINARY) --manifest > $(DIST)/plugin.yaml
	cp ui/index.js $(DIST)/ui/index.js
