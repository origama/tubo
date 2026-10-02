# Website (www.tubo.click)

This directory is the GitHub Pages site, deployed from `main` by `.github/workflows/pages.yml`.

## Layout

- `index.html` — landing page
- `docs/` — documentation pages
- `install.sh` — one-line installer (serves release binaries from GitHub Releases)
- `assets/` — CSS, images, JS
- `CNAME` — custom domain `www.tubo.click`

## Source of truth

The website summarizes the product. Canonical technical documentation lives in the
repository's `docs/` directory (architecture, protocol, state, CLI, security,
implementation roadmap, and test acceptance). When behavior changes, update both.

## Notes

- Publishing: push to `main` (paths `site/**` or the workflow file) triggers a Pages build.
- The architecture docs that previously lived in the old prototype website
  (`reference/`, `runbooks/`, `reports/`, `design/`, `comparisons/`) are preserved in the
  private prototype archive repository, superseded by the repository's `docs/` tree.
- Keep `install.sh` in sync with the release asset naming
  (`tubo_<version>_<os>_<arch>.tar.gz` + `SHA256SUMS.txt`).
