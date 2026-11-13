import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = dirname(dirname(fileURLToPath(import.meta.url)));
const script = join(repo, 'scripts', 'sign-macos.sh');
const IDENTITY = 'Developer ID Application: KLOUDY (PP3PVUEBBT)';
const TEAM = 'PP3PVUEBBT';
const PROFILE = 'dormeto-notary';
const ID = '2efe2717-52ef-43a5-96dc-0797e4ca1041';

function write(file, text, mode) {
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text);
  if (mode) chmodSync(file, mode);
}

// Stubs for codesign, security, xcrun (notarytool) and ditto, each logging its call, and a darwin binary in dist/.
// codesign: SIGN_FAIL, VERIFY_FAIL, NOT_NOTARIZED (the online notarization check fails), TEAM_FOUND (what -dv says).
// security: NO_IDENTITY (find-identity lists none). xcrun: NO_PROFILE (the profile is not in the Keychain),
// NOTARY_STATUS (Accepted by default), NOTARY_FAIL (notarytool fails before any answer), NOTARY_EXIT.
function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'dormeto-sign-'));
  const bin = join(root, 'bin');
  const log = join(root, 'calls.log');
  const binary = join(root, 'dist', 'dormeto_darwin_arm64_v8.0', 'dormeto');
  write(binary, 'mach-o\n', 0o755);
  write(join(bin, 'codesign'), `#!/bin/sh
set +eu
echo "codesign $* GITHUB_TOKEN=\${GITHUB_TOKEN:-}" >> "${log}"
case " $* " in
  *" --sign "*) [ -n "$SIGN_FAIL" ] && { echo "codesign: simulated failure" >&2; exit 1; }; exit 0;;
  *" -dv "*) printf 'Executable=%s\\nIdentifier=com.dormeto.cli\\nTeamIdentifier=%s\\n' "$2" "\${TEAM_FOUND:-${TEAM}}" >&2; exit 0;;
  *" --check-notarization "*) [ -n "$NOT_NOTARIZED" ] && { echo "test-requirement: code failed to satisfy specified code requirement(s)" >&2; exit 3; }; exit 0;;
  *" --verify "*) [ -n "$VERIFY_FAIL" ] && { echo "$3: invalid signature (code or signature have been modified)" >&2; exit 1; }; exit 0;;
esac
echo "codesign: unexpected $*" >&2; exit 2
`, 0o755);
  write(join(bin, 'security'), `#!/bin/sh
set +eu
echo "security $*" >> "${log}"
[ "$1" = find-identity ] || { echo "security: unexpected $*" >&2; exit 2; }
[ -z "$NO_IDENTITY" ] && echo '  1) 0123456789ABCDEF0123456789ABCDEF01234567 "${IDENTITY}"'
[ -z "$NO_IDENTITY" ] && echo '     1 valid identities found' || echo '     0 valid identities found'
exit 0
`, 0o755);
  write(join(bin, 'xcrun'), `#!/bin/sh
set +eu
echo "xcrun $* GITHUB_TOKEN=\${GITHUB_TOKEN:-}" >> "${log}"
[ "$1" = notarytool ] || { echo "xcrun: unexpected $*" >&2; exit 2; }
[ -n "$NO_PROFILE" ] && { printf 'Error: No Keychain password item found for profile: ${PROFILE}\\n\\nRun %s to create another credential profile.\\n' "'notarytool store-credentials'" >&2; exit 69; }
case "$2" in
  history) echo "Successfully received submission history."; echo "  createdDate: 2026-11-13T09:00:00.000Z"; exit 0;;
  submit)
    [ -f "$3" ] || { echo "Error: $3 does not exist" >&2; exit 1; }
    cp "$3" "${root}/submitted.zip"
    [ -n "$NOTARY_FAIL" ] && { echo "Error: HTTP status code: 401. Unable to authenticate." >&2; exit 1; }
    printf '{"id":"${ID}","message":"Processing complete","status":"%s"}\\n' "\${NOTARY_STATUS:-Accepted}"
    exit "\${NOTARY_EXIT:-0}";;
esac
echo "xcrun: unexpected $*" >&2; exit 2
`, 0o755);
  // ditto -c -k <file> <zip>: the zip holds the file (here a copy, enough for the submit stub).
  write(join(bin, 'ditto'), `#!/bin/sh
set +eu
echo "ditto $*" >> "${log}"
cp "$3" "$4"
`, 0o755);
  const env = {
    ...process.env,
    DORMETO_CODESIGN: join(bin, 'codesign'),
    DORMETO_SECURITY: join(bin, 'security'),
    DORMETO_XCRUN: join(bin, 'xcrun'),
    DORMETO_DITTO: join(bin, 'ditto'),
    TMPDIR: join(root, 'tmp'),
  };
  for (const name of ['DORMETO_SIGN_IDENTITY', 'DORMETO_SIGN_TEAM', 'DORMETO_NOTARY_PROFILE', 'GITHUB_TOKEN']) delete env[name];
  mkdirSync(env.TMPDIR);
  const run = (args, extra = {}) => spawnSync('bash', [script, ...args], { cwd: root, encoding: 'utf8', env: { ...env, ...extra } });
  return { root, binary, log, run, calls: () => (existsSync(log) ? readFileSync(log, 'utf8') : ''), cleanup: () => rmSync(root, { recursive: true, force: true }) };
}

