# Changelog

## 0.1.0

- Extract Fragments Engine callback translation and three Loom pilot reminder
  declarations into a headless subprocess plugin.
- Use a scoped, revocable durable-agent wake grant; keep execution and profile
  provisioning in core, and report successful submission as queued work.
- Require bounded strict callback JSON and never retry uncertain wakes.
- Retire the old core callback route on host adoption; configure destinations
  under `/api/plugins/nanite.loom/curator-wake`.
