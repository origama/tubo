# Releasing `tubo`

This project uses the manual release flow described in `docs/VERSIONING.md`.

## Release artifacts

Each release must update or produce:

- `VERSION`
- `CHANGELOG.md`
- git tag `vX.Y.Z`
- GitHub Release from that tag
- prebuilt `tubo` archives for:
  - Linux amd64
  - Linux arm64
  - macOS amd64
  - macOS arm64
  - Windows amd64
- `SHA256SUMS.txt`

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
10. Wait for `.github/workflows/release.yml` to create/update the GitHub Release and attach:
   - platform archives
   - `SHA256SUMS.txt`
11. Review the GitHub Release body and make sure it matches the new `CHANGELOG.md` section.
12. Post-release verification:
   - download one published archive (for example `tubo_X.Y.Z_linux_amd64.tar.gz`)
   - verify checksums with `SHA256SUMS.txt`
   - run `./tubo version` from the extracted archive and confirm it reports:
     - product version
     - protocol version
     - commit SHA
     - build date

The release workflow can also be re-run manually with `workflow_dispatch` for an existing tag.

## Build metadata

Release builds should inject or preserve:

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

The release workflow uses the same metadata injection and the resulting binaries must report it via `tubo version`.
