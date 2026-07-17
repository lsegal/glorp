#!/usr/bin/env bash
set -euo pipefail

repo="${GLORP_REPO:-lsegal/glorp}"
version="${GLORP_VERSION:-latest}"
bin_dir="${GLORP_BIN_DIR:-$HOME/.local/bin}"
command -v gh >/dev/null 2>&1 || { echo "gh CLI is required: https://cli.github.com/" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
mkdir -p "$bin_dir"
if [[ "$version" == "latest" ]]; then
  version="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": "\([^"]*\)".*/\1/p' | head -n 1)"
  [[ -n "$version" ]] || { echo "could not resolve latest release" >&2; exit 1; }
fi
os_name="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
[[ "$arch" == "x86_64" ]] && arch="amd64"
[[ "$arch" == "aarch64" || "$arch" == "arm64" ]] && arch="arm64"
archive="glorp_${version#v}_${os_name}_${arch}.tar.gz"
url="https://github.com/$repo/releases/download/$version/$archive"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$url" -o "$tmp/$archive"
tar -xzf "$tmp/$archive" -C "$tmp"
install "$tmp/glorp" "$bin_dir/glorp"
npx --yes skills add "$repo@gh-fix" --global --agent codex --agent claude-code -y
echo "Installed glorp to $bin_dir/glorp and gh-fix globally."
