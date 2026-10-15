# Giphy

Install `giphy/v0.2.0` through Nanite's plugin catalog. This subprocess plugin
uses plugin-sdk v0.6.1 and public host contract v0.1.6.

Use `/giphy cats` or the agent tool `giphy_search` with a `query`. Both attach a
`giphy-modal` card. Searches return deterministic built-in demo GIFs when no API
key is configured. Set the optional `giphy_api_key` secret for live searches;
the host can source it from `GIPHY_API_KEY`. Set `giphy_rating` to `g` (default),
`pg`, `pg-13`, or `r` for the maximum rating.

Live searches send the query and API key to Giphy over HTTPS. Cards load Giphy
images with no referrer. Requests have a five-second timeout, refuse redirects,
and bound response sizes. Request failures omit URLs and keys. The plugin holds
no durable user data.

## Upgrade from the standalone repository

The ID remains `giphy`, preserving its host-owned configuration namespace.
The old `search` tool is replaced by `giphy_search`. Re-enter any key previously
stored as ordinary configuration using the host secret control. Schema 1
manifests, signatures, signing commands and embedded keys are removed.
Reinstall the new release to replace an old bundle.

## Build

Run `make all` at the repository root. `make dist` in this directory builds the
binary, generated manifest, React ES module and envelope schema. React comes
from the host import map. The four platform release archives include these assets
and the Apache-2.0 license. DataDir and CacheDir stay outside the bundle.
