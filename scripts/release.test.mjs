import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = dirname(dirname(fileURLToPath(import.meta.url)));
const VERSION = '0.2.0';

const git = (cwd, ...args) => {
  const out = spawnSync('git', args, { cwd, encoding: 'utf8', env: { ...process.env, GIT_AUTHOR_NAME: 't', GIT_AUTHOR_EMAIL: 't@t', GIT_COMMITTER_NAME: 't', GIT_COMMITTER_EMAIL: 't@t' } });
  assert.equal(out.status, 0, `git ${args.join(' ')}: ${out.stderr}`);
  return out.stdout.trim();
};

function write(file, text, mode) {
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text);
  if (mode) chmodSync(file, mode);
}

// A throwaway repository on main with a bare origin and release.sh and sign-macos.sh copied in, a tiny Go module, stubs
// for security, xcrun (notarytool), goreleaser and npm (a tripwire), and a latest.json.
function fixture({ manager = '0.10.0', latest = '0.10.0' } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'dormeto-release-'));
  const work = join(root, 'work');
  const origin = join(root, 'origin.git');
  const bin = join(root, 'bin');
  const log = join(root, 'calls.log');
  mkdirSync(work);
  git(root, 'init', '--bare', '-b', 'main', origin);
  git(work, 'init', '-b', 'main');
  git(work, 'remote', 'add', 'origin', origin);
  mkdirSync(join(work, 'scripts'));
  cpSync(join(repo, 'scripts', 'release.sh'), join(work, 'scripts', 'release.sh'));
  cpSync(join(repo, 'scripts', 'sign-macos.sh'), join(work, 'scripts', 'sign-macos.sh'));
  cpSync(join(repo, 'LICENSE'), join(work, 'LICENSE'));
  write(join(work, 'go.mod'), 'module example.com/fixture\n\ngo 1.22\n');
  write(join(work, 'fixture.go'), 'package fixture\n');
  write(join(work, 'fixture_test.go'), 'package fixture\n\nimport "testing"\n\nfunc TestFixture(t *testing.T) {}\n');
  write(join(work, '.gitignore'), 'dist/\n');
  write(join(work, 'CHANGELOG.md'), `# Changelog\n\n## ${VERSION}\n\n- the release notes of ${VERSION}\n- second line\n\n## 0.1.0\n\n- the first\n`);
  write(join(work, 'testdata', 'parity', 'versions.json'), JSON.stringify({ manager, router: '0.29.0', commit: 'abc' }));
  git(work, 'add', '-A');
  git(work, 'commit', '-m', 'fixture');
  git(work, 'push', '-u', 'origin', 'main');
  // Not executable, so not run, until a test or the goreleaser stub (REFUSE_PUSHES_AFTER) makes origin refuse pushes.
  write(join(origin, 'hooks', 'pre-receive'), '#!/bin/sh\necho "refused by the fixture" >&2\nexit 1\n');
  for (const r of ['cli', 'homebrew-tap']) write(join(root, 'api', 'repos', 'Dormeto', r), '{}');
  write(join(root, 'latest.json'), JSON.stringify({ version: latest, router: '0.29.0', published: '2026-10-12T00:00:00.000Z' }));

  // security: exits 1 for the items listed in MISSING; with -w it prints "token-<item>". find-identity lists KLOUDY's
  // Developer ID, none with "codesigning" in MISSING.
  write(join(bin, 'security'), `#!/bin/sh
set +eu # bash exports SHELLOPTS (errexit, nounset) to children when it is set in the environment
echo "security $*" >> "${log}"
if [ "$1" = find-identity ]; then
  case " $MISSING " in *" codesigning "*) echo "     0 valid identities found"; exit 0;; esac
  echo '  1) 0123456789ABCDEF0123456789ABCDEF01234567 "Developer ID Application: KLOUDY (PP3PVUEBBT)"'
  echo "     1 valid identities found"; exit 0
fi
item=""; want=0
while [ $# -gt 0 ]; do case "$1" in -s) item="$2"; shift;; -w) want=1;; esac; shift; done
case " $MISSING " in *" $item "*) echo "The specified item could not be found in the keychain." >&2; exit 44;; esac
[ "$want" = 1 ] && echo "token-$item"
exit 0
`, 0o755);
  // goreleaser: records its arguments, the token and the notes; makes a dist/ as the real one does.
  write(join(bin, 'goreleaser'), `#!/bin/sh
set +eu # bash exports SHELLOPTS (errexit, nounset) to children when it is set in the environment
echo "goreleaser $* GITHUB_TOKEN=\${GITHUB_TOKEN:-}" >> "${log}"
[ "$1" = check ] && exit 0
[ -n "$GORELEASER_FAIL" ] && { echo "goreleaser: simulated failure" >&2; exit 1; }
[ -n "$REFUSE_PUSHES_AFTER" ] && chmod +x "${origin}/hooks/pre-receive"
notes=""
while [ $# -gt 0 ]; do case "$1" in --release-notes) notes="$2"; shift;; esac; shift; done
cat "$notes" > "${root}/notes.copy"
mkdir -p dist
printf '{"version":"${VERSION}"}' > dist/metadata.json
printf '[' > dist/artifacts.json
sep=""
for t in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/arm64 windows/amd64; do
  os=\${t%/*}; arch=\${t#*/}; exe=dormeto; [ "$os" = windows ] && exe=dormeto.exe
  mkdir -p "dist/dormeto_\${os}_\${arch}"; echo bin > "dist/dormeto_\${os}_\${arch}/$exe"
  printf '%s{"type":"Binary","goos":"%s","goarch":"%s","path":"dist/dormeto_%s_%s/%s"}' "$sep" "$os" "$arch" "$os" "$arch" "$exe" >> dist/artifacts.json
  sep=","
done
printf ']' >> dist/artifacts.json
`, 0o755);
  // xcrun notarytool: the profile answers its history, but with "dormeto-notary" in MISSING; a submit is GoReleaser's
  // hook's, never release.sh's: recorded and refused.
  write(join(bin, 'xcrun'), `#!/bin/sh
set +eu
echo "xcrun $*" >> "${log}"
case "$1 $2" in
  "notarytool history") case " $MISSING " in *" dormeto-notary "*) echo "Error: No Keychain password item found for profile: dormeto-notary" >&2; exit 69;; esac; echo "Successfully received submission history."; exit 0;;
esac
echo "xcrun: unexpected $*" >&2; exit 1
`, 0o755);
  // npm: not a channel (the user, 2026-10-13): a tripwire that only records a call.
  write(join(bin, 'npm'), `#!/bin/sh\necho "npm $* | cwd=$(basename "$PWD")" >> "${log}"\nexit 1\n`, 0o755);
  write(join(root, 'after.sh'), `#!/bin/sh\nset +eu # bash exports SHELLOPTS (errexit, nounset) to children when it is set in the environment\necho "after $*" >> "${log}"\n[ -z "$AFTER_FAIL" ] || { echo "after-release: simulated failure" >&2; exit 1; }\n`, 0o755);
  const env = {
    ...process.env,
    HOME: join(root, 'home'),
    GIT_AUTHOR_NAME: 't', GIT_AUTHOR_EMAIL: 't@t', GIT_COMMITTER_NAME: 't', GIT_COMMITTER_EMAIL: 't@t',
    DORMETO_SECURITY: join(bin, 'security'),
    DORMETO_XCRUN: join(bin, 'xcrun'),
    DORMETO_GORELEASER: join(bin, 'goreleaser'),
    DORMETO_NPM: join(bin, 'npm'),
    DORMETO_AFTER: join(root, 'after.sh'),
    DORMETO_GITHUB_API: `file://${join(root, 'api')}`,
    DORMETO_MANAGER_LATEST_URL: `file://${join(root, 'latest.json')}`,
    TMPDIR: join(root, 'tmp'),
    MISSING: '',
  };
  mkdirSync(env.HOME);
  mkdirSync(env.TMPDIR);
  const run = (...args) => spawnSync('bash', [join(work, 'scripts', 'release.sh'), ...args], { cwd: work, encoding: 'utf8', env: { ...env, ...(run.env ?? {}) } });
  return { root, work, origin, log, env, run, calls: () => (existsSync(log) ? readFileSync(log, 'utf8') : ''), cleanup: () => rmSync(root, { recursive: true, force: true }) };
}

