# oEmbed link previews

Install `oembed/v0.2.0` through Nanite's plugin catalog. This subprocess plugin
uses plugin-sdk v0.6.1 and public host contract v0.1.6.

After a user message, the plugin checks at most five URLs and attaches the first
matching provider preview. It supports YouTube, Spotify, Vimeo, SoundCloud and
Flickr. Slash commands and exclamation commands do not trigger previews. Matching
URLs are sent to the provider's fixed oEmbed endpoint; the entire message is never
sent. Preview thumbnails load from URLs supplied by the provider with no referrer.
The card renders metadata and links, and never executes provider HTML.

Requests have a five-second timeout, refuse redirects, and limit response size.
A bounded, ten-minute in-memory cache avoids repeated requests; unloading clears
it. The plugin holds no durable user data and never copies the host message store.

## Extra providers and upgrades

The ID remains `oembed`, preserving its host-owned configuration namespace.
`oembed_extra_providers` now accepts a JSON array, replacing the old YAML setting:

```json
[{"name":"Example","pattern":"^https://example.com/posts/","endpoint":"https://example.com/oembed?url={url}"}]
```

Convert existing YAML to this format before enabling the new release. Each entry
requires a unique name, a Go regular expression and an HTTP(S) endpoint containing
exactly one `{url}` placeholder. Anchor patterns to the intended origin. Matching
URLs are sent to that explicitly configured endpoint. Unknown and duplicate JSON
fields, credential URLs and invalid endpoints are rejected. At most 32 extra
providers fit in a 64 KiB configuration. Schema 1 manifests, signing commands,
signatures and embedded keys are removed; reinstall to replace old bundles.

## Build

Run `make all` at the repository root. `make dist` in this directory builds the
binary, generated manifest, React ES module and envelope schema. React comes
from the host import map. Four platform archives include these assets and the
Apache-2.0 license. DataDir and CacheDir stay outside the bundle.
