BINARY := diagnostics
DIST := ../dist/diagnostics

.PHONY: dist

dist:
	mkdir -p $(DIST)/bin $(DIST)/ui
	GOWORK=off CGO_ENABLED=0 go build -trimpath -o $(DIST)/bin/$(BINARY) .
	$(DIST)/bin/$(BINARY) --manifest > $(DIST)/plugin.yaml
	cp ui/*.js $(DIST)/ui/
