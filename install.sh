#!/usr/bin/env bash
# agentdesk installer / updater
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/xdamman/agentdesk/main/install.sh | bash
#
# Environment variables:
#   AGENTDESK_VERSION   Pin to a specific tag (e.g. v0.1.0). Defaults to latest.
#   AGENTDESK_PREFIX    Override install directory. Defaults to /usr/local/bin if
#                       writable, else $HOME/.local/bin.

set -euo pipefail

REPO="xdamman/agentdesk"
BIN="agentdesk"

log()  { printf "\033[36m==>\033[0m %s\n" "$*"; }
warn() { printf "\033[33mwarn:\033[0m %s\n" "$*" >&2; }
die()  { printf "\033[31merror:\033[0m %s\n" "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"; }
need curl
need uname
need install
need mktemp

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux) ;;
  *) die "unsupported OS: $os — only linux binaries are published";;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) die "unsupported arch: $arch — supported: amd64, arm64";;
esac

# Resolve version — user-pinned, else latest.
version="${AGENTDESK_VERSION:-}"
if [ -z "$version" ]; then
  log "resolving latest tag..."
  version="$(curl -sSL -H "Accept: application/vnd.github+json" \
    "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
    | head -n 1)"
  [ -n "$version" ] || die "could not resolve latest version from GitHub API"
fi

# Resolve install directory.
prefix="${AGENTDESK_PREFIX:-}"
if [ -z "$prefix" ]; then
  if [ -w /usr/local/bin ]; then
    prefix="/usr/local/bin"
  elif [ "$(id -u)" = "0" ]; then
    prefix="/usr/local/bin"
  else
    prefix="${HOME}/.local/bin"
  fi
fi
mkdir -p "$prefix"

asset="${BIN}-${os}-${arch}"
url="https://github.com/${REPO}/releases/download/${version}/${asset}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

log "downloading ${asset} ${version}"
if ! curl -fsSL -o "${tmpdir}/${BIN}" "$url"; then
  die "download failed: ${url}"
fi

# Optional checksum verification.
if curl -fsSL -o "${tmpdir}/checksums.txt" \
     "https://github.com/${REPO}/releases/download/${version}/checksums.txt" 2>/dev/null; then
  expected="$(grep "  ${asset}\$" "${tmpdir}/checksums.txt" | awk '{print $1}' || true)"
  if [ -n "$expected" ] && command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${tmpdir}/${BIN}" | awk '{print $1}')"
    if [ "$expected" != "$actual" ]; then
      die "checksum mismatch: expected $expected got $actual"
    fi
    log "checksum ok"
  fi
fi

chmod +x "${tmpdir}/${BIN}"
install -m 0755 "${tmpdir}/${BIN}" "${prefix}/${BIN}"

log "installed ${BIN} ${version} to ${prefix}/${BIN}"

if ! printf '%s' ":$PATH:" | grep -q ":${prefix}:"; then
  warn "${prefix} is not on your PATH. Add this to your shell rc:"
  printf '\n  export PATH="%s:$PATH"\n\n' "$prefix"
else
  "${prefix}/${BIN}" --version 2>/dev/null || true
fi
