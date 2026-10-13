#!/usr/bin/env bash
# Releases Dormeto/cli from the Mac: scripts/release.sh <version> [--dry-run]
#
#   1. the checks (main, clean, up to date with origin, no such tag, the CHANGELOG section,
#      go vet and go test, the shared cases against the manager's latest release, both Keychain items)
#   2. the annotated tag v<version>, pushed
#   3. GoReleaser: archives, checksums, GitHub release, Homebrew tap (GITHUB_TOKEN from the Keychain)
#   4. npm: the seven packages assembled in dist/npm/, the six platform packages first, then @dormeto/cli
#      (the token from the Keychain in a temporary user configuration, removed on every exit)
#   5. scripts/after-release.sh: the checks after
#
# --dry-run runs the checks, a GoReleaser snapshot and the npm assembly, and publishes nothing:
# no tag, no push, no token read.
#
# Overridable for the tests: DORMETO_SECURITY, DORMETO_GORELEASER, DORMETO_NPM, DORMETO_AFTER,
# DORMETO_MANAGER_LATEST_URL.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
security="${DORMETO_SECURITY:-security}"
goreleaser="${DORMETO_GORELEASER:-goreleaser}"
npm="${DORMETO_NPM:-npm}"
after="${DORMETO_AFTER:-$root/scripts/after-release.sh}"
latest_url="${DORMETO_MANAGER_LATEST_URL:-https://releases.dormeto.com/manager/latest.json}"

fail() { echo "release: $*" >&2; exit 1; }
usage() { echo "usage: scripts/release.sh <version> [--dry-run]" >&2; exit 2; }

version=""
dry_run=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=1 ;;
    -*) usage ;;
    *) [ -z "$version" ] || usage; version="$arg" ;;
  esac
done
[ -n "$version" ] || usage
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "the version '$version' must be x.y.z, without a leading v"
tag="v$version"

# Temporary files, removed on every exit (the npm configuration holds the token).
scratch=""
cleanup() { [ -z "$scratch" ] || rm -rf "$scratch"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
scratch="$(mktemp -d "${TMPDIR:-/tmp}/dormeto-release.XXXXXX")"

cd "$root"

# --- 1. the checks ---------------------------------------------------------------------------
echo "release: checking $tag"

branch="$(git rev-parse --abbrev-ref HEAD)"
[ "$branch" = main ] || fail "on branch '$branch': a release is made from main"

[ -z "$(git status --porcelain)" ] || fail "the working tree is not clean (uncommitted or untracked files): commit or remove them"

git fetch --quiet origin main || fail "cannot fetch origin"
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || fail "main is not up to date with origin/main: pull or push first"

if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null || [ -n "$(git ls-remote --tags origin "refs/tags/$tag")" ]; then
  fail "the tag $tag already exists"
fi

notes="$scratch/notes.md"
awk -v header="## $version" '
  $0 == header || index($0, header " ") == 1 { found = 1; next }
  found && /^## / { exit }
  found { print }
' CHANGELOG.md | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}' > "$notes"
[ -s "$notes" ] || fail "CHANGELOG.md has no '## $version' section with notes"

go vet ./... || fail "go vet fails"
go test ./... || fail "go test fails"

shared="$(node -e '
  const fs = require("fs");
  process.stdout.write(JSON.parse(fs.readFileSync("testdata/parity/versions.json", "utf8")).manager);
')" || fail "cannot read the manager version of testdata/parity/versions.json"
latest="$(curl -fsS --max-time 30 "$latest_url" | node -e '
  let s = ""; process.stdin.on("data", (d) => { s += d; }).on("end", () => process.stdout.write(String(JSON.parse(s).version)));
')" || fail "cannot read the manager's latest release from $latest_url"
[ -n "$latest" ] || fail "no version in $latest_url"
older="$(node -e '
  const [a, b] = process.argv.slice(1).map((v) => v.split(".").map(Number));
  for (let i = 0; i < 3; i += 1) { if (a[i] !== b[i]) { process.stdout.write(a[i] < b[i] ? "yes" : "no"); process.exit(0); } }
  process.stdout.write("no");
' "$shared" "$latest")"
[ "$older" = no ] || fail "the shared test cases name manager $shared, older than the latest release $latest ($latest_url): export the cases again from the manager"

for item in npm-dormeto github-dormeto; do
  "$security" find-generic-password -a "${USER:-$(id -un)}" -s "$item" >/dev/null 2>&1 \
    || fail "the Keychain item '$item' does not exist (see the README: security add-generic-password -U -a \"\$USER\" -s $item -w)"
done

echo "release: the checks pass"

# --- dry run: a snapshot and the npm assembly, nothing published ---------------------------------
if [ "$dry_run" = 1 ]; then
  "$goreleaser" release --snapshot --clean --release-notes "$notes"
  node npm/assemble.mjs
  echo "release: dry run of $tag done: nothing tagged, pushed or published (dist/ holds the snapshot and dist/npm/ the packages)"
  exit 0
fi

# --- 2. the tag ---------------------------------------------------------------------------------
git tag -a "$tag" -m "dormeto $version"
git push origin "$tag"

# --- 3. GoReleaser ------------------------------------------------------------------------------
github_token="$("$security" find-generic-password -a "${USER:-$(id -un)}" -s github-dormeto -w)"
GITHUB_TOKEN="$github_token" "$goreleaser" release --clean --release-notes "$notes"
unset github_token

# --- 4. npm -------------------------------------------------------------------------------------
node npm/assemble.mjs
npmrc="$scratch/npmrc"
( umask 077; : > "$npmrc" )
chmod 600 "$npmrc"
npm_token="$("$security" find-generic-password -a "${USER:-$(id -un)}" -s npm-dormeto -w)"
printf '//registry.npmjs.org/:_authToken=%s\n' "$npm_token" > "$npmrc"
unset npm_token
for package in cli-darwin-arm64 cli-darwin-x64 cli-linux-arm64 cli-linux-x64 cli-win32-arm64 cli-win32-x64 cli; do
  ( cd "dist/npm/$package" && "$npm" publish --access public --userconfig "$npmrc" )
done
rm -f "$npmrc"

# --- 5. the checks after --------------------------------------------------------------------------
"$after" "$version"
echo "release: $tag released"