function refused(t, f, args, pattern) {
  const out = f.run(...args);
  assert.notEqual(out.status, 0, `expected a refusal, got:\n${out.stdout}${out.stderr}`);
  assert.match(out.stderr, pattern);
  assert.equal(f.calls().includes('goreleaser release'), false, 'no build after a refusal');
  assert.equal(git(f.work, 'tag'), '', 'no tag after a refusal');
  assert.equal(git(f.origin, 'tag'), '', 'no pushed tag after a refusal');
  return out;
}

test('release.sh needs a version of the form x.y.z', () => {
  const f = fixture();
  try {
    refused(null, f, [], /usage/i);
    refused(null, f, ['v1.2.3', '--dry-run'], /version/i);
    refused(null, f, ['1.2', '--dry-run'], /version/i);
    refused(null, f, [VERSION, '--nope'], /usage/i);
  } finally { f.cleanup(); }
});

test('release.sh refuses a branch other than main', () => {
  const f = fixture();
  try {
    git(f.work, 'checkout', '-b', 'topic');
    refused(null, f, [VERSION, '--dry-run'], /main/);
  } finally { f.cleanup(); }
});

test('release.sh refuses a dirty tree (modified and untracked files)', () => {
  const f = fixture();
  try {
    write(join(f.work, 'fixture.go'), 'package fixture\n// changed\n');
    refused(null, f, [VERSION, '--dry-run'], /clean|uncommitted/i);
    git(f.work, 'checkout', 'fixture.go');
    write(join(f.work, 'new.txt'), 'x');
    refused(null, f, [VERSION, '--dry-run'], /clean|uncommitted/i);
  } finally { f.cleanup(); }
});

