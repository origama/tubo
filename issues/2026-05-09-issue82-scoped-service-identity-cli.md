# Issue #82 — scoped service identity and namespace-aware service commands

Status: Done

Notes:
- Added service/ prefix parsing for connect and attach shorthand rewriting.
- Added current cluster / namespace resolution helpers for connect, get, describe, and inspect.
- Extended service discovery results with optional scope metadata so future discovery-v2 work can carry cluster/namespace context without changing the published discovery format.
- Updated CLI docs and tests for scoped service refs, namespace overrides, and all-namespaces listing.
- Verified with `go test ./...` and `./tests/smoke-compose.sh`.
- Issue closed on GitHub after acceptance criteria were confirmed complete.
