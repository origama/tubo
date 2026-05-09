# Issue #10 — distributed pid/process management for failure injection

Status: Done

Notes:
- Hardening the distributed failure harness so restart injection does not rely on stale pid files alone.
- Added cleanup that clears pidfiles and listeners before relaunching, and fixed the large-payload request path so it no longer hits shell argument-length limits.
- Applied the same approach to the distributed smoke setup/cleanup and to the Linode perf restart path so repeatable campaigns do not require manual cleanup.
- Verified with `go test ./...`, `./tests/smoke-distributed-two-host.sh`, and `./tests/failure-campaign-two-host.sh`.
- Issue closed on GitHub after acceptance criteria were confirmed complete.
