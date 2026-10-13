#!/usr/bin/env bash
# The checks after a release: scripts/after-release.sh <version>. Run by release.sh; safe to run again.
# The release's assets and checksums; brew; npx; install.sh; go install. Each waits a little for the
# registries, then fails with what it saw.
#
# install.sh: https://dormeto.com/install.sh, or while it does not answer 200 (the website not deployed yet, Ruling
# P17) the website checkout's site/install.sh, DORMETO_WEBSITE_DIR (default: ../website beside this repository).
set -euo pipefail

version="${1:?usage: scripts/after-release.sh <version>}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
website="${DORMETO_WEBSITE_DIR:-$root/../website}"
download="https://github.com/Dormeto/cli/releases/download/v$version"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/dormeto-after.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() { echo "after-release: $*" >&2; exit 1; }

# retry <attempts> <command...>: the registries and the proxy lag a few minutes behind a publish.
retry() {
  local attempts="$1" n=1
  shift
  until "$@"; do
    [ "$n" -lt "$attempts" ] || return 1
    n=$((n + 1))
    sleep 15
  done
}

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

echo "after-release: assets and checksums"
curl -fsSL -o "$scratch/checksums.txt" "$download/checksums.txt" || fail "no checksums.txt in the release v$version"
[ "$(wc -l < "$scratch/checksums.txt" | tr -d ' ')" = 6 ] || fail "checksums.txt does not list 6 archives"
while read -r sum file; do
  curl -fsSL -o "$scratch/$file" "$download/$file" || fail "no asset $file in the release"
  [ "$(sha256 "$scratch/$file")" = "$sum" ] || fail "the checksum of $file differs from checksums.txt"
done < "$scratch/checksums.txt"

echo "after-release: brew"
brew update --quiet
# A cask (Homebrew refuses --formula with --cask): upgraded when installed, `brew install` would leave it as it is.
if brew list --cask dormeto >/dev/null 2>&1; then brew upgrade --cask dormeto/tap/dormeto; else brew install --cask dormeto/tap/dormeto; fi
# Homebrew's own dormeto, linked in its prefix's bin/, not whichever is first on PATH (a go install build).
brewed="$(brew --prefix)/bin/dormeto"
[ "$("$brewed" version | head -n 1)" = "dormeto $version" ] || fail "brew gave '$("$brewed" version | head -n 1)' ($brewed), not 'dormeto $version'"

echo "after-release: npx"
check_npx() { [ "$(cd "$scratch" && npx -y "@dormeto/cli@$version" version | head -n 1)" = "dormeto $version" ]; }
retry 10 check_npx || fail "npx @dormeto/cli@$version version did not give 'dormeto $version'"

echo "after-release: install.sh"
mkdir "$scratch/install"
served="$(curl -sSL -o "$scratch/install.sh" -w '%{http_code}' https://dormeto.com/install.sh || true)"
if [ "$served" = 200 ]; then
  installer="$scratch/install.sh"
  echo "after-release: install.sh from https://dormeto.com/install.sh"
else
  installer="$website/site/install.sh"
  [ -f "$installer" ] || fail "https://dormeto.com/install.sh does not answer 200 (${served:-no answer}) and there is no website checkout's copy at $installer (set DORMETO_WEBSITE_DIR)"
  echo "after-release: https://dormeto.com/install.sh answers ${served:-nothing}, not 200 (the website not deployed yet): install.sh from the website checkout, $installer"
fi
DORMETO_VERSION="$version" DORMETO_INSTALL_DIR="$scratch/install" sh "$installer" || fail "install.sh ($installer) failed"
[ "$("$scratch/install/dormeto" version | head -n 1)" = "dormeto $version" ] || fail "install.sh ($installer) did not install $version"

echo "after-release: go install"
mkdir "$scratch/gobin" "$scratch/gowork"
check_go() { ( cd "$scratch/gowork" && GOBIN="$scratch/gobin" go install "github.com/Dormeto/cli/cmd/dormeto@v$version" ); }
retry 10 check_go || fail "go install github.com/Dormeto/cli/cmd/dormeto@v$version failed"
[ "$("$scratch/gobin/dormeto" version | head -n 1)" = "dormeto $version" ] || fail "go install gave '$("$scratch/gobin/dormeto" version | head -n 1)'"

echo "after-release: all checks pass for $version"
