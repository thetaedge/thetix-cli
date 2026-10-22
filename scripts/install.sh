#!/bin/sh
# Installs thetix on macOS or Linux.
#
#   curl -fsSL https://thetix.ai/install.sh | sh
#
# (thetix.ai/install.sh redirects to this file on GitHub:
#  https://raw.githubusercontent.com/thetaedge/thetix-cli/main/scripts/install.sh)
#
# Environment (all optional):
#   THETIX_VERSION         tag to install, e.g. v0.1.0 or 0.1.0 (default: latest)
#   THETIX_INSTALL_DIR     where to put the binary
#                          (default: /usr/local/bin if writable, otherwise ~/.local/bin)
#   THETIX_NO_MODIFY_PATH  set to 1 to leave your shell rc files alone
#   THETIX_BASE_URL        download from here instead of GitHub Releases (mirrors, tests)
#
# Needs only POSIX sh, tar, and curl or wget. The archive's SHA-256 is checked
# against the release's checksums.txt before anything is installed. The binary
# is static (no glibc or musl requirement).
#
# Everything runs from main, called on the last line, so a download cut off
# part way through (curl | sh) runs nothing.
set -eu

main() {

REPO="thetaedge/thetix-cli"
VERSION="${THETIX_VERSION:-latest}"
case "$VERSION" in
  [0-9]*) VERSION="v$VERSION" ;;
esac

say() { printf '%s\n' "$*" >&2; }
fail() { say "install.sh: $*"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

have tar || fail "missing required tool: tar"
if have curl; then
  download() { curl -fsSL --retry 3 --retry-delay 1 "$1" -o "$2"; }
elif have wget; then
  download() { wget -q "$1" -O "$2"; }
else
  fail "missing required tool: curl (or wget)"
fi

os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Darwin) goos=darwin ;;
  Linux) goos=linux ;;
  MINGW*|MSYS*|CYGWIN*) fail "on Windows run in PowerShell: irm https://thetix.ai/install.ps1 | iex" ;;
  *) fail "unsupported OS: $os" ;;
esac
case "$arch" in
  x86_64|amd64) goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *) fail "unsupported architecture: $arch (build from source: go install github.com/$REPO@latest)" ;;
esac
asset="thetix-$goos-$goarch.tar.gz"

if [ -n "${THETIX_BASE_URL:-}" ]; then
  base="${THETIX_BASE_URL%/}"
elif [ "$VERSION" = "latest" ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi

if [ -n "${THETIX_INSTALL_DIR:-}" ]; then
  dir="$THETIX_INSTALL_DIR"
elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t thetix)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset ($VERSION)..."
download "$base/$asset" "$tmp/$asset" || fail "could not download $base/$asset"
download "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download $base/checksums.txt"

say "Verifying checksum..."
expected="$(grep -E "[ *]$asset\$" "$tmp/checksums.txt" | head -n1 | awk '{print $1}')"
[ -n "$expected" ] || fail "no checksum for $asset in checksums.txt"
if have sha256sum; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
elif have shasum; then
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
elif have openssl; then
  actual="$(openssl dgst -sha256 "$tmp/$asset" | awk '{print $NF}')"
else
  fail "no sha256sum, shasum or openssl available to verify the download"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $asset (expected $expected, got $actual)"

tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/thetix" ] || fail "archive did not contain thetix"

mkdir -p "$dir" || fail "cannot create $dir (set THETIX_INSTALL_DIR to a writable directory)"
[ -w "$dir" ] || fail "cannot write to $dir (set THETIX_INSTALL_DIR to a writable directory, or re-run with sudo)"
# Copy to a temp name and rename so a running thetix is never half-overwritten.
cp "$tmp/thetix" "$dir/.thetix.new"
chmod 0755 "$dir/.thetix.new"
mv -f "$dir/.thetix.new" "$dir/thetix"
if [ "$goos" = darwin ] && have xattr; then
  xattr -d com.apple.quarantine "$dir/thetix" 2>/dev/null || true
fi

if ! installed="$("$dir/thetix" --version 2>&1)"; then
  say "$installed"
  fail "$dir/thetix was installed but does not run on this system"
fi
say "Installed $installed to $dir/thetix"

# Put thetix on PATH: one line in the shell's rc file, unless
# THETIX_NO_MODIFY_PATH is set or the file already mentions the directory.
case ":$PATH:" in
  *":$dir:"*) on_path=1 ;;
  *) on_path=0 ;;
esac
if [ "$on_path" = 0 ]; then
  case "$dir" in
    "$HOME"/*) dir_expr="\$HOME${dir#"$HOME"}" ;;
    *) dir_expr="$dir" ;;
  esac
  case "$(basename "${SHELL:-sh}")" in
    zsh) rc="${ZDOTDIR:-$HOME}/.zshrc"; line="export PATH=\"$dir_expr:\$PATH\"" ;;
    bash) rc="$HOME/.bashrc"; line="export PATH=\"$dir_expr:\$PATH\"" ;;
    fish) rc="${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish"; line="fish_add_path -g \"$dir_expr\"" ;;
    *) rc="$HOME/.profile"; line="export PATH=\"$dir_expr:\$PATH\"" ;;
  esac
  if [ "${THETIX_NO_MODIFY_PATH:-0}" != 0 ]; then
    say ""
    say "$dir is not on your PATH. Add it, e.g.:"
    say "  $line"
  elif [ -f "$rc" ] && grep -qF "$dir_expr" "$rc"; then
    say ""
    say "$rc already adds $dir to PATH; open a new terminal to pick it up."
  else
    mkdir -p "$(dirname "$rc")"
    printf '\n# thetix (https://github.com/%s)\n%s\n' "$REPO" "$line" >> "$rc"
    say ""
    say "Added $dir to PATH in $rc. Open a new terminal, or run:"
    say "  $line"
  fi
fi

say ""
say "Next: run  thetix login  to connect your Thetix account."
}

main "$@"
