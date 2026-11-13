#!/usr/bin/env bash
# Signs the release's macOS binaries with KLOUDY's Developer ID and has Apple notarize them. GoReleaser's build hook
# (.goreleaser.yaml) runs it on each darwin binary, before the archives and checksums.txt are made:
#
#   scripts/sign-macos.sh <binary>              sign, verify, notarize: fails unless Apple accepts it
#   scripts/sign-macos.sh --snapshot <binary>   a snapshot's: signs when the identity is in the Keychain, sends nothing
#                                               to Apple
#   scripts/sign-macos.sh --check               a release's two prerequisites, the identity and the notary profile
#                                               (release.sh's checks): fails naming what is missing and what to do
#   scripts/sign-macos.sh --verify <binary>     a published binary: its signature, KLOUDY's team and Apple's
#                                               notarization (after-release.sh)
#
# The identity is the certificate "Developer ID Application: KLOUDY (PP3PVUEBBT)" with its private key in the login
# Keychain; the notary service's credentials are the notarytool Keychain profile dormeto-notary. No secret is in a
# file, the environment or the output.
#
# A bare Mach-O executable cannot be stapled (stapler takes apps, disk images and installer packages): its ticket stays
# with Apple, and Gatekeeper reads it online at the first run of a downloaded binary. For the same reason, `spctl
# --assess --type execute` rejects any bare executable, notarized or not ("does not seem to be an app"): --verify asks
# codesign for an online notarization check instead.
#
# Overridable for the tests: DORMETO_SIGN_IDENTITY ("-" signs ad hoc, without the Keychain), DORMETO_SIGN_TEAM,
# DORMETO_NOTARY_PROFILE, DORMETO_CODESIGN, DORMETO_SECURITY, DORMETO_XCRUN, DORMETO_DITTO.
set -euo pipefail
{ set +x; } 2>/dev/null
# GoReleaser's environment holds the GitHub token: none of the tools below needs it.
unset GITHUB_TOKEN

team="${DORMETO_SIGN_TEAM:-PP3PVUEBBT}"
identity="${DORMETO_SIGN_IDENTITY:-Developer ID Application: KLOUDY ($team)}"
profile="${DORMETO_NOTARY_PROFILE:-dormeto-notary}"
identifier="com.dormeto.cli"
codesign="${DORMETO_CODESIGN:-codesign}"
security="${DORMETO_SECURITY:-security}"
xcrun="${DORMETO_XCRUN:-xcrun}"
ditto="${DORMETO_DITTO:-ditto}"

fail() { echo "sign-macos: $*" >&2; exit 1; }
usage() { echo "usage: scripts/sign-macos.sh [--snapshot | --verify] <binary> | --check" >&2; exit 2; }

scratch=""
trap '[ -z "$scratch" ] || rm -rf "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

# --- the prerequisites ----------------------------------------------------------------------------------------------

identity_missing="the signing identity \"$identity\" is not in the Keychain (security find-identity -v -p codesigning \
does not list it): install KLOUDY's Developer ID Application certificate with its private key in the login Keychain \
(in Xcode, signed in as the Account Holder of team $team: Settings, Accounts, Manage Certificates, +, Developer ID \
Application; or open the .p12 exported with its key from the Mac that has it), then run this again"

has_identity() {
  local listed
  [ "$identity" = - ] && return 0
  listed="$("$security" find-identity -v -p codesigning 2>/dev/null || true)"
  case "$listed" in *"\"$identity\""*) return 0 ;; esac
  return 1
}

# The profile answers when notarytool can read the submissions' history with it: the credentials are there and Apple
# takes them. Its history is not printed; its error, when it fails, is kept for the message, on one line.
notary_error=""
has_profile() {
  local errors
  if errors="$("$xcrun" notarytool history --keychain-profile "$profile" 2>&1 >/dev/null)"; then return 0; fi
  notary_error="$(printf '%s\n' "$errors" | sed -e '/^[[:space:]]*$/d' | tr '\n' ' ' | sed -e 's/ *$//')"
  return 1
}

profile_missing() {
  echo "the notary profile \"$profile\" does not answer (xcrun notarytool history --keychain-profile $profile: \
${notary_error:-no answer}): store the notary service's credentials in the Keychain with xcrun notarytool \
store-credentials $profile --apple-id <Apple ID> --team-id $team, which asks for an app-specific password \
(account.apple.com, Sign-In and Security, App-Specific Passwords), then run this again"
}

