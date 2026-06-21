#!/bin/sh
# chepherd universal installer.
#
# Usage:
#   curl -fsSL https://chepherd.org/install.sh | sh
#
# Detects OS + arch, downloads the matching binary from the latest GitHub
# release, places it in ~/.local/bin (no sudo). Runs 'chepherd doctor' so
# the user sees what's missing immediately.

set -eu

REPO="chepherd/chepherd"
BIN_DIR="${HOME}/.local/bin"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

color() {
  case "$1" in
    info)  printf '\033[38;5;39m%s\033[0m\n' "$2" ;;
    ok)    printf '\033[38;5;82m%s\033[0m\n' "$2" ;;
    warn)  printf '\033[38;5;208m%s\033[0m\n' "$2" ;;
    err)   printf '\033[38;5;196m%s\033[0m\n' "$2" ;;
    *) printf '%s\n' "$2" ;;
  esac
}

uname_os() {
  case "$(uname -s)" in
    Linux*) echo linux ;;
    Darwin*) echo darwin ;;
    *)
      color err "unsupported OS: $(uname -s)" >&2
      exit 1
    ;;
  esac
}

uname_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *)
      color err "unsupported arch: $(uname -m)" >&2
      exit 1
    ;;
  esac
}

OS="$(uname_os)"
ARCH="$(uname_arch)"
color info "chepherd installer — detected ${OS}/${ARCH}"

# Resolve the most recent release tag — handles prereleases too.
# 1. Try the stable /releases/latest redirect.
# 2. If that yields a literal "latest" (no stable release), query the API
#    for the newest tag (prereleases included).
LATEST_URL="$(curl -sL -o /dev/null -w '%{url_effective}' \
  "https://github.com/${REPO}/releases/latest" 2>/dev/null || true)"
TAG="$(basename "${LATEST_URL}")"

if [ "${TAG}" = "latest" ] || [ "${TAG}" = "releases" ] || [ -z "${TAG}" ]; then
  # No stable release; fetch most recent tag (prerelease-aware).
  TAG="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" 2>/dev/null \
    | grep -oE '"tag_name":\s*"[^"]+"' \
    | head -1 \
    | sed -E 's/.*"([^"]+)"/\1/' || true)"
fi

if [ -z "${TAG}" ]; then
  color warn "no releases published yet — falling back to 'go install'"
  if command -v go > /dev/null 2>&1; then
    go install github.com/agenity-org/agenity@latest
    color ok "installed via go install"
    exit 0
  fi
  color err "and Go isn't installed either. Either install Go or wait for the first release."
  exit 1
fi

ASSET="chepherd_${TAG#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
color info "downloading ${URL}"
curl -fsSL "${URL}" -o "${TMP_DIR}/chepherd.tar.gz"

mkdir -p "${BIN_DIR}"
tar -xzf "${TMP_DIR}/chepherd.tar.gz" -C "${TMP_DIR}"
mv "${TMP_DIR}/chepherd" "${BIN_DIR}/chepherd"
chmod +x "${BIN_DIR}/chepherd"
color ok "installed: ${BIN_DIR}/chepherd"

# Path hint
if ! echo "${PATH}" | tr ':' '\n' | grep -q "^${BIN_DIR}\$"; then
  color warn "WARNING: ${BIN_DIR} is not in your PATH"
  color warn "add it via:"
  printf '\n    %s\n\n' "echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bashrc"
fi

# Quick doctor
if command -v chepherd > /dev/null 2>&1 || [ -x "${BIN_DIR}/chepherd" ]; then
  color info "running 'chepherd doctor'..."
  "${BIN_DIR}/chepherd" doctor || true
fi

color ok "done. start the dashboard with:  chepherd"