// A commit on origin that this checkout does not have.
function originMoves(f) {
  const other = join(f.root, 'other');
  git(f.root, 'clone', f.origin, other);
  write(join(other, 'behind.txt'), 'x');
  git(other, 'add', '-A');
  git(other, 'commit', '-m', 'behind');
  git(other, 'push', 'origin', 'main');
}
function localCommit(f) {
  write(join(f.work, 'ahead.txt'), 'x');
  git(f.work, 'add', '-A');
  git(f.work, 'commit', '-m', 'ahead');
}

test('release.sh refuses a main behind origin\'s, and one that has diverged from it, pushing nothing', () => {
  const f = fixture();
  try {
    originMoves(f);
    const moved = git(f.origin, 'rev-parse', 'main');
    refused(null, f, [VERSION], /behind origin\/main or diverged.*pull/);
    localCommit(f);
    refused(null, f, [VERSION], /behind origin\/main or diverged.*pull/);
    assert.equal(git(f.origin, 'rev-parse', 'main'), moved);
  } finally { f.cleanup(); }
});

test('release.sh refuses local commits with a dirty tree, modified or untracked, and pushes nothing', () => {
  const f = fixture();
  try {
    const pushed = git(f.origin, 'rev-parse', 'main');
    localCommit(f);
    write(join(f.work, 'untracked.txt'), 'x');
    refused(null, f, [VERSION], /not clean/);
    rmSync(join(f.work, 'untracked.txt'));
    write(join(f.work, 'ahead.txt'), 'changed');
    refused(null, f, [VERSION], /not clean/);
    assert.equal(git(f.origin, 'rev-parse', 'main'), pushed);
  } finally { f.cleanup(); }
});

test('release.sh pushes local commits with the release, after the checks, and the tag is on origin\'s main', () => {
  const f = fixture();
  try {
    localCommit(f);
    const local = git(f.work, 'rev-parse', 'HEAD');
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.equal(git(f.origin, 'rev-parse', `v${VERSION}^{commit}`), local);
    assert.equal(git(f.origin, 'merge-base', '--is-ancestor', local, 'main'), '');
    assert.equal(git(f.work, 'rev-parse', 'HEAD'), git(f.origin, 'rev-parse', 'main'));
  } finally { f.cleanup(); }
});

test('release.sh pushes local commits together with the release commit of a leading ## Unreleased', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    localCommit(f);
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.deepEqual(subjects(f.origin, 3), [`chore: next iteration after cli ${VERSION}`, `chore(release): cli ${VERSION}`, 'ahead']);
  } finally { f.cleanup(); }
});

test('local commits are not pushed by a dry run, which says how many there are', () => {
  const f = fixture();
  try {
    const pushed = git(f.origin, 'rev-parse', 'main');
    localCommit(f);
    const out = f.run(VERSION, '--dry-run');
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(out.stdout, /1 local commit would be pushed/);
    assert.equal(git(f.origin, 'rev-parse', 'main'), pushed);
  } finally { f.cleanup(); }
});

