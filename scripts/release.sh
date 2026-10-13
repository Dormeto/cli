#!/usr/bin/env bash
# Releases Dormeto/cli from the Mac: scripts/release.sh <version> [--dry-run]
#
#   1. the checks (main, clean, up to date with origin, no such tag, the CHANGELOG section,
#      go vet and go test, the shared cases against the manager's latest release, the Keychain item github-dormeto)
#   2. the annotated tag v<version>, pushed
#   3. GoReleaser: archives, checksums, GitHub release, Homebrew tap (GITHUB_TOKEN from the Keychain)
#   4. scripts/after-release.sh: the checks after
# npm is not a channel (the user, 2026-10-13): the 0.1.0 packages are deprecated, no other is published.
#
# --dry-run runs the checks and a GoReleaser snapshot, and publishes nothing: no tag, no push, no token read.
#
# Overridable for the tests: DORMETO_SECURITY, DORMETO_GORELEASER, DORMETO_AFTER, DORMETO_MANAGER_LATEST_URL,
# DORMETO_GITHUB_API.
set -euo pipefail
# Tracing (bash -x, SHELLOPTS=xtrace) would print the tokens: off, and kept off.
{ set +x; } 2>/dev/null

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
security="${DORMETO_SECURITY:-security}"
goreleaser="${DORMETO_GORELEASER:-goreleaser}"
after="${DORMETO_AFTER:-$root/scripts/after-release.sh}"
github_api="${DORMETO_GITHUB_API:-https://api.github.com}"
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

# Temporary files, removed on every exit (the GitHub token's header file). After the tag is pushed,
# a failure also prints what was done and the commands that finish the release.
scratch=""
tag_made=0
tag_pushed=0
goreleaser_done=0
cleanup() {
  local status=$?
  [ -z "$scratch" ] || rm -rf "$scratch"
  if [ "$status" -ne 0 ] && [ "$tag_made" = 1 ]; then report_state "$status" >&2; fi
  return "$status"
}
report_state() {
  echo
  echo "release: $tag is only partly released (exit $1). What was done:"
  if [ "$tag_pushed" = 1 ]; then echo "  - the tag $tag is pushed"; else echo "  - the tag $tag exists here only (not pushed)"; fi
  if [ "$goreleaser_done" = 1 ]; then echo "  - GoReleaser finished: the GitHub release, the archives, checksums.txt and the Homebrew cask are published"; else echo "  - GoReleaser did not finish: check https://github.com/Dormeto/cli/releases/tag/$tag and the tap"; fi
  echo "To finish (each step is safe to run once it is not done; the token comes from the Keychain, never typed):"
  if [ "$tag_pushed" = 0 ]; then echo "  git push origin $tag"; fi
  if [ "$goreleaser_done" = 0 ]; then
    echo "  GITHUB_TOKEN=\"\$(security find-generic-password -a \"\$USER\" -s github-dormeto -w)\" goreleaser release --clean --release-notes .git/release-notes-$version.md"
  fi
  echo "  scripts/after-release.sh $version"
}
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

"$security" find-generic-password -a "${USER:-$(id -un)}" -s github-dormeto >/dev/null 2>&1 \
  || fail "the Keychain item 'github-dormeto' does not exist (see the README: security add-generic-password -U -a \"\$USER\" -s github-dormeto -w)"

go mod tidy -diff || fail "go mod tidy would change go.mod or go.sum (GoReleaser's own hook refuses it): run go mod tidy and commit"
"$goreleaser" check || fail "goreleaser check fails"

echo "release: the checks pass"

# --- dry run: a snapshot, nothing published -------------------------------------------------------
if [ "$dry_run" = 1 ]; then
  "$goreleaser" release --snapshot --clean --release-notes "$notes"
  echo "release: dry run of $tag done: nothing tagged, pushed or published (dist/ holds the snapshot)"
  exit 0
fi

# --- the token, checked before anything is public ---------------------------------------------------
# Read once, held in a variable and in a mode-600 file of $scratch; never on a command line.
user="${USER:-$(id -un)}"
github_token="$("$security" find-generic-password -a "$user" -s github-dormeto -w)" || fail "cannot read the Keychain item github-dormeto"
[ -n "$github_token" ] || fail "the Keychain item github-dormeto is empty"

githubheader="$scratch/github-header"
( umask 077; : > "$githubheader" )
printf 'Authorization: Bearer %s\n' "$github_token" > "$githubheader"

# This proves the token valid, not its write grant: the repositories are public. The release itself proves the grant;
# a refusal there stops the release with the state report.
for repository in cli homebrew-tap; do
  curl -fsS --max-time 30 -H "@$githubheader" -o /dev/null "$github_api/repos/Dormeto/$repository" \
    || fail "the GitHub token (Keychain item github-dormeto) is refused reading Dormeto/$repository: expired or revoked"
done

saved_notes="$(git rev-parse --git-dir)/release-notes-$version.md" # outside dist/, which --clean empties
cp "$notes" "$saved_notes"

# --- 2. the tag ---------------------------------------------------------------------------------
tag_made=1
git tag -a "$tag" -m "dormeto $version"
git push origin "$tag"
tag_pushed=1

# --- 3. GoReleaser ------------------------------------------------------------------------------
GITHUB_TOKEN="$github_token" "$goreleaser" release --clean --release-notes "$saved_notes"
unset github_token
goreleaser_done=1

# --- 4. the checks after --------------------------------------------------------------------------
"$after" "$version"
echo "release: $tag released"
