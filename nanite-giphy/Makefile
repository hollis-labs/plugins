BINARY := giphy
DIST := ../dist/giphy

.PHONY: dist

dist:
	mkdir -p $(DIST)/bin $(DIST)/ui $(DIST)/schemas
	GOWORK=off CGO_ENABLED=0 go build -trimpath -o $(DIST)/bin/$(BINARY) .
	$(DIST)/bin/$(BINARY) --manifest > $(DIST)/plugin.yaml
	cp ui/index.js $(DIST)/ui/index.js
	cp schemas/*.json $(DIST)/schemas/