const lines = (calls, prefix) => calls.split('\n').filter((l) => l.startsWith(prefix));

// --- a release: signed, verified, notarized -------------------------------------------------------------------

test('sign-macos.sh signs with KLOUDY\'s Developer ID (hardened runtime, timestamp), verifies, and has Apple notarize the zipped binary', () => {
  const f = fixture();
  try {
    const out = f.run([f.binary], { GITHUB_TOKEN: 'token-github-dormeto' });
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.deepEqual(lines(calls, 'codesign ').map((l) => l.replace(/ GITHUB_TOKEN=.*$/, '')), [
      `codesign --force --options runtime --timestamp --identifier com.dormeto.cli --sign ${IDENTITY} ${f.binary}`,
      `codesign --verify --strict ${f.binary}`,
    ]);
    const [ditto] = lines(calls, 'ditto ');
    assert.match(ditto, new RegExp(`^ditto -c -k ${f.binary.replaceAll('.', '\\.')} \\S+/dormeto\\.zip$`));
    const [submit] = lines(calls, 'xcrun ');
    const zip = ditto.split(' ').at(-1);
    assert.ok(submit.startsWith(`xcrun notarytool submit ${zip} --keychain-profile ${PROFILE} --wait `), submit);
    assert.equal(readFileSync(join(f.root, 'submitted.zip'), 'utf8'), 'mach-o\n', 'the zip holds the signed binary');
    assert.ok(calls.indexOf('codesign --verify') < calls.indexOf('xcrun notarytool submit'), 'verified before it is sent');
    assert.match(out.stdout, new RegExp(`submission ${ID}`));
    assert.match(out.stdout, /Accepted/);
    assert.doesNotMatch(calls, /GITHUB_TOKEN=token/, 'the GitHub token stays out of codesign and notarytool');
    assert.equal(readdirSync(join(f.root, 'tmp')).length, 0, 'the zip and its directory are removed');
  } finally { f.cleanup(); }
});

test('sign-macos.sh fails unless Apple accepts the binary, with the submission id and the command that reads Apple\'s log', () => {
  for (const extra of [{ NOTARY_STATUS: 'Invalid' }, { NOTARY_STATUS: 'Invalid', NOTARY_EXIT: '1' }, { NOTARY_STATUS: 'In Progress', NOTARY_EXIT: '1' }]) {
    const f = fixture();
    try {
      const out = f.run([f.binary], extra);
      assert.notEqual(out.status, 0, out.stdout + out.stderr);
      assert.ok(out.stderr.includes(extra.NOTARY_STATUS), out.stderr);
      assert.ok(out.stderr.includes(`xcrun notarytool log ${ID} --keychain-profile ${PROFILE}`), out.stderr);
      assert.equal(readdirSync(join(f.root, 'tmp')).length, 0, 'temporary files removed');
    } finally { f.cleanup(); }
  }
});

test('sign-macos.sh fails when notarytool fails before any answer, with notarytool\'s error and the profile to check', () => {
  const f = fixture();
  try {
    const out = f.run([f.binary], { NOTARY_FAIL: '1' });
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /HTTP status code: 401/);
    assert.match(out.stderr, /dormeto-notary/);
  } finally { f.cleanup(); }
});

test('sign-macos.sh refuses a release without the identity in the Keychain, before codesign, saying how to install it', () => {
  const f = fixture();
  try {
    const out = f.run([f.binary], { NO_IDENTITY: '1' });
    assert.notEqual(out.status, 0);
    assert.ok(out.stderr.includes(`"${IDENTITY}" is not in the Keychain`), out.stderr);
    assert.match(out.stderr, /security find-identity -v -p codesigning/);
    assert.match(out.stderr, /Developer ID Application/);
    assert.doesNotMatch(f.calls(), /^codesign /m);
    assert.doesNotMatch(f.calls(), /^xcrun /m);
  } finally { f.cleanup(); }
});