test('local commits are not pushed when a check fails', () => {
  const f = fixture();
  try {
    const pushed = git(f.origin, 'rev-parse', 'main');
    localCommit(f);
    rmSync(join(f.root, 'api', 'repos', 'Dormeto', 'homebrew-tap')); // the last check before the push
    refused(null, f, [VERSION], /token/);
    assert.equal(git(f.origin, 'rev-parse', 'main'), pushed);
  } finally { f.cleanup(); }
});

test('local commits origin refuses stop the release before the tag', () => {
  const f = fixture();
  try {
    localCommit(f);
    chmodSync(join(f.origin, 'hooks', 'pre-receive'), 0o755);
    refused(null, f, [VERSION], /cannot push main.*nothing is published/);
  } finally { f.cleanup(); }
});

test('release.sh refuses a version whose tag exists', () => {
  const f = fixture();
  try {
    git(f.work, 'tag', `v${VERSION}`);
    const out = f.run(VERSION, '--dry-run');
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /tag/);
  } finally { f.cleanup(); }
});

test('release.sh refuses a CHANGELOG without the version\'s section', () => {
  const f = fixture();
  try {
    refused(null, f, ['0.3.0', '--dry-run'], /CHANGELOG\.md.*## 0\.3\.0/);
  } finally { f.cleanup(); }
});

test('release.sh refuses when go test fails', () => {
  const f = fixture();
  try {
    write(join(f.work, 'fixture_test.go'), 'package fixture\n\nimport "testing"\n\nfunc TestFixture(t *testing.T) { t.Fatal("broken") }\n');
    git(f.work, 'commit', '-am', 'break');
    git(f.work, 'push', 'origin', 'main');
    refused(null, f, [VERSION, '--dry-run'], /go test|go vet/);
  } finally { f.cleanup(); }
});

test('release.sh refuses shared cases older than the manager\'s latest release', () => {
  const f = fixture({ manager: '0.10.0', latest: '0.11.0' });
  try {
    refused(null, f, [VERSION, '--dry-run'], /0\.10\.0.*0\.11\.0|0\.11\.0.*0\.10\.0/);
  } finally { f.cleanup(); }
});

test('release.sh compares versions as numbers (0.9.0 is older than 0.10.0)', () => {
  const f = fixture({ manager: '0.9.0', latest: '0.10.0' });
  try {
    refused(null, f, [VERSION, '--dry-run'], /0\.9\.0/);
  } finally { f.cleanup(); }
});

test('release.sh refuses when the manager\'s latest release cannot be read', () => {
  const f = fixture();
  try {
    f.env.DORMETO_MANAGER_LATEST_URL = `file://${join(f.root, 'nothing.json')}`;
    refused(null, f, [VERSION, '--dry-run'], /latest\.json|latest/);
  } finally { f.cleanup(); }
});

test('release.sh refuses a missing Keychain item github-dormeto', () => {
  const f = fixture();
  try {
    f.run.env = { MISSING: 'github-dormeto' };
    refused(null, f, [VERSION, '--dry-run'], /Keychain.*github-dormeto/);
    assert.equal(/ -w/.test(f.calls()), false, 'a check never reads the token');
  } finally { f.cleanup(); }
});

// --- the macOS signature's prerequisites: KLOUDY's Developer ID and the notary profile (scripts/sign-macos.sh) -----------

const IDENTITY_MISSING = /"Developer ID Application: KLOUDY \(PP3PVUEBBT\)" is not in the Keychain/;
const PROFILE_MISSING = /xcrun notarytool store-credentials dormeto-notary --apple-id <Apple ID> --team-id PP3PVUEBBT/;

test('release.sh checks the signing identity and the notary profile before it changes anything, and notarizes nothing itself', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.match(calls, /^security find-identity -v -p codesigning$/m);
    assert.match(calls, /^xcrun notarytool history --keychain-profile dormeto-notary/m);
    assert.ok(calls.indexOf('xcrun notarytool history') < calls.indexOf('goreleaser check'), 'among the checks');
    assert.doesNotMatch(calls, /notarytool submit/, 'GoReleaser\'s build hook notarizes');
  } finally { f.cleanup(); }
});

