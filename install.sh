#!/bin/sh
# Remote Hands installer for macOS and Linux.
#
# Paste this into Terminal (it downloads the program, checks it, and starts it;
# on a Mac it also puts a "Remote Hands" starter on the Desktop):
#
#   curl -fsSL https://raw.githubusercontent.com/HamsterPark/remote-hands/main/install.sh | sh
#
# Window language: write  REMOTE_HANDS_LANG=zh sh  (or cs, en) instead of  sh.
# Running it again installs the newest version.
set -eu

REPO="HamsterPark/remote-hands"
BASE="${REMOTE_HANDS_DOWNLOAD:-https://github.com/$REPO/releases/latest/download}"

say() { printf '%s\n' "$*"; }
fail() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "This installer is for macOS and Linux. On Windows, use the PowerShell command from the guide." ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
armv7l | armv6l | armhf) arch=arm ;;
*) fail "Unsupported processor: $(uname -m)" ;;
esac
# Apple Silicon Macs report x86_64 inside Rosetta; use the native program.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64
fi

asset="remote-hands-$os-$arch"
dir="$HOME/.local/bin"
bin="$dir/remote-hands"
tmp="$(mktemp "${TMPDIR:-/tmp}/remote-hands.XXXXXX")"
trap 'rm -f "$tmp" "$tmp.sums"' EXIT INT TERM

say ""
say "Remote Hands installer"
say "Downloading $asset ..."
curl -fSL --progress-bar "$BASE/$asset" -o "$tmp" || fail "Download failed. Check the internet connection and try again."
curl -fsSL "$BASE/SHA256SUMS" -o "$tmp.sums" || fail "Could not download the checksums. Please try again."
expected="$(awk -v f="$asset" '{ n = $2; sub(/^\*/, "", n); if (n == f) print $1 }' "$tmp.sums")"
if command -v sha256sum >/dev/null 2>&1; then
	actual="$(sha256sum "$tmp" | awk '{ print $1 }')"
else
	actual="$(shasum -a 256 "$tmp" | awk '{ print $1 }')"
fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "The download is damaged (checksum mismatch). Please try again."
rm -f "$tmp.sums"

mkdir -p "$dir"
chmod 755 "$tmp"
mv -f "$tmp" "$bin"
say "Installed: $bin"

args=""
case "${REMOTE_HANDS_LANG:-}" in
en | zh | cs) args="--lang $REMOTE_HANDS_LANG" ;;
esac

# On a Mac, a double-clickable starter on the Desktop for next time.
if [ "$os" = darwin ]; then
	starter="$HOME/Desktop/Remote Hands.command"
	if printf '#!/bin/sh\nexec "%s" %s\n' "$bin" "$args" >"$starter" 2>/dev/null && chmod +x "$starter" 2>/dev/null; then
		say "Next time, just double-click \"Remote Hands\" on your Desktop."
	fi
else
	say "Next time, run: $bin"
fi
say ""

[ -n "${REMOTE_HANDS_NO_START:-}" ] && exit 0
# When this script is piped into sh, its input is the script itself; give the
# program the keyboard instead.
if (exec </dev/tty) 2>/dev/null; then
	# shellcheck disable=SC2086 # $args is empty or two words on purpose
	exec "$bin" $args </dev/tty
fi
# shellcheck disable=SC2086
exec "$bin" $args
