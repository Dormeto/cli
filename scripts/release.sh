#!/usr/bin/env bash
# Releases Dormeto/cli from the Mac: scripts/release.sh <version> [--dry-run]
#
#   1. the checks (main, clean, up to date with origin, no such tag, the CHANGELOG section,
#      go vet and go test, the shared cases against the manager's latest release, both Keychain items, npm 11.15.0 or
#      later)
#   2. the annotated tag v<version>, pushed
#   3. GoReleaser: archives, checksums, GitHub release, Homebrew tap (GITHUB_TOKEN from the Keychain)
#   4. npm: the seven packages assembled in dist/npm/ and staged (npm stage publish: the token is stage-only), the six
#      platform packages first, then @dormeto/cli (the token from the Keychain in a temporary user configuration,
#      removed on every exit); then the wait for the maintainer's approvals on npmjs.com (two-factor authentication)
#   5. scripts/after-release.sh: the checks after
#
# --dry-run runs the checks, a GoReleaser snapshot and the npm assembly, and publishes nothing:
# no tag, no push, no token read.
#
# Overridable for the tests: DORMETO_SECURITY, DORMETO_GORELEASER, DORMETO_NPM, DORMETO_AFTER,
# DORMETO_MANAGER_LATEST_URL, DORMETO_GITHUB_API; the wait for the approvals, DORMETO_APPROVAL_WAIT (seconds, 1800) and
# DORMETO_POLL (seconds between two looks at the registry, 15).
set -euo pipefail
# Tracing (bash -x, SHELLOPTS=xtrace) would print the tokens: off, and kept off.
{ set +x; } 2>/dev/null

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
security="${DORMETO_SECURITY:-security}"
goreleaser="${DORMETO_GORELEASER:-goreleaser}"
npm="${DORMETO_NPM:-npm}"
after="${DORMETO_AFTER:-$root/scripts/after-release.sh}"
github_api="${DORMETO_GITHUB_API:-https://api.github.com}"
latest_url="${DORMETO_MANAGER_LATEST_URL:-https://releases.dormeto.com/manager/latest.json}"
approval_wait="${DORMETO_APPROVAL_WAIT:-1800}"
poll="${DORMETO_POLL:-15}"
npm_floor=11.15.0 # the first npm with npm stage

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

# Temporary files, removed on every exit (the npm configuration holds the token). After the tag is pushed,
# a failure also prints what was done and the commands that finish the release.
scratch=""
tag_made=0
tag_pushed=0
goreleaser_done=0
staged=()
approved=0
packages=(cli-darwin-arm64 cli-darwin-x64 cli-linux-arm64 cli-linux-x64 cli-win32-arm64 cli-win32-x64 cli)
cleanup() {
  local status=$?
  [ -z "$scratch" ] || rm -rf "$scratch"
  if [ "$status" -ne 0 ] && [ "$tag_made" = 1 ]; then report_state "$status" >&2; fi
  return "$status"
}
report_state() {
  local package done_list=" ${staged[*]:-} "
  echo
  echo "release: $tag is only partly released (exit $1). What was done:"
  if [ "$tag_pushed" = 1 ]; then echo "  - the tag $tag is pushed"; else echo "  - the tag $tag exists here only (not pushed)"; fi
  if [ "$goreleaser_done" = 1 ]; then echo "  - GoReleaser finished: the GitHub release, the archives, checksums.txt and the Homebrew cask are published"; else echo "  - GoReleaser did not finish: check https://github.com/Dormeto/cli/releases/tag/$tag and the tap"; fi
  echo "  - npm packages staged: ${staged[*]:-none}"
  if [ "$approved" = 1 ]; then echo "  - npm packages approved: all ${#packages[@]}"; else echo "  - npm packages not approved yet"; fi
  echo "To finish (each step is safe to run once it is not done; tokens come from the Keychain, never typed):"
  if [ "$tag_pushed" = 0 ]; then echo "  git push origin $tag"; fi
  if [ "$goreleaser_done" = 0 ]; then
    echo "  GITHUB_TOKEN=\"\$(security find-generic-password -a \"\$USER\" -s github-dormeto -w)\" goreleaser release --clean --release-notes .git/release-notes-$version.md"
  fi
  local missing=0
  for package in "${packages[@]}"; do case "$done_list" in *" $package "*) ;; *) missing=1;; esac; done
  if [ "$missing" = 1 ]; then
    echo "  node npm/assemble.mjs   # after GoReleaser, if dist/npm is missing"
    echo "  npmrc=\$(mktemp) && printf '//registry.npmjs.org/:_authToken=%s\\n' \"\$(security find-generic-password -a \"\$USER\" -s npm-dormeto -w)\" > \"\$npmrc\""
    for package in "${packages[@]}"; do
      case "$done_list" in *" $package "*) ;; *) echo "  (cd dist/npm/$package && npm stage publish --access public --userconfig \"\$npmrc\")";; esac
    done
    echo "  rm -f \"\$npmrc\""
  fi
  if [ "$approved" = 0 ]; then
    echo "  approve each staged package on npmjs.com (Staged Packages, Approve, two-factor authentication), @dormeto/cli last:"
    for package in "${packages[@]}"; do echo "    https://www.npmjs.com/package/@dormeto/$package"; done
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