for (const [what, missing, pattern] of [['the signing identity', 'codesigning', IDENTITY_MISSING], ['the notary profile', 'dormeto-notary', PROFILE_MISSING]]) {
  test(`release.sh refuses without ${what}, before the release commit, saying what to do`, () => {
    const f = fixture();
    try {
      withChangelog(f, UNRELEASED);
      const head = git(f.work, 'rev-parse', 'HEAD');
      f.run.env = { MISSING: missing };
      const out = refused(null, f, [VERSION], pattern);
      assert.match(out.stderr, /release: the macOS binaries cannot be signed and notarized/);
      assert.equal(readFileSync(join(f.work, 'CHANGELOG.md'), 'utf8'), UNRELEASED);
      assert.equal(git(f.work, 'rev-parse', 'HEAD'), head);
      assert.equal(git(f.origin, 'rev-parse', 'main'), head);
      assert.doesNotMatch(f.calls(), / -w/, 'no token read');
    } finally { f.cleanup(); }
  });
}

test('--dry-run without the identity nor the notary profile says both are missing, and still builds the snapshot', () => {
  const f = fixture();
  try {
    f.run.env = { MISSING: 'codesigning dormeto-notary' };
    const out = f.run(VERSION, '--dry-run');
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(out.stdout, IDENTITY_MISSING);
    assert.match(out.stdout, PROFILE_MISSING);
    assert.match(out.stdout, /release: a release would stop on what is missing above/);
    assert.match(out.stdout, /signs the macOS binaries only with the identity in the Keychain, and sends nothing to Apple/);
    assert.match(f.calls(), /goreleaser release --snapshot --clean/);
    assert.doesNotMatch(f.calls(), /notarytool submit/);
  } finally { f.cleanup(); }
});

test('--dry-run with the identity alone names the notary profile missing', () => {
  const f = fixture();
  try {
    f.run.env = { MISSING: 'dormeto-notary' };
    const out = f.run(VERSION, '--dry-run');
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.doesNotMatch(out.stdout, IDENTITY_MISSING);
    assert.match(out.stdout, /the signing identity "Developer ID Application: KLOUDY \(PP3PVUEBBT\)" is in the Keychain/);
    assert.match(out.stdout, PROFILE_MISSING);
    assert.match(out.stdout, /a release would stop/);
  } finally { f.cleanup(); }
});

