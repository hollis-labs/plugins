# Makefile — nanite-plugins-catalog
#
# Targets:
#   build    : run scripts/build-catalog.go, emit dist/catalog.yaml
#   sign     : sign dist/catalog.yaml using the root key from 1Password
#   publish  : wrangler pages deploy dist --project-name=nanite-plugins-catalog
#   verify   : verify dist/catalog.yaml.sig against a local copy of the public key
#   clean    : rm -rf dist

.PHONY: build sign publish verify clean all

all: build sign

build:
	go run ./scripts/build-catalog.go

sign:
	./scripts/sign-catalog.sh dist/catalog.yaml

publish:
	wrangler pages deploy dist --project-name=nanite-plugins-catalog --branch=main --commit-dirty=true

verify:
	@if [ ! -f dist/catalog.yaml.sig ]; then echo "dist/catalog.yaml.sig missing — run make sign"; exit 1; fi
	@op read "op://Nanite/nanite-plugin-catalog-signing-key/public-key" > /tmp/nanite-catalog-pub.pem
	openssl pkeyutl -verify -pubin -inkey /tmp/nanite-catalog-pub.pem -rawin -in dist/catalog.yaml -sigfile dist/catalog.yaml.sig
	@rm -f /tmp/nanite-catalog-pub.pem

clean:
	rm -rf dist
