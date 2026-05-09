# Issue #91 — Namespace-scoped service listing and query authorization

Status: DONE
Updated: 2026-05-09 21:20 UTC

## Summary
- Added namespace-aware service listing and lookup authorization for `get services`, `get service/...`, `describe`, `inspect`, and `watch`.
- Service queries now stay on the current namespace by default, authorize explicit namespace selection when a valid membership capability is present, and support `get services -A` only when every namespace is authorized or a broad capability (`NamespaceID="*"`) is present.

## Notes
- Uses membership/list capabilities from the capability foundation introduced in #79.
- Legacy discovery behavior stays intact when cluster-aware metadata is absent.