test('--dry-run with both says the snapshot signs the macOS binaries and sends nothing to Apple', () => {
  const f = fixture();
  try {
    const out = f.run(VERSION, '--dry-run');
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(out.stdout, /release: the snapshot signs the macOS binaries with KLOUDY's Developer ID and sends nothing to Apple/);
    assert.doesNotMatch(out.stdout, /would stop/);
  } finally { f.cleanup(); }
});

test('release.sh needs no npm token: npm is not a channel', () => {
  const f = fixture();
  try {
    f.run.env = { MISSING: 'npm-dormeto' };
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.doesNotMatch(f.calls(), /npm-dormeto/);
    assert.doesNotMatch(f.calls(), /^npm /m);
  } finally { f.cleanup(); }
});

test('release.sh --dry-run builds a snapshot and publishes nothing', () => {
  const f = fixture();
  try {
    const out = f.run(VERSION, '--dry-run');
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.match(calls, /goreleaser release --snapshot --clean --release-notes \S+/);
    assert.doesNotMatch(calls, /^npm /m);
    assert.doesNotMatch(calls, /^after /m);
    assert.doesNotMatch(calls, / -w/, 'no token read');
    assert.doesNotMatch(calls, /token-/);
    assert.equal(git(f.work, 'tag'), '');
    assert.equal(git(f.origin, 'tag'), '');
    assert.equal(readFileSync(join(f.root, 'notes.copy'), 'utf8'), `- the release notes of ${VERSION}\n- second line\n`);
    assert.equal(existsSync(join(f.work, 'dist', 'npm')), false, 'no npm packages');
    assert.match(out.stdout, /dry run/i);
    assert.equal(readdirSync(f.env.TMPDIR).length, 0, 'temporary files removed');
    assert.equal(existsSync(join(f.env.HOME, '.npmrc')), false);
  } finally { f.cleanup(); }
});

test('release.sh tags, releases with GoReleaser (the archives, the GitHub release, the Homebrew tap), then checks', () => {
  const f = fixture();
  try {
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.equal(git(f.origin, 'tag'), `v${VERSION}`);
    assert.equal(git(f.origin, 'cat-file', '-t', `v${VERSION}`), 'tag', 'annotated');
    const calls = f.calls();
    assert.match(calls, /^goreleaser release --clean --release-notes \S+ GITHUB_TOKEN=token-github-dormeto$/m);
    assert.match(calls, new RegExp(`^after ${VERSION.replaceAll('.', '\\.')}$`, 'm'));
    assert.ok(calls.indexOf('goreleaser release') < calls.indexOf('after '));
    assert.doesNotMatch(calls, /^npm /m, 'npm is not a channel');
    assert.doesNotMatch(calls, /npm-dormeto/);
    assert.equal(readdirSync(f.env.TMPDIR).length, 0, 'temporary files removed');
    assert.equal(out.stdout.includes('token-'), false, 'no token in the output');
    assert.equal(out.stderr.includes('token-'), false);
  } finally { f.cleanup(); }
});

// --- the changelog's leading ## Unreleased, and the next iteration -------------------------------------------------

const UNRELEASED = '# Changelog\n\n## Unreleased\n\n- the unreleased notes\n- their second line\n\n## 0.1.0\n\n- the first\n';

function withChangelog(f, text) {
  write(join(f.work, 'CHANGELOG.md'), text);
  git(f.work, 'add', '-A');
  git(f.work, 'commit', '-m', 'the changelog');
  git(f.work, 'push', 'origin', 'main');
}
const subjects = (cwd, n) => git(cwd, 'log', `-${n}`, '--format=%s').split('\n');

test('a leading ## Unreleased is released as the version: its commit is pushed, tagged, and the next iteration follows', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.equal(git(f.origin, 'show', `v${VERSION}:CHANGELOG.md`), UNRELEASED.replace('## Unreleased', `## ${VERSION}`).trimEnd());
    assert.equal(git(f.origin, 'log', '-1', '--format=%s', `v${VERSION}`), `chore(release): cli ${VERSION}`);
    assert.equal(readFileSync(join(f.root, 'notes.copy'), 'utf8'), '- the unreleased notes\n- their second line\n');
    assert.deepEqual(subjects(f.origin, 2), [`chore: next iteration after cli ${VERSION}`, `chore(release): cli ${VERSION}`]);
    assert.equal(readFileSync(join(f.work, 'CHANGELOG.md'), 'utf8'), `# Changelog\n\n## Unreleased\n\n## ${VERSION}\n\n- the unreleased notes\n- their second line\n\n## 0.1.0\n\n- the first\n`);
    assert.equal(git(f.work, 'rev-parse', 'HEAD'), git(f.origin, 'rev-parse', 'main'));
    assert.equal(git(f.work, 'status', '--porcelain'), '');
    assert.match(f.calls(), /^after 0\.2\.0$/m);
  } finally { f.cleanup(); }
});

test('--dry-run with a leading ## Unreleased checks its notes and changes nothing', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    const head = git(f.work, 'rev-parse', 'HEAD');
    const out = f.run(VERSION, '--dry-run');
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.equal(readFileSync(join(f.root, 'notes.copy'), 'utf8'), '- the unreleased notes\n- their second line\n');
    assert.match(out.stdout, /## Unreleased would become ## 0\.2\.0/);
    assert.equal(readFileSync(join(f.work, 'CHANGELOG.md'), 'utf8'), UNRELEASED);
    assert.equal(git(f.work, 'rev-parse', 'HEAD'), head);
    assert.equal(git(f.origin, 'rev-parse', 'main'), head);
    assert.equal(git(f.work, 'status', '--porcelain'), '');
  } finally { f.cleanup(); }
});

test('a check that fails leaves a leading ## Unreleased as it was: no commit, nothing pushed', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    const head = git(f.work, 'rev-parse', 'HEAD');
    rmSync(join(f.root, 'api', 'repos', 'Dormeto', 'homebrew-tap')); // the last check before the commit
    refused(null, f, [VERSION], /token/);
    assert.equal(readFileSync(join(f.work, 'CHANGELOG.md'), 'utf8'), UNRELEASED);
    assert.equal(git(f.work, 'rev-parse', 'HEAD'), head);
    assert.equal(git(f.origin, 'rev-parse', 'main'), head);
  } finally { f.cleanup(); }
});