test('sign-macos.sh sends nothing to Apple when the signature does not verify, or when codesign fails', () => {
  for (const extra of [{ VERIFY_FAIL: '1' }, { SIGN_FAIL: '1' }]) {
    const f = fixture();
    try {
      const out = f.run([f.binary], extra);
      assert.notEqual(out.status, 0, out.stdout + out.stderr);
      assert.match(out.stderr, /sign-macos: /);
      assert.doesNotMatch(f.calls(), /^xcrun /m);
    } finally { f.cleanup(); }
  }
});

test('sign-macos.sh refuses a missing binary and a bad usage', () => {
  const f = fixture();
  try {
    assert.notEqual(f.run([join(f.root, 'nothing')]).status, 0);
    assert.equal(f.run([]).status, 2);
    assert.equal(f.run(['--nope', f.binary]).status, 2);
    assert.equal(f.run([f.binary, f.binary]).status, 2);
    assert.doesNotMatch(f.calls(), /^(codesign|xcrun) /m);
  } finally { f.cleanup(); }
});

// --- a snapshot: signed when it can be, never sent to Apple --------------------------------------------------------

test('sign-macos.sh --snapshot signs with the identity there, and sends nothing to Apple', () => {
  const f = fixture();
  try {
    const out = f.run(['--snapshot', f.binary]);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(f.calls(), /^codesign --force --options runtime --timestamp --identifier com\.dormeto\.cli --sign Developer ID Application: KLOUDY \(PP3PVUEBBT\) /m);
    assert.match(f.calls(), /^codesign --verify --strict /m);
    assert.doesNotMatch(f.calls(), /^(xcrun|ditto) /m);
    assert.match(out.stdout, /signed/);
    assert.match(out.stdout, /not sent to Apple/);
  } finally { f.cleanup(); }
});

test('sign-macos.sh --snapshot without the identity leaves the binary unsigned, says so, and passes', () => {
  const f = fixture();
  try {
    const out = f.run(['--snapshot', f.binary], { NO_IDENTITY: '1' });
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(out.stdout, /not signed/);
    assert.ok(out.stdout.includes(IDENTITY), out.stdout);
    assert.doesNotMatch(f.calls(), /^(codesign|xcrun|ditto) /m);
  } finally { f.cleanup(); }
});

test('DORMETO_SIGN_IDENTITY=- signs ad hoc, with no Keychain to look in (a snapshot\'s proof of the hook)', () => {
  const f = fixture();
  try {
    const out = f.run(['--snapshot', f.binary], { DORMETO_SIGN_IDENTITY: '-', NO_IDENTITY: '1' });
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(f.calls(), /^codesign --force --options runtime --timestamp --identifier com\.dormeto\.cli --sign - /m);
    assert.doesNotMatch(f.calls(), /^security /m);
  } finally { f.cleanup(); }
});

// --- the release's checks: the identity and the notary profile -----------------------------------------------------

test('sign-macos.sh --check passes with the identity and the notary profile, reading only the notary history', () => {
  const f = fixture();
  try {
    const out = f.run(['--check']);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(f.calls(), /^security find-identity -v -p codesigning$/m);
    assert.match(f.calls(), new RegExp(`^xcrun notarytool history --keychain-profile ${PROFILE} `, 'm'));
    assert.doesNotMatch(f.calls(), /submit|store-credentials|--password|--apple-id/);
    assert.ok(out.stdout.includes(IDENTITY), out.stdout);
    assert.ok(out.stdout.includes(PROFILE), out.stdout);
    assert.doesNotMatch(out.stdout + out.stderr, /createdDate|Successfully received/, 'the history is not printed');
  } finally { f.cleanup(); }
});

