# Avatar server

## 0.4.0

Browser access and a self-contained svg engine.

- Read-only HTTP surface (`AVATAR_HTTP_ADDR`): stored avatars as `GET /avatars/<name>.svg|.png|@8x.png`
  and unsaved previews as `GET /preview`. It never generates, spends or deletes, needs no token and
  allows cross-origin access only from `AVATAR_HTTP_ORIGINS`.
- `avatar_preview`: an svg avatar for a seed, with no files written and no palette slot taken.
- `avatar_delete`: takes an agent's avatar out of service by archiving it under a timestamped name.
- `avatar_generate` returns browser-loadable `urls` when `AVATAR_PUBLIC_URL` is set.
- The svg sprite is drawn in-process by default (`AVATAR_SVG_ENGINE=native`), under a millisecond and
  with no Hadron to run. It is a port of the workflow script and matches the workflow's output byte
  for byte on the golden cases. `AVATAR_SVG_ENGINE=hadron` keeps the workflow path.
- Build fix: `go-mcp` now lives at `github.com/hollis-labs/go-mcp`; the module pins v0.7.1.

## 0.3.0

Engine seam, fallback policy and spend limits for the paid model modes.