test('notes under a leading ## Unreleased and a section for the version are refused: which one is meant is not clear', () => {
  const f = fixture();
  try {
    withChangelog(f, `# Changelog\n\n## Unreleased\n\n- new\n\n## ${VERSION}\n\n- hand-written\n`);
    refused(null, f, [VERSION, '--dry-run'], /both.*## Unreleased.*## 0\.2\.0/);
  } finally { f.cleanup(); }
});

test('an empty leading ## Unreleased and no section for the version: nothing to release', () => {
  const f = fixture();
  try {
    withChangelog(f, '# Changelog\n\n## Unreleased\n\n## 0.1.0\n\n- the first\n');
    refused(null, f, [VERSION], /CHANGELOG\.md.*## 0\.2\.0/);
    assert.equal(subjects(f.work, 1)[0], 'the changelog');
  } finally { f.cleanup(); }
});

test('an empty leading ## Unreleased above the version\'s section: that section is released, and nothing is added', () => {
  const f = fixture();
  try {
    withChangelog(f, `# Changelog\n\n## Unreleased\n\n## ${VERSION}\n\n- hand-written\n`);
    const head = git(f.work, 'rev-parse', 'HEAD');
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.equal(readFileSync(join(f.root, 'notes.copy'), 'utf8'), '- hand-written\n');
    assert.equal(git(f.origin, 'rev-parse', `v${VERSION}^{commit}`), head);
    assert.equal(git(f.origin, 'rev-parse', 'main'), head, 'the next iteration is already there');
  } finally { f.cleanup(); }
});

test('a section written by hand is released from its own commit, and the next iteration adds ## Unreleased', () => {
  const f = fixture();
  try {
    const head = git(f.work, 'rev-parse', 'HEAD');
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.equal(git(f.origin, 'rev-parse', `v${VERSION}^{commit}`), head);
    assert.equal(subjects(f.origin, 1)[0], `chore: next iteration after cli ${VERSION}`);
    assert.match(readFileSync(join(f.work, 'CHANGELOG.md'), 'utf8'), /^# Changelog\n\n## Unreleased\n\n## 0\.2\.0\n/);
  } finally { f.cleanup(); }
});

test('a release commit origin refuses stops before the tag, and says how to go on', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    chmodSync(join(f.origin, 'hooks', 'pre-receive'), 0o755);
    const out = refused(null, f, [VERSION], /nothing is published/);
    assert.match(out.stderr, /this release again/);
    assert.match(out.stderr, /git reset --keep HEAD~1/);
    assert.equal(subjects(f.work, 1)[0], `chore(release): cli ${VERSION}`);
    assert.equal(git(f.work, 'status', '--porcelain'), '');

    // Once origin takes pushes, the same command finishes it, from the commit already made.
    chmodSync(join(f.origin, 'hooks', 'pre-receive'), 0o644);
    const again = f.run(VERSION);
    assert.equal(again.status, 0, again.stdout + again.stderr);
    assert.equal(git(f.origin, 'log', '-1', '--format=%s', `v${VERSION}`), `chore(release): cli ${VERSION}`);
  } finally { f.cleanup(); }
});

test('a next iteration origin refuses is reported after a release that is published, with the push and the checks left', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    f.run.env = { REFUSE_PUSHES_AFTER: '1' };
    const out = f.run(VERSION);
    assert.notEqual(out.status, 0);
    assert.equal(git(f.origin, 'tag'), `v${VERSION}`);
    assert.match(out.stderr, /GoReleaser finished/);
    assert.match(out.stderr, /git push origin main/);
    assert.match(out.stderr, /scripts\/after-release\.sh 0\.2\.0/);
    assert.equal(subjects(f.work, 1)[0], `chore: next iteration after cli ${VERSION}`);
  } finally { f.cleanup(); }
});

test('a GoReleaser failure names the next iteration among what is left', () => {
  const f = fixture();
  try {
    withChangelog(f, UNRELEASED);
    f.run.env = { GORELEASER_FAIL: '1' };
    const out = f.run(VERSION);
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /the next iteration: "## Unreleased" back at the top of CHANGELOG\.md/);
  } finally { f.cleanup(); }
});

