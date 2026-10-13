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

// A throwaway repository on main with a bare origin, release.sh and npm/ copied in,
// a tiny Go module, stubs for security, goreleaser and npm, and a latest.json.
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
  cpSync(join(repo, 'npm'), join(work, 'npm'), { recursive: true, filter: (f) => !f.endsWith('.test.mjs') });
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
  for (const r of ['cli', 'homebrew-tap']) write(join(root, 'api', 'repos', 'Dormeto', r), '{}');
  write(join(root, 'latest.json'), JSON.stringify({ version: latest, router: '0.29.0', published: '2026-10-12T00:00:00.000Z' }));

  // security: exits 1 for the items listed in MISSING; with -w it prints "token-<item>".
  write(join(bin, 'security'), `#!/bin/sh
set +eu # bash exports SHELLOPTS (errexit, nounset) to children when it is set in the environment
echo "security $*" >> "${log}"
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
  // npm: records "stage publish <dir> <userconfig>" and the userconfig's mode and text at that moment. A staged package
  // counts as published (npm view gives its version) once NPM_APPROVE is set: the maintainer's approval on npmjs.com.
  write(join(bin, 'npm'), `#!/bin/sh
set +eu # bash exports SHELLOPTS (errexit, nounset) to children when it is set in the environment
cfg=""; prev=""
for a in "$@"; do [ "$prev" = --userconfig ] && cfg="$a"; prev="$a"; done
echo "npm $* | cwd=$(basename "$PWD")" >> "${log}"
[ "$1" = --version ] && { echo "\${NPM_VERSION:-11.21.0}"; exit 0; }
[ "$1" = whoami ] && { [ -n "$NPM_WHOAMI_FAIL" ] && { echo "npm ERR! 401" >&2; exit 1; }; echo someone; exit 0; }
if [ "$1" = view ]; then
  [ -n "$NPM_TAKEN" ] && { echo "$NPM_TAKEN"; exit 0; }
  p=\${2#@dormeto/}; p=\${p%@*}
  [ -n "$NPM_APPROVE" ] && [ -f "${root}/staged/$p" ] && echo "${VERSION}"
  exit 0
fi
if [ -n "$cfg" ]; then
  echo "userconfig $cfg mode=$(stat -f %Lp "$cfg" 2>/dev/null || stat -c %a "$cfg") text=$(cat "$cfg")" >> "${log}"
fi
[ "$NPM_FAIL" = "$(basename "$PWD")" ] && { echo "npm ERR! simulated failure" >&2; exit 1; }
[ "$1 $2" = "stage publish" ] && { mkdir -p "${root}/staged"; : > "${root}/staged/$(basename "$PWD")"; }
exit 0
`, 0o755);
  write(join(root, 'after.sh'), `#!/bin/sh\necho "after $*" >> "${log}"\n`, 0o755);
  const env = {
    ...process.env,
    HOME: join(root, 'home'),
    DORMETO_SECURITY: join(bin, 'security'),
    DORMETO_GORELEASER: join(bin, 'goreleaser'),
    DORMETO_NPM: join(bin, 'npm'),
    DORMETO_AFTER: join(root, 'after.sh'),
    DORMETO_GITHUB_API: `file://${join(root, 'api')}`,
    DORMETO_MANAGER_LATEST_URL: `file://${join(root, 'latest.json')}`,
    TMPDIR: join(root, 'tmp'),
    MISSING: '',
    NPM_APPROVE: '1',
    DORMETO_POLL: '1',
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

test('release.sh refuses a main that is not origin\'s (ahead and behind)', () => {
  const f = fixture();
  try {
    write(join(f.work, 'ahead.txt'), 'x');
    git(f.work, 'add', '-A');
    git(f.work, 'commit', '-m', 'ahead');
    refused(null, f, [VERSION, '--dry-run'], /origin/);
    git(f.work, 'reset', '--hard', 'HEAD~1');
    const other = join(f.root, 'other');
    git(f.root, 'clone', f.origin, other);
    write(join(other, 'behind.txt'), 'x');
    git(other, 'add', '-A');
    git(other, 'commit', '-m', 'behind');
    git(other, 'push', 'origin', 'main');
    refused(null, f, [VERSION, '--dry-run'], /origin/);
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

for (const item of ['npm-dormeto', 'github-dormeto']) {
  test(`release.sh refuses a missing Keychain item ${item}`, () => {
    const f = fixture();
    try {
      f.run.env = { MISSING: item };
      refused(null, f, [VERSION, '--dry-run'], new RegExp(`Keychain.*${item}`));
      assert.equal(/ -w/.test(f.calls()), false, 'a check never reads the token');
    } finally { f.cleanup(); }
  });
}

test('release.sh --dry-run builds a snapshot and the npm packages and publishes nothing', () => {
  const f = fixture();
  try {
    const out = f.run(VERSION, '--dry-run');
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.match(calls, /goreleaser release --snapshot --clean --release-notes \S+/);
    assert.doesNotMatch(calls, /^npm (?!--version )/m, 'npm only asked its version');
    assert.doesNotMatch(calls, /^after /m);
    assert.doesNotMatch(calls, / -w/, 'no token read');
    assert.doesNotMatch(calls, /token-/);
    assert.equal(git(f.work, 'tag'), '');
    assert.equal(git(f.origin, 'tag'), '');
    assert.equal(readFileSync(join(f.root, 'notes.copy'), 'utf8'), `- the release notes of ${VERSION}\n- second line\n`);
    assert.equal(readdirSync(join(f.work, 'dist', 'npm')).length, 7);
    assert.match(out.stdout, /dry run/i);
    assert.equal(readdirSync(f.env.TMPDIR).length, 0, 'temporary files removed');
    assert.equal(existsSync(join(f.env.HOME, '.npmrc')), false);
  } finally { f.cleanup(); }
});

test('release.sh tags, releases, stages the six platform packages before the main one, waits for their approval, then checks', () => {
  const f = fixture();
  try {
    const out = f.run(VERSION);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.equal(git(f.origin, 'tag'), `v${VERSION}`);
    assert.equal(git(f.origin, 'cat-file', '-t', `v${VERSION}`), 'tag', 'annotated');
    const calls = f.calls();
    assert.match(calls, /^goreleaser release --clean --release-notes \S+ GITHUB_TOKEN=token-github-dormeto$/m);
    const staged = [...calls.matchAll(/^npm stage publish .*\| cwd=(\S+)$/gm)].map((m) => m[1]);
    assert.equal(staged.length, 7);
    assert.equal(staged[6], 'cli');
    assert.deepEqual(staged.slice(0, 6).sort(), ['cli-darwin-arm64', 'cli-darwin-x64', 'cli-linux-arm64', 'cli-linux-x64', 'cli-win32-arm64', 'cli-win32-x64']);
    assert.match(calls, /^npm stage publish --access public --userconfig \S+ \| cwd=cli$/m);
    assert.doesNotMatch(calls, /^npm publish/m, 'a stage-only token cannot publish directly');
    // The approval: the links, the platform packages before @dormeto/cli, then each version seen on the registry.
    assert.match(out.stdout, /approve/i);
    assert.ok(out.stdout.indexOf('https://www.npmjs.com/package/@dormeto/cli-darwin-arm64') < out.stdout.indexOf('https://www.npmjs.com/package/@dormeto/cli\n'));
    for (const p of staged) assert.match(calls, new RegExp(`^npm view @dormeto/${p}@0\\.2\\.0 version --prefer-online --userconfig \\S+ \\| cwd=work$`, 'm'));
    assert.ok(calls.lastIndexOf('npm view') < calls.indexOf('after '), 'the checks after the approvals');
    const configs = [...calls.matchAll(/^userconfig (\S+) mode=(\d+) text=(.*)$/gm)];
    assert.equal(configs.length, 7);
    for (const [, file, mode, text] of configs) {
      assert.equal(mode, '600');
      assert.match(text, /_authToken=token-npm-dormeto/);
      assert.equal(existsSync(file), false, 'userconfig removed');
    }
    assert.match(calls, new RegExp(`^after ${VERSION.replaceAll('.', '\\.')}$`, 'm'));
    assert.ok(calls.indexOf('goreleaser') < calls.indexOf('npm stage publish'));
    assert.equal(readdirSync(f.env.TMPDIR).length, 0, 'temporary files removed');
    assert.equal(existsSync(join(f.env.HOME, '.npmrc')), false);
    assert.equal(out.stdout.includes('token-'), false, 'no token in the output');
    assert.equal(out.stderr.includes('token-'), false);
  } finally { f.cleanup(); }
});

test('release.sh removes the npm token file when a staging fails, and stops', () => {
  const f = fixture();
  try {
    f.run.env = { NPM_FAIL: 'cli-linux-x64' };
    const out = f.run(VERSION);
    assert.notEqual(out.status, 0);
    const calls = f.calls();
    assert.doesNotMatch(calls, /cwd=cli$/m, 'the main package is not published after a failure');
    assert.doesNotMatch(calls, /^after /m);
    assert.equal(readdirSync(f.env.TMPDIR).length, 0, 'temporary files removed');
    const files = [...calls.matchAll(/^userconfig (\S+) /gm)].map((m) => m[1]);
    assert.ok(files.length > 0);
    for (const file of files) assert.equal(existsSync(file), false);
    assert.equal(out.stdout.includes('token-'), false);
    assert.equal(out.stderr.includes('token-'), false);
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

// F9: npm whoami proves only that the npm token is valid, and reading the public repositories only that the GitHub
// token is: a refusal says the token is expired or revoked, never that a grant is missing (the publish proves those).
test('release.sh refuses before the tag when npm refuses the token: expired or revoked', () => {
  const f = fixture();
  try {
    f.run.env = { NPM_WHOAMI_FAIL: '1' };
    const out = refused(null, f, [VERSION], /npm token.*whoami.*expired or revoked/);
    assert.doesNotMatch(out.stderr, /grant/);
    assert.match(out.stderr, /npm ERR! 401/, "npm's own message");
    assert.match(out.stderr, /Allowed IP ranges.*IPv4.*IPv6/s);
    assert.equal(out.stderr.includes('token-'), false);
  } finally { f.cleanup(); }
});

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

test('release.sh refuses before the tag when the npm version is taken', () => {
  const f = fixture();
  try {
    f.run.env = { NPM_TAKEN: VERSION };
    refused(null, f, [VERSION], /already published on npm/);
  } finally { f.cleanup(); }
});

test('release.sh refuses before the tag when go.mod is untidy', () => {
  const f = fixture();
  try {
    write(join(f.work, 'go.mod'), 'module example.com/fixture\n\ngo 1.22\n\nrequire github.com/spf13/cobra v1.8.0\n');
    git(f.work, 'commit', '-am', 'untidy');
    git(f.work, 'push', 'origin', 'main');
    refused(null, f, [VERSION], /go mod tidy|go vet|go test/);
  } finally { f.cleanup(); }
});

test('a failure after the tag prints what was done and the commands that finish (npm)', () => {
  const f = fixture();
  try {
    f.run.env = { NPM_FAIL: 'cli-linux-x64' };
    const out = f.run(VERSION);
    assert.notEqual(out.status, 0);
    assert.equal(git(f.origin, 'tag'), `v${VERSION}`);
    assert.match(out.stderr, /only partly released/);
    assert.match(out.stderr, /the tag v0\.2\.0 is pushed/);
    assert.match(out.stderr, /GoReleaser finished/);
    assert.match(out.stderr, /npm packages staged: cli-darwin-arm64 cli-darwin-x64 cli-linux-arm64\n/);
    assert.match(out.stderr, /\(cd dist\/npm\/cli-linux-x64 && npm stage publish --access public --userconfig "\$npmrc"\)/);
    assert.match(out.stderr, /\(cd dist\/npm\/cli && npm stage publish/);
    assert.match(out.stderr, /approve/i);
    assert.doesNotMatch(out.stderr, /cd dist\/npm\/cli-darwin-arm64/);
    assert.match(out.stderr, /scripts\/after-release\.sh 0\.2\.0/);
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
    assert.match(out.stderr, /npm packages staged: none/);
    assert.equal(existsSync(join(f.work, '.git', 'release-notes-0.2.0.md')), true, 'the notes survive for the re-run');
    assert.equal(out.stderr.includes('token-'), false);
  } finally { f.cleanup(); }
});

test('release.sh refuses an npm older than 11.15.0, the first with npm stage (versions compared as numbers)', () => {
  for (const version of ['11.10.1', '11.9.0', '10.9.2']) {
    const f = fixture();
    try {
      f.run.env = { NPM_VERSION: version };
      refused(null, f, [VERSION, '--dry-run'], new RegExp(`npm ${version.replaceAll('.', '\\.')}.*11\\.15\\.0`));
    } finally { f.cleanup(); }
  }
  for (const version of ['11.15.0', '11.21.0', '12.2.0']) {
    const f = fixture();
    try {
      f.run.env = { NPM_VERSION: version };
      const out = f.run(VERSION, '--dry-run');
      assert.equal(out.status, 0, `${version}: ${out.stderr}`);
    } finally { f.cleanup(); }
  }
});

test('release.sh waits for the approvals; when they do not come, it prints the state and what finishes the release', () => {
  const f = fixture();
  try {
    f.run.env = { NPM_APPROVE: '', DORMETO_APPROVAL_WAIT: '2' };
    const out = f.run(VERSION);
    assert.notEqual(out.status, 0);
    assert.equal(git(f.origin, 'tag'), `v${VERSION}`);
    assert.doesNotMatch(f.calls(), /^after /m);
    assert.match(out.stderr, /only partly released/);
    assert.match(out.stderr, /npm packages staged: cli-darwin-arm64 cli-darwin-x64 cli-linux-arm64 cli-linux-x64 cli-win32-arm64 cli-win32-x64 cli\n/);
    assert.match(out.stderr, /not approved/i);
    assert.match(out.stderr, /https:\/\/www\.npmjs\.com\/package\/@dormeto\/cli-darwin-arm64/);
    assert.doesNotMatch(out.stderr, /npm stage publish --access/, 'nothing left to stage');
    assert.match(out.stderr, /scripts\/after-release\.sh 0\.2\.0/);
    assert.equal(readdirSync(f.env.TMPDIR).length, 0, 'temporary files removed');
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
