# sysmon

Cerberus connector that reports the machine the plugin runs on as typed
objects: CPU, memory, per-mount disk, load, GPUs, OS identity and uptime. It is
the data source for a Host probe, and every operation is a read.

> **Pre-release.** No outside users, no compatibility guarantees, no support
> channel. Interfaces and behaviour can change without notice.

## Operations

| Operation | Effect | Arguments | Returns |
|---|---|---|---|
| `cpu` | read | none | `model`, `cores` (logical), `load` (`one`/`five`/`fifteen`), `usage_percent` (Linux only, a 200 ms sample) |
| `memory` | read | none | `total_bytes`, `free_bytes`, `available_bytes`, `used_bytes`, `swap_total_bytes`, `swap_free_bytes` |
| `disk` | read | optional `path` (default `/`) | `mounts[]`: `mount`, `device`, `fs_type`, `size_bytes`, `used_bytes`, `avail_bytes`, `used_percent` for the filesystem holding `path` |
| `host` | read | none | `hostname`, `os`, `os_version`, `arch`, `kernel`, `uptime_seconds` |
| `host_report` | read | none | everything above in one call: `host`, `cpu`, `memory`, `disks[]` (every real mount), `gpus[]`, `warnings[]` |

```bash
cerberus connectors exec sysmon host_report
cerberus connectors exec sysmon disk --arg path=/
```

**Output shape changed in 0.2.0.** Earlier versions returned a JSON document
inside a string (gopsutil structs, serialized and then quoted). Every operation
now returns a JSON object whose shape is declared in `output_schema`, so a
consumer no longer parses a string. The field names differ from gopsutil's.

## Behaviour worth knowing

- `host_report` is best effort. A section that cannot be read is left empty and
  named in `warnings` (fixed text, never command output); the call still
  succeeds. The individual operations fail instead.
- `gpus` is always a list. It is empty when no supported tool is present or a
  tool errors. Sources: `nvidia-smi` and `rocm-smi` (Linux), `system_profiler
  SPDisplaysDataType -json` (macOS). Apple Silicon reports `cores` and no
  `memory_total_bytes`, because its memory is unified.
- `disks` skips pseudo filesystems (tmpfs, overlay, squashfs, ...) and, on
  Linux, repeat mounts of one block device. On macOS it keeps `/dev/` volumes
  and drops the hidden `/System/Volumes/*` helpers except `Data`.
- `available_bytes` is `MemAvailable` on Linux and free + inactive +
  speculative + purgeable pages on macOS.
- Linux and macOS only. Other systems get an explicit unsupported error.
- Memory and disk figures are bytes. Host-originated text (hostname, CPU and
  GPU model, mount points) is labelled `untrusted` in the output schema.

## Tools and PATH

Commands (`df`, `sysctl`, `vm_stat`, `sw_vers`, `nvidia-smi`, `rocm-smi`,
`system_profiler`) are resolved on every call: `PATH` first, then fixed
fallback directories (`/usr/bin`, `/bin`, `/usr/sbin`, `/sbin`,
`/usr/local/bin`, `/opt/homebrew/bin`, `/opt/rocm/bin`, `/usr/lib/rocm/bin`,
the usual NVIDIA locations). A daemon with launchd's minimal `PATH` therefore
still finds them. Linux data otherwise comes from `/proc` and `/etc/os-release`.

The plugin runs only inspection commands and writes nothing. It declares no
secrets and needs no credentials or capabilities.

## Tests

`make test` runs against recorded command output and fake files only, with a
fake `Env` standing in for the machine. Nothing in the suite touches the real
host. The macOS fixtures are hand-trimmed from the documented output formats of
`sysctl`, `vm_stat` and `system_profiler`; they have not been replayed on a Mac.
