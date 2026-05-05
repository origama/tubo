#!/usr/bin/env bash
set -euo pipefail

REPO="${TUBO_REPO:-origama/tubo}"
INSTALL_DIR="${TUBO_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${TUBO_VERSION:-}"
VERIFY_CHECKSUM="${TUBO_VERIFY_CHECKSUM:-1}"

usage() {
  cat <<'EOF'
Tubo installer

Usage:
  install.sh [--version vX.Y.Z] [--install-dir DIR] [--no-verify]

Environment:
  TUBO_REPO              GitHub repository to install from. Default: origama/tubo
  TUBO_VERSION           Release tag to install, for example v0.1.3. Default: latest release
  TUBO_INSTALL_DIR       Destination directory. Default: $HOME/.local/bin
  TUBO_VERIFY_CHECKSUM   Set to 0 to skip SHA256SUMS verification. Default: 1
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    -v|--version)
      VERSION="${2:-}"
      if [ -z "$VERSION" ]; then
        echo "error: --version requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --install-dir)
      INSTALL_DIR="${2:-}"
      if [ -z "$INSTALL_DIR" ]; then
        echo "error: --install-dir requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --no-verify)
      VERIFY_CHECKSUM=0
      shift
      ;;
    *)
      echo "error: unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: '$1' is required" >&2
    exit 1
  }
}

need curl
need tar

raw_os="$(uname -s)"
raw_arch="$(uname -m)"

case "$raw_os" in
  Linux*) os="linux" ;;
  Darwin*) os="darwin" ;;
  *)
    echo "error: unsupported OS: $raw_os" >&2
    exit 1
    ;;
esac

case "$raw_arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "error: unsupported architecture: $raw_arch" >&2
    exit 1
    ;;
esac

resolve_latest_version() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' \
    | head -n 1
}

if [ -z "$VERSION" ]; then
  VERSION="$(resolve_latest_version)"
fi

if [ -z "$VERSION" ]; then
  echo "error: could not resolve the latest Tubo release" >&2
  echo "hint: pass --version vX.Y.Z or set TUBO_VERSION=vX.Y.Z" >&2
  exit 1
fi

case "$VERSION" in
  v*.*.*) ;;
  *)
    echo "error: release version must look like vX.Y.Z, got: $VERSION" >&2
    exit 1
    ;;
esac

version_no_v="${VERSION#v}"
asset="tubo_${version_no_v}_${os}_${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases/download/${VERSION}"

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

echo "Installing tubo ${VERSION} for ${os}/${arch}"
echo "Repository: https://github.com/${REPO}"
echo "Destination: ${INSTALL_DIR}"

curl -fL "${base_url}/${asset}" -o "${tmp}/${asset}"

if [ "$VERIFY_CHECKSUM" != "0" ]; then
  curl -fL "${base_url}/SHA256SUMS.txt" -o "${tmp}/SHA256SUMS.txt"
  (
    cd "$tmp"
    if command -v sha256sum >/dev/null 2>&1; then
      grep " ${asset}$" SHA256SUMS.txt | sha256sum -c -
    elif command -v shasum >/dev/null 2>&1; then
      grep " ${asset}$" SHA256SUMS.txt | shasum -a 256 -c -
    else
      echo "warning: no sha256sum or shasum found; skipping checksum verification" >&2
    fi
  )
else
  echo "warning: checksum verification disabled" >&2
fi

tar -xzf "${tmp}/${asset}" -C "$tmp"

src="${tmp}/tubo_${version_no_v}_${os}_${arch}/tubo"
if [ ! -x "$src" ]; then
  echo "error: expected executable not found in archive: $src" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
dst="${INSTALL_DIR}/tubo"

if command -v install >/dev/null 2>&1; then
  install -m 0755 "$src" "$dst"
else
  cp "$src" "$dst"
  chmod 0755 "$dst"
fi

echo "Installed: $dst"
"$dst" version || true

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo
    echo "Note: ${INSTALL_DIR} is not currently in your PATH."
    echo "Add it to your shell profile before using tubo from every shell."
    ;;
esac
