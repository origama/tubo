# Releasing `tubo`

This project uses the manual release flow described in `docs/VERSIONING.md`.

## Release artifacts

Each release must update or produce:

- `VERSION`
- `CHANGELOG.md`
- git tag `vX.Y.Z`
- GitHub Release from that tag

## Checklist

1. Ensure `main` is green.
2. Decide the next product version bump (`PATCH`, `MINOR`, `MAJOR`).
3. Decide whether protocol stays the same, bumps `minor`, or bumps `major`.
4. Update `VERSION`.
5. Add the new release section to `CHANGELOG.md`.
6. Run verification gates:
   - `go test ./...`
   - relevant smoke/integration/perf checks for the scope of the release
7. Commit the release prep.
8. Create the annotated tag:
   - `git tag -a vX.Y.Z -m "vX.Y.Z"`
9. Push commit and tag:
   - `git push origin main --follow-tags`
10. Create the GitHub Release using the matching changelog text.

## Build metadata

Release builds should inject:

- product version
- protocol version
- commit SHA
- build date

Example build pattern:

```bash
VERSION=$(cat VERSION)
COMMIT=$(git rev-parse HEAD)
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
go build -ldflags "-X p2p-api-tunnel/internal/version.ProductVersion=$VERSION -X p2p-api-tunnel/internal/version.Commit=$COMMIT -X p2p-api-tunnel/internal/version.BuildDate=$BUILD_DATE" ./cmd/tubo
```