check() {
  local missing=0
  if has_identity; then echo "sign-macos: the signing identity \"$identity\" is in the Keychain"
  else echo "sign-macos: $identity_missing" >&2; missing=1; fi
  if has_profile; then echo "sign-macos: the notary profile \"$profile\" answers"
  else echo "sign-macos: $(profile_missing)" >&2; missing=1; fi
  return "$missing"
}

# --- the signature and the notarization -----------------------------------------------------------------------------

# json_field <key> <file>: a string field of notarytool's JSON answer, whose fields are all at its top level.
json_field() { sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$2" | head -n 1 || true; }

notarize() {
  local binary="$1" zip result id status
  scratch="$(mktemp -d "${TMPDIR:-/tmp}/dormeto-notarize.XXXXXX")"
  zip="$scratch/$(basename "$binary").zip"
  result="$scratch/result.json"
  "$ditto" -c -k "$binary" "$zip" || fail "ditto could not zip $binary"
  echo "sign-macos: $binary sent to Apple's notary service (profile $profile), waiting for its answer"
  "$xcrun" notarytool submit "$zip" --keychain-profile "$profile" --wait --timeout 30m --output-format json \
    > "$result" 2> "$scratch/errors" || true
  id="$(json_field id "$result")"
  status="$(json_field status "$result")"
  if [ -z "$id" ]; then
    cat "$result" "$scratch/errors" >&2
    fail "notarytool could not submit $binary (its answer above): check the profile with xcrun notarytool history --keychain-profile $profile"
  fi
  echo "sign-macos: $binary: submission $id, ${status:-no status}"
  if [ "$status" != Accepted ]; then
    cat "$scratch/errors" >&2
    fail "Apple did not accept $binary: submission $id is ${status:-without a status}. Apple's log: xcrun notarytool log $id --keychain-profile $profile"
  fi
  echo "sign-macos: $binary notarized by Apple (submission $id, Accepted)"
}

sign() {
  local binary="$1" snapshot="$2"
  [ -f "$binary" ] || fail "no binary at $binary"
  if ! has_identity; then
    if [ "$snapshot" = 1 ]; then
      echo "sign-macos: $binary not signed: \"$identity\" is not in the Keychain (a snapshot goes on without it)"
      return 0
    fi
    fail "$identity_missing"
  fi
  "$codesign" --force --options runtime --timestamp --identifier "$identifier" --sign "$identity" "$binary" \
    || fail "codesign could not sign $binary with \"$identity\" (its error above)"
  "$codesign" --verify --strict "$binary" || fail "the signature of $binary does not verify (codesign --verify --strict)"
  echo "sign-macos: $binary signed by \"$identity\""
  if [ "$snapshot" = 1 ]; then
    echo "sign-macos: $binary not sent to Apple: a snapshot is not notarized"
    return 0
  fi
  notarize "$binary"
}

# --- a published binary ---------------------------------------------------------------------------------------------

verify() {
  local binary="$1" details found
  [ -f "$binary" ] || fail "no binary at $binary"
  "$codesign" --verify --strict "$binary" || fail "the signature of $binary does not verify (codesign --verify --strict)"
  details="$("$codesign" -dv "$binary" 2>&1)" || fail "codesign cannot read the signature of $binary"
  found="$(printf '%s\n' "$details" | sed -n 's/^TeamIdentifier=//p')"
  [ "$found" = "$team" ] || fail "$binary is signed by team '${found:-none}', not $team (KLOUDY)"
  "$codesign" --verify --strict --check-notarization -R="notarized" "$binary" \
    || fail "$binary is not notarized by Apple (codesign --verify --strict --check-notarization -R=notarized, an online check)"
  echo "sign-macos: $binary is signed by team $team (KLOUDY) and notarized by Apple"
}

mode=sign
snapshot=0
binary=""
for arg in "$@"; do
  case "$arg" in
    --snapshot) snapshot=1 ;;
    --check | --verify) [ "$mode" = sign ] || usage; mode="${arg#--}" ;;
    -*) usage ;;
    *) [ -z "$binary" ] || usage; binary="$arg" ;;
  esac
done
case "$mode" in
  check) { [ -z "$binary" ] && [ "$snapshot" = 0 ]; } || usage; check ;;
  verify) { [ -n "$binary" ] && [ "$snapshot" = 0 ]; } || usage; verify "$binary" ;;
  sign) [ -n "$binary" ] || usage; sign "$binary" "$snapshot" ;;
esac