test('sign-macos.sh --check names each missing prerequisite with what to do, and fails', () => {
  const f = fixture();
  try {
    const identity = f.run(['--check'], { NO_IDENTITY: '1' });
    assert.notEqual(identity.status, 0);
    assert.ok(identity.stderr.includes(`"${IDENTITY}" is not in the Keychain`), identity.stderr);
    assert.doesNotMatch(identity.stderr, /store-credentials/);

    const profile = f.run(['--check'], { NO_PROFILE: '1' });
    assert.notEqual(profile.status, 0);
    assert.ok(profile.stderr.includes(`xcrun notarytool store-credentials ${PROFILE} --apple-id <Apple ID> --team-id ${TEAM}`), profile.stderr);
    assert.match(profile.stderr, /No Keychain password item found/, 'notarytool\'s own error');
    assert.match(profile.stderr, /app-specific password/);
    assert.doesNotMatch(profile.stderr, /is not in the Keychain \(security/);

    const both = f.run(['--check'], { NO_IDENTITY: '1', NO_PROFILE: '1' });
    assert.notEqual(both.status, 0);
    assert.match(both.stderr, /is not in the Keychain/);
    assert.match(both.stderr, /store-credentials/);
  } finally { f.cleanup(); }
});

// --- the checks after a release ------------------------------------------------------------------------------------

test('sign-macos.sh --verify checks the signature, KLOUDY\'s team and Apple\'s notarization (online: a bare binary has no staple)', () => {
  const f = fixture();
  try {
    const out = f.run(['--verify', f.binary]);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.deepEqual(lines(f.calls(), 'codesign ').map((l) => l.replace(/ GITHUB_TOKEN=.*$/, '')), [
      `codesign --verify --strict ${f.binary}`,
      `codesign -dv ${f.binary}`,
      `codesign --verify --strict --check-notarization -R=notarized ${f.binary}`,
    ]);
    assert.doesNotMatch(f.calls(), /^(security|xcrun) /m);
    assert.match(out.stdout, /PP3PVUEBBT/);
  } finally { f.cleanup(); }
});

test('sign-macos.sh --verify fails on a broken signature, another team or no notarization, naming the binary', () => {
  const cases = [
    [{ VERIFY_FAIL: '1' }, /signature/],
    [{ TEAM_FOUND: 'not set' }, /team 'not set', not PP3PVUEBBT/],
    [{ TEAM_FOUND: 'ABCDE12345' }, /team 'ABCDE12345', not PP3PVUEBBT/],
    [{ NOT_NOTARIZED: '1' }, /notariz/],
  ];
  for (const [extra, pattern] of cases) {
    const f = fixture();
    try {
      const out = f.run(['--verify', f.binary], extra);
      assert.notEqual(out.status, 0, JSON.stringify(extra));
      assert.match(out.stderr, pattern);
      assert.ok(out.stderr.includes(f.binary), out.stderr);
    } finally { f.cleanup(); }
  }
});

// --- where it is wired, and what it never holds -------------------------------------------------------------------

test('GoReleaser signs the darwin binaries after each build, before the archives: --snapshot on a snapshot, nothing for the others', () => {
  const yaml = readFileSync(join(repo, '.goreleaser.yaml'), 'utf8');
  const builds = yaml.slice(yaml.indexOf('\nbuilds:'), yaml.indexOf('\narchives:'));
  assert.ok(builds.includes(`    hooks:
      post:
        - cmd: '{{ if eq .Os "darwin" }}scripts/sign-macos.sh{{ if .IsSnapshot }} --snapshot{{ end }} "{{ .Path }}"{{ end }}'
          output: true
`), builds);
  assert.match(builds, /goos: \[darwin, linux, windows\]/);
  assert.match(builds, /goarch: \[amd64, arm64\]/);
});

test('the script holds no secret and reads the notarization\'s credentials only from the Keychain profile', () => {
  const text = readFileSync(script, 'utf8');
  assert.match(text, /^#!\/usr\/bin\/env bash/);
  assert.match(text, /set -euo pipefail/);
  assert.doesNotMatch(text, /--password|--apple-id [^<]|--key |NOTARY_PASSWORD|APPLE_ID=/);
  assert.match(text, /cannot be stapled/);
  assert.ok(statSync(script).mode & 0o100, 'executable');
});

test('the README names the identity, the team, the notary profile and the check a user runs, as the script has them', () => {
  const readme = readFileSync(join(repo, 'README.md'), 'utf8');
  const install = readme.slice(readme.indexOf('\n## Install\n'), readme.indexOf('\n## First run\n'));
  assert.match(install, /signed with the Developer ID of KLOUDY \(team PP3PVUEBBT\) and notarized by Apple/);
  assert.ok(install.includes('codesign -dv --verbose=2 ./dormeto'), 'how to check a download');
  assert.doesNotMatch(readme, /xattr|quarantine mark/);
  const releasing = readme.slice(readme.indexOf('\n## Releasing\n'), readme.indexOf('\n## License\n'));
  assert.ok(releasing.includes(`\`${IDENTITY}\``), 'the identity');
  assert.ok(releasing.includes(`xcrun notarytool store-credentials ${PROFILE} --apple-id <Apple ID> --team-id ${TEAM}`), 'the profile');
  assert.ok(releasing.includes('scripts/sign-macos.sh'));
  const text = readFileSync(script, 'utf8');
  assert.ok(text.includes(`team="\${DORMETO_SIGN_TEAM:-${TEAM}}"`));
  assert.ok(text.includes('identity="${DORMETO_SIGN_IDENTITY:-Developer ID Application: KLOUDY ($team)}"'));
  assert.ok(text.includes(`profile="\${DORMETO_NOTARY_PROFILE:-${PROFILE}}"`));
});
