.PHONY: build test clean

build:
	go build -o bin/cerberus-fail2ban-plugin ./cmd/cerberus-fail2ban-plugin
	./bin/cerberus-fail2ban-plugin write-dist .

test:
	go test ./...

clean:
	rm -rf bin/ plugin.yaml
