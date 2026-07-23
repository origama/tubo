## Summary

<!-- Brief description of what this PR changes and why. -->

## Verification

<!-- Which gates did you run? At minimum, the CI checks below must be green on this PR before merge. -->

- [ ] `go build ./...`
- [ ] `go test ./...`
- [ ] `go test -race ./...` (or CI `test` job)
- [ ] `golangci-lint run` (or CI `lint` job)
- [ ] `./tests/smoke-cli-ux.sh`
- [ ] `./tests/smoke-compose.sh`
- [ ] `./tests/verify-repo-hygiene.sh`

## No-merge-on-red

**A red required check blocks merge.** `test`, `lint`, `smoke-cli-ux`, and `smoke-compose` are required status checks on `main` (with `enforce_admins`, so admins cannot bypass a red check either). Do not merge while any of these is failing.

If a required check is genuinely flaky, do one of the following — do **not** silently merge on red:

- fix the flakiness in this PR or a follow-up PR;
- quarantine the flaky case with a tracked issue and an explicit `t.Skip` with a reason;
- demote the check to non-required with a tracked issue and an explicit decision record.

If docs/hygiene-only changes do not exercise a check, note that here and explain why the broader gate was skipped.

## Compatibility

- Product version:
- Protocol version:
- Protocol compatibility change:
- Operator action required:

## Docs / changelog

- [ ] `CHANGELOG.md` updated (Added / Changed / Fixed / Compatibility)
- [ ] Relevant `docs/` updated for any behavior, CLI, config, protocol, test, or operational change
