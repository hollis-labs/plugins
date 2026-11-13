# Changelog

## v0.1.0

First tagged release of this module in this repository (its version is stamped from this tag when it is built with
`make`). Requires Tangent v0.21.0 and its host-callback client (`pkg/plugin/hostclient`): the plugin reads the key file the
host mints for it (`TANGENT_MCP_KEY_FILE`) on every call back into `/mcp`. A build against Tangent v0.20.0 presents no key
and is refused with 401 by a v0.21.0 host, so an installed copy must be rebuilt from this release and reinstalled. Go 1.26.11.

The integration fixture now starts the host the way v0.21.0 is started (a Substrate config file for the listen address,
`identity setup` and `identity key create` offline, sign-in for the owner's requests), and gives the plugin its key file.
