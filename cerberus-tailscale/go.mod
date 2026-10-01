module github.com/hollis-labs/cerberus-plugins/tailscale

go 1.26.8

replace github.com/hollis-labs/cerberus => ../../../apps/cerberus

replace github.com/hollis-labs/plugin-sdk => ../../plugin-sdk

require (
	github.com/hollis-labs/cerberus v0.5.0-beta.1
	github.com/hollis-labs/plugin-sdk v0.5.0
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/kr/pretty v0.3.1 // indirect