test('the script is executable-agnostic and has no token literal', () => {
  const text = readFileSync(join(repo, 'scripts', 'release.sh'), 'utf8');
  assert.match(text, /^#!\/usr\/bin\/env bash/);
  assert.match(text, /set -euo pipefail/);
  assert.match(text, /trap /);
  assert.doesNotMatch(text, /npm_[A-Za-z0-9]{20,}|ghp_|github_pat_/);
  assert.ok(statSync(join(repo, 'scripts', 'release.sh')).mode & 0o100, 'executable');
});

// --- tokens and failures before and after the tag ---------------------------------------------------

// F9: reading the public repositories proves only that the GitHub token is valid: a refusal says the token is expired
// or revoked, never that a grant is missing (the publish proves those).

for (const repository of ['cli', 'homebrew-tap']) {
  test(`release.sh refuses before the tag when the GitHub token cannot read ${repository}: expired or revoked`, () => {
    const f = fixture();
    try {
      rmSync(join(f.root, 'api', 'repos', 'Dormeto', repository));
      const out = refused(null, f, [VERSION], new RegExp(`GitHub token.*Dormeto/${repository}.*expired or revoked`));
      assert.doesNotMatch(out.stderr, /grant/);
      assert.equal(out.stderr.includes('token-'), false);
    } finally { f.cleanup(); }
  });
}

test('release.sh refuses before the tag when go.mod is untidy', () => {
  const f = fixture();
  try {
    write(join(f.work, 'go.mod'), 'module example.com/fixture\n\ngo 1.22\n\nrequire github.com/spf13/cobra v1.8.0\n');
    git(f.work, 'commit', '-am', 'untidy');
    git(f.work, 'push', 'origin', 'main');
    refused(null, f, [VERSION], /go mod tidy|go vet|go test/);
  } finally { f.cleanup(); }
});

test('a failure of the checks after the release prints what was done and the command that finishes it', () => {
  const f = fixture();
  try {
    f.run.env = { AFTER_FAIL: '1' };
    const out = f.run(VERSION);
    assert.notEqual(out.status, 0);
    assert.equal(git(f.origin, 'tag'), `v${VERSION}`);
    assert.match(out.stderr, /only partly released/);
    assert.match(out.stderr, /the tag v0\.2\.0 is pushed/);
    assert.match(out.stderr, /GoReleaser finished/);
    assert.doesNotMatch(out.stderr, /goreleaser release --clean/, 'nothing to release again');
    assert.match(out.stderr, /scripts\/after-release\.sh 0\.2\.0/);
    assert.doesNotMatch(out.stderr, /npm/);
    assert.equal(readdirSync(f.env.TMPDIR).length, 0, 'temporary files removed');
    assert.equal(out.stderr.includes('token-'), false);
  } finally { f.cleanup(); }
});

test('a GoReleaser failure after the tag prints the state and the goreleaser command', () => {
  const f = fixture();
  try {
    f.run.env = { GORELEASER_FAIL: '1' };
    const out = f.run(VERSION);
    assert.notEqual(out.status, 0);
    assert.equal(git(f.origin, 'tag'), `v${VERSION}`);
    assert.match(out.stderr, /GoReleaser did not finish/);
    assert.match(out.stderr, /goreleaser release --clean --release-notes \.git\/release-notes-0\.2\.0\.md/);
    assert.doesNotMatch(out.stderr, /npm/);
    assert.equal(existsSync(join(f.work, '.git', 'release-notes-0.2.0.md')), true, 'the notes survive for the re-run');
    assert.equal(out.stderr.includes('token-'), false);
  } finally { f.cleanup(); }
});

test('no token reaches stdout or stderr under shell tracing (SHELLOPTS=xtrace and bash -x)', () => {
  const f = fixture();
  try {
    f.run.env = { SHELLOPTS: 'xtrace' };
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stderr);
    assert.equal((out.stdout + out.stderr).includes('token-'), false);
    const f2 = fixture();
    try {
      const traced = spawnSync('bash', ['-x', join(f2.work, 'scripts', 'release.sh'), VERSION], { cwd: f2.work, encoding: 'utf8', env: f2.env });
      assert.equal(traced.status, 0, traced.stderr);
      assert.equal((traced.stdout + traced.stderr).includes('token-'), false);
    } finally { f2.cleanup(); }
  } finally { f.cleanup(); }
});
