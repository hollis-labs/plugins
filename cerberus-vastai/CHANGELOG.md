# Changelog

## v0.1.0

- Imported into the plugins repository from the former `cerberus-plugins` working branch (history kept). Vast.ai GPU rental connector: offer search with GPU, VRAM, price and reliability filters; create, start, stop, destroy and relabel instances; status and logs; cost reports grouped by instance label from billed charges.
- `max_price` is checked against the live offer and refuses when it cannot be priced. Dry runs show environment variable names and an `onstart` hash, never values. Requires Cerberus `v0.5.0-beta.2` (plugin protocol 2).
- Not yet tagged. `start`, `stop` and `set_label` have not been run against the live API.
