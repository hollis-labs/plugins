.PHONY: build test clean
build:
	go build -o bin/cerberus-postgres-data ./cmd/cerberus-postgres-data
	./bin/cerberus-postgres-data write-dist .
test:
	go test ./...
clean:
	rm -rf bin/ plugin.yaml
