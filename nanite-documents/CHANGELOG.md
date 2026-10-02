# Changelog

## 0.1.0

- Add session documents with pasted/uploaded text, inclusion and full-content or
  pointer-summary context settings.
- Preserve complete typed core exports with verified receipts and durable replay
  protection.
- Add create/list/get agent tools, bounded reads, HTTP routes and a primary
  Documents drawer tab; context settings and deletion remain user authority.
- Bound native storage growth, cache validated tables under the storage lock,
  and fall back from full context to pointers when the shared budget is small.
- Restrict agent list/get to user-included documents and show quota messages in
  the drawer; allow legacy toggle/timestamp changes above native growth limits.
