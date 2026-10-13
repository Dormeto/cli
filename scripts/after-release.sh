#!/usr/bin/env bash
# The checks after a release: scripts/after-release.sh <version>. Run by release.sh; safe to run again.
# The release's assets and checksums; brew; npx; install.sh; go install. Each waits a little for the
# registries, then fails with what it saw.
set -euo pipefail

version="${1:?usage: scripts/after-release.sh <version>}"
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
if brew list --formula --cask 2>/dev/null | grep -qx dormeto; then brew upgrade dormeto/tap/dormeto; else brew install dormeto/tap/dormeto; fi
[ "$(dormeto version | head -n 1)" = "dormeto $version" ] || fail "brew gave '$(dormeto version | head -n 1)', not 'dormeto $version'"

echo "after-release: npx"
check_npx() { [ "$(cd "$scratch" && npx -y "@dormeto/cli@$version" version | head -n 1)" = "dormeto $version" ]; }
retry 10 check_npx || fail "npx @dormeto/cli@$version version did not give 'dormeto $version'"

echo "after-release: install.sh"
mkdir "$scratch/install"
curl -fsSL https://dormeto.com/install.sh | DORMETO_VERSION="$version" DORMETO_INSTALL_DIR="$scratch/install" sh
[ "$("$scratch/install/dormeto" version | head -n 1)" = "dormeto $version" ] || fail "install.sh did not install $version"

echo "after-release: go install"
mkdir "$scratch/gobin" "$scratch/gowork"
check_go() { ( cd "$scratch/gowork" && GOBIN="$scratch/gobin" go install "github.com/Dormeto/cli/cmd/dormeto@v$version" ); }
retry 10 check_go || fail "go install github.com/Dormeto/cli/cmd/dormeto@v$version failed"
[ "$("$scratch/gobin/dormeto" version | head -n 1)" = "dormeto $version" ] || fail "go install gave '$("$scratch/gobin/dormeto" version | head -n 1)'"

echo "after-release: all checks pass for $version"
