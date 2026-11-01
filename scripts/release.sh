#!/usr/bin/env bash
# Releases Dormeto/cli from the Mac: scripts/release.sh <version> [--dry-run]
#
#   1. the checks (main, clean, not behind origin, no such tag, the CHANGELOG's notes,
#      go vet and go test, the shared cases against the manager's latest release, the Keychain item github-dormeto)
#   2. a leading "## Unreleased" with notes becomes "## <version>": the release commit; main pushed, with any local commits
#   3. the annotated tag v<version>, pushed
#   4. GoReleaser: archives, checksums, GitHub release, Homebrew tap (GITHUB_TOKEN from the Keychain)
#   5. the next iteration: "## Unreleased" back at the top of the CHANGELOG, committed and pushed
#   6. scripts/after-release.sh: the checks after
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

# Temporary files, removed on every exit (the GitHub token's header file). After the release commit,
# a failure also prints what was done and the commands that finish the release.
scratch=""
commit_made=0
tag_made=0
tag_pushed=0
goreleaser_done=0
next_committed=0
next_done=0
cleanup() {
  local status=$?
  [ -z "$scratch" ] || rm -rf "$scratch"
  if [ "$status" -ne 0 ]; then
    if [ "$tag_made" = 1 ]; then report_state "$status" >&2
    elif [ "$commit_made" = 1 ]; then report_commit "$status" >&2; fi
  fi
  return "$status"
}
report_commit() {
  echo
  echo "release: nothing is published, but the release commit of $version is only here (exit $1)."
  echo "To go on, once origin takes the push: this release again. To abandon it: git reset --keep HEAD~1"
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
  if [ "$next_committed" = 1 ]; then
    if [ "$next_done" = 0 ]; then echo "  git push origin main"; fi
  elif [ "$next_done" = 0 ]; then
    echo "  the next iteration: \"## Unreleased\" back at the top of CHANGELOG.md, committed and pushed"
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
git merge-base --is-ancestor origin/main HEAD || fail "main is behind origin/main or diverged from it: pull first"

if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null || [ -n "$(git ls-remote --tags origin "refs/tags/$tag")" ]; then
  fail "the tag $tag already exists"
fi

# section <title>: the notes under "## <title>" of the CHANGELOG, without the blank lines around them.
section() {
  awk -v header="## $1" '
    $0 == header || index($0, header " ") == 1 { found = 1; next }
    found && /^## / { exit }
    found { print }
  ' CHANGELOG.md | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}'
}
leading_unreleased() { [ "$(grep -m 1 '^## ' CHANGELOG.md || true)" = "## Unreleased" ]; }

# A leading "## Unreleased" with notes is what this release publishes, as "## <version>" once the checks have passed.
notes="$scratch/notes.md"
section "$version" > "$notes"
name_section=0
if leading_unreleased; then
  section Unreleased > "$scratch/unreleased.md"
  if [ -s "$scratch/unreleased.md" ]; then
    if [ -s "$notes" ] || grep -q -e "^## $version\$" -e "^## $version " CHANGELOG.md; then
      fail "CHANGELOG.md has both notes under '## Unreleased' and a '## $version' section: keep one"
    fi
    cp "$scratch/unreleased.md" "$notes"
    name_section=1
  fi
fi
[ -s "$notes" ] || fail "CHANGELOG.md has no notes for $version: neither under a leading '## Unreleased' nor under '## $version'"

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
  if [ "$name_section" = 1 ]; then echo "release: CHANGELOG.md's ## Unreleased would become ## $version"; fi
  local_commits="$(git rev-list --count origin/main..HEAD)"
  if [ "$local_commits" = 1 ]; then echo "release: 1 local commit would be pushed"
  elif [ "$local_commits" != 0 ]; then echo "release: $local_commits local commits would be pushed"; fi
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

# --- 2. the release commit, and main pushed ------------------------------------------------------
if [ "$name_section" = 1 ]; then
  if ! { awk -v version="$version" 'done != 1 && $0 == "## Unreleased" { print "## " version; done = 1; next } { print }' CHANGELOG.md > "$scratch/changelog.md" \
    && cp "$scratch/changelog.md" CHANGELOG.md \
    && git add CHANGELOG.md \
    && git commit -q -m "chore(release): cli $version"; }; then
    git reset -q HEAD -- CHANGELOG.md
    git checkout HEAD -- CHANGELOG.md
    fail "the release commit failed: CHANGELOG.md is restored"
  fi
  commit_made=1
fi
# The tree is clean and every check passed on this commit: all of main goes, local commits included.
if [ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]; then
  git push origin main || fail "cannot push main to origin: nothing is published"
fi

# --- 3. the tag ---------------------------------------------------------------------------------
tag_made=1
git tag -a "$tag" -m "dormeto $version"
git push origin "$tag"
tag_pushed=1

# --- 4. GoReleaser ------------------------------------------------------------------------------
GITHUB_TOKEN="$github_token" "$goreleaser" release --clean --release-notes "$saved_notes"
unset github_token
goreleaser_done=1

# --- 5. the next iteration --------------------------------------------------------------------------
# The version comes from the tag: all there is to prepare is the CHANGELOG's next section.
if leading_unreleased; then
  next_done=1
else
  awk 'done != 1 && /^## / { print "## Unreleased"; print ""; done = 1 } { print }' CHANGELOG.md > "$scratch/changelog.md"
  cp "$scratch/changelog.md" CHANGELOG.md
  git add CHANGELOG.md
  git commit -q -m "chore: next iteration after cli $version"
  next_committed=1
  git push origin main
  next_done=1
fi

# --- 6. the checks after --------------------------------------------------------------------------
"$after" "$version"
echo "release: $tag released"
