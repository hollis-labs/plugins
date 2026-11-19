# Changelog

## v0.1.0

First tagged release of this module in this repository. Requires Tangent v0.21.0 and its host-callback client
(`pkg/plugin/hostclient`): the plugin reads the key file the host mints for it (`TANGENT_MCP_KEY_FILE`) on every call back
into `/mcp`. A build against Tangent v0.20.0 presents no key and is refused with 401 by a v0.21.0 host, so an installed copy
must be rebuilt from this release and reinstalled. Go 1.26.11.