for item in npm-dormeto github-dormeto; do
  "$security" find-generic-password -a "${USER:-$(id -un)}" -s "$item" >/dev/null 2>&1 \
    || fail "the Keychain item '$item' does not exist (see the README: security add-generic-password -U -a \"\$USER\" -s $item -w)"
done

npm_version="$("$npm" --version)" || fail "cannot run npm"
too_old="$(node -e '
  const [a, b] = process.argv.slice(1).map((v) => v.split(".").map(Number));
  for (let i = 0; i < 3; i += 1) { if (a[i] !== b[i]) { process.stdout.write(a[i] < b[i] ? "yes" : "no"); process.exit(0); } }
  process.stdout.write("no");
' "$npm_version" "$npm_floor")"
[ "$too_old" = no ] || fail "npm $npm_version cannot stage packages (npm stage, the only way a stage-only token publishes, needs $npm_floor or later): npm install -g npm@11"

go mod tidy -diff || fail "go mod tidy would change go.mod or go.sum (GoReleaser's own hook refuses it): run go mod tidy and commit"
"$goreleaser" check || fail "goreleaser check fails"

echo "release: the checks pass"

# --- dry run: a snapshot and the npm assembly, nothing published ---------------------------------
if [ "$dry_run" = 1 ]; then
  "$goreleaser" release --snapshot --clean --release-notes "$notes"
  node npm/assemble.mjs
  echo "release: dry run of $tag done: nothing tagged, pushed or published (dist/ holds the snapshot and dist/npm/ the packages)"
  exit 0
fi

# --- tokens, checked before anything is public ------------------------------------------------------
# Read once, held in variables and in mode-600 files of $scratch; never on a command line.
user="${USER:-$(id -un)}"
github_token="$("$security" find-generic-password -a "$user" -s github-dormeto -w)" || fail "cannot read the Keychain item github-dormeto"
npm_token="$("$security" find-generic-password -a "$user" -s npm-dormeto -w)" || fail "cannot read the Keychain item npm-dormeto"
[ -n "$github_token" ] || fail "the Keychain item github-dormeto is empty"
[ -n "$npm_token" ] || fail "the Keychain item npm-dormeto is empty"

npmrc="$scratch/npmrc"
githubheader="$scratch/github-header"
( umask 077; : > "$npmrc"; : > "$githubheader" )
printf '//registry.npmjs.org/:_authToken=%s\n' "$npm_token" > "$npmrc"
printf 'Authorization: Bearer %s\n' "$github_token" > "$githubheader"
unset npm_token

# These prove the tokens valid, not their write grants: npm whoami answers any valid token, and the repositories are
# public. The publish itself proves the grants; a refusal there stops the release with the state report.
whoami="$("$npm" whoami --userconfig "$npmrc" 2>&1 >/dev/null)" || fail "the npm token (Keychain item npm-dormeto) is refused by npm whoami: expired or revoked (it lasts 90 days), or limited to Allowed IP ranges that exclude this address (npm takes IPv4 ranges only: a Mac reaching npm over IPv6 is always outside them). npm said:
$(printf '%s\n' "$whoami" | sed 's/^/  /')"
for repository in cli homebrew-tap; do
  curl -fsS --max-time 30 -H "@$githubheader" -o /dev/null "$github_api/repos/Dormeto/$repository" \
    || fail "the GitHub token (Keychain item github-dormeto) is refused reading Dormeto/$repository: expired or revoked"
done
taken="$("$npm" view "@dormeto/cli@$version" version --userconfig "$npmrc" 2>/dev/null || true)"
[ -z "$taken" ] || fail "@dormeto/cli@$version is already published on npm: npm versions cannot be reused"

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

# --- 4. npm: staged, then approved on npmjs.com -------------------------------------------------
node npm/assemble.mjs
for package in "${packages[@]}"; do
  ( cd "dist/npm/$package" && "$npm" stage publish --access public --userconfig "$npmrc" )
  staged+=("$package")
done
echo
echo "release: the ${#packages[@]} npm packages are staged. Approve each on npmjs.com (Staged Packages, Approve, with two-factor"
echo "authentication), @dormeto/cli last so that it never points at a platform package not yet published:"
for package in "${packages[@]}"; do echo "  https://www.npmjs.com/package/@dormeto/$package"; done
echo "release: waiting for the approvals (up to $((approval_wait / 60)) minutes; Ctrl-C prints what finishes the release)"
pending=("${packages[@]}")
deadline=$((SECONDS + approval_wait))
while :; do
  left=()
  for package in "${pending[@]}"; do
    # --prefer-online: npm's cache would still hold the package as it was before the approval.
    [ "$("$npm" view "@dormeto/$package@$version" version --prefer-online --userconfig "$npmrc" 2>/dev/null || true)" = "$version" ] \
      || left+=("$package")
  done
  if [ "${#left[@]}" -eq 0 ]; then break; fi
  if [ "${#left[@]}" -ne "${#pending[@]}" ]; then echo "release: $(( ${#packages[@]} - ${#left[@]} )) of ${#packages[@]} approved"; fi
  pending=("${left[@]}")
  [ "$SECONDS" -lt "$deadline" ] || fail "npm packages not approved after $((approval_wait / 60)) minutes: ${pending[*]}"
  sleep "$poll"
done
approved=1
echo "release: all ${#packages[@]} npm packages approved"
rm -f "$npmrc"

# --- 5. the checks after --------------------------------------------------------------------------
"$after" "$version"
echo "release: $tag released"
