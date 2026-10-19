import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = dirname(dirname(fileURLToPath(import.meta.url)));
const VERSION = '0.2.0';

function write(file, text, mode) {
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text);
  if (mode) chmodSync(file, mode);
}

// Shell that writes, at <path>, a dormeto saying the version <version> (a shell word, expanded when it runs).
const installs = (path, version) => `printf '#!/bin/sh\\necho "dormeto %s"\\n' ${version} > ${path} && chmod 755 ${path}`;

// A throwaway checkout of the cli with scripts/after-release.sh copied in and the website's beside it (../website),
// stubs for curl (the web served from web/), brew, npx (a tripwire), go and sleep, and the release's six archives with their
// checksums.txt. Options: served (https://dormeto.com/install.sh deployed), website (a website checkout beside the cli),
// shadow (a dev build of dormeto first on PATH, as `go install` leaves one), installed (brew has the cask already).
function fixture({ served = false, website = true, shadow = true, installed = false } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'dormeto-after-'));
  const work = join(root, 'cli');
  const bin = join(root, 'bin');
  const web = join(root, 'web');
  const prefix = join(root, 'homebrew');
  const log = join(root, 'calls.log');
  mkdirSync(join(work, 'scripts'), { recursive: true });
  cpSync(join(repo, 'scripts', 'after-release.sh'), join(work, 'scripts', 'after-release.sh'));
  mkdirSync(join(prefix, 'bin'), { recursive: true });

  // The release: six archives and checksums.txt.
  const download = join(web, 'github.com', 'Dormeto', 'cli', 'releases', 'download', `v${VERSION}`);
  let sums = '';
  for (const t of ['darwin_arm64', 'darwin_amd64', 'linux_arm64', 'linux_amd64', 'windows_arm64', 'windows_amd64']) {
    const file = `dormeto_${VERSION}_${t}.${t.startsWith('windows') ? 'zip' : 'tar.gz'}`;
    const body = `archive ${t}\n`;
    write(join(download, file), body);
    sums += `${createHash('sha256').update(body).digest('hex')}  ${file}\n`;
  }
  write(join(download, 'checksums.txt'), sums);

  // install.sh: the deployed one and the website checkout's, each logging that it ran and with which variables.
  const installer = (which) => `#!/bin/sh
set +eu
echo "${which} install.sh DORMETO_VERSION=$DORMETO_VERSION DORMETO_INSTALL_DIR=$DORMETO_INSTALL_DIR" >> "${log}"
[ -n "$INSTALL_FAIL" ] && { echo "install.sh: cannot download" >&2; exit 1; }
mkdir -p "$DORMETO_INSTALL_DIR" && ${installs('"$DORMETO_INSTALL_DIR/dormeto"', '"$DORMETO_VERSION"')}
`;
  if (served) write(join(web, 'dormeto.com', 'install.sh'), installer('deployed'));
  if (website) write(join(root, 'website', 'site', 'install.sh'), installer('website'));

  // curl: the URL's file under web/ (-o or stdout); a missing one is a 404: exit 22 with -f, else the -w code.
  write(join(bin, 'curl'), `#!/bin/sh
set +eu
echo "curl $*" >> "${log}"
out=""; fmt=""; url=""; fail=0
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift;;
    -w) fmt="$2"; shift;;
    http*) url="$1";;
    --*) ;;
    -*) case "$1" in *f*) fail=1;; esac;;
  esac
  shift
done
file="${web}/\${url#*://}"
if [ -f "$file" ]; then
  if [ -n "$out" ]; then cp "$file" "$out"; else cat "$file"; fi
  [ -n "$fmt" ] && printf 200
  exit 0
fi
[ "$fail" = 1 ] && { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
if [ -n "$out" ]; then echo "Not Found" > "$out"; else echo "Not Found"; fi
[ -n "$fmt" ] && printf 404
exit 0
`, 0o755);
  // brew: as Homebrew answers; the cask installed or upgraded links bin/dormeto in its prefix.
  write(join(bin, 'brew'), `#!/bin/sh
set +eu
echo "brew $*" >> "${log}"
case "$1" in
  update) exit 0;;
  --prefix) echo "${prefix}"; exit 0;;
  --caskroom) echo "${prefix}/Caskroom"; exit 0;;
  list)
    case " $* " in *" --formula "*) case " $* " in *" --cask "*) echo "Error: Options --formula and --cask are mutually exclusive." >&2; exit 1;; esac;; esac
    [ -f "${root}/installed" ] && { echo dormeto; exit 0; }
    echo "Error: Cask 'dormeto' is not installed." >&2; exit 1;;
  install|upgrade)
    ${installs(`"${prefix}/bin/dormeto"`, `"\${BREW_GIVES:-${VERSION}}"`)}
    [ -z "$CASK_MISSING" ] && mkdir -p "${prefix}/Caskroom/dormeto/${VERSION}" && ${installs(`"${prefix}/Caskroom/dormeto/${VERSION}/dormeto"`, `"\${BREW_GIVES:-${VERSION}}"`)}
    touch "${root}/installed"; exit 0;;
esac
echo "brew: unexpected $*" >&2; exit 1
`, 0o755);
  // npm is not a channel (the user, 2026-10-13): an npx stub records any call, which the tests refuse.
  write(join(bin, 'npx'), `#!/bin/sh\necho "npx $*" >> "${log}"\nexit 1\n`, 0o755);
  write(join(bin, 'go'), `#!/bin/sh\necho "go $*" >> "${log}"\nmkdir -p "$GOBIN" && ${installs('"$GOBIN/dormeto"', '"${2##*@v}"')}\n`, 0o755);
  // uname: the system under test (UNAME, default Darwin: the quarantine is a macOS matter); xattr: the quarantine is
  // there when XATTR_QUARANTINED is set, as `xattr -p` prints it, else it fails as it does without the attribute.
  write(join(bin, 'uname'), '#!/bin/sh\necho "${UNAME:-Darwin}"\n', 0o755);
  write(join(bin, 'xattr'), `#!/bin/sh
echo "xattr $*" >> "${log}"
[ -n "$XATTR_QUARANTINED" ] && { echo "0083;5f5e1000;Safari;"; exit 0; }
echo "xattr: $3: No such xattr: com.apple.quarantine" >&2; exit 1
`, 0o755);
  write(join(bin, 'sleep'), '#!/bin/sh\nexit 0\n', 0o755);
  if (shadow) write(join(bin, 'dormeto'), '#!/bin/sh\necho "dormeto dev"\n', 0o755);
  if (installed) writeFileSync(join(root, 'installed'), '');

  // Without the dev build, Homebrew's bin/ is on PATH, as on a Mac where brew's dormeto comes first.
  const path = shadow ? `${bin}:${process.env.PATH}` : `${bin}:${join(prefix, 'bin')}:${process.env.PATH}`;
  const env = { ...process.env, PATH: path, TMPDIR: join(root, 'tmp') };
  delete env.DORMETO_WEBSITE_DIR;
  mkdirSync(env.TMPDIR);
  const run = (extra = {}) => spawnSync('bash', [join(work, 'scripts', 'after-release.sh'), VERSION], { cwd: work, encoding: 'utf8', env: { ...env, ...extra } });
  return { root, work, prefix, log, run, calls: () => (existsSync(log) ? readFileSync(log, 'utf8') : ''), cleanup: () => rmSync(root, { recursive: true, force: true }) };
}

// --- the channels: the archives, Homebrew, the install script and go install; npm is not one (the user, 2026-10-13) ---

test('after-release.sh checks the archives, Homebrew, the install script and go install, and never npm', () => {
  const f = fixture({ shadow: false });
  try {
    const out = f.run();
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.doesNotMatch(calls, /^npx /m, 'npm is not a channel');
    assert.doesNotMatch(out.stdout, /npx|npm/);
    assert.match(calls, /^brew /m);
    assert.match(calls, /^go install /m);
    const steps = [...out.stdout.matchAll(/^after-release: (assets and checksums|brew|install\.sh|go install)$/gm)].map((m) => m[1]);
    assert.deepEqual(steps, ['assets and checksums', 'brew', 'install.sh', 'go install']);
  } finally { f.cleanup(); }
});

// --- install.sh before the website is deployed (Ruling P17) -------------------------------------------------------

test('after-release.sh runs the website checkout\'s install.sh while https://dormeto.com/install.sh answers 404, and says so', () => {
  const f = fixture({ shadow: false });
  try {
    const out = f.run();
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.match(calls, new RegExp(`^website install\\.sh DORMETO_VERSION=${VERSION.replaceAll('.', '\\.')} DORMETO_INSTALL_DIR=\\S+$`, 'm'));
    assert.doesNotMatch(calls, /^deployed install\.sh/m);
    assert.match(out.stdout, /https:\/\/dormeto\.com\/install\.sh .*404/);
    // ../website beside the cli checkout, by default.
    assert.ok(out.stdout.includes(`install.sh from the website checkout, ${f.work}/../website/site/install.sh\n`), out.stdout);
    assert.match(out.stdout, /all checks pass for 0\.2\.0/);
  } finally { f.cleanup(); }
});

test('after-release.sh runs https://dormeto.com/install.sh once it answers 200, and says so', () => {
  const f = fixture({ shadow: false, served: true });
  try {
    const out = f.run();
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(f.calls(), /^deployed install\.sh DORMETO_VERSION=0\.2\.0 /m);
    assert.doesNotMatch(f.calls(), /^website install\.sh/m);
    assert.match(out.stdout, /install\.sh from https:\/\/dormeto\.com\/install\.sh/);
  } finally { f.cleanup(); }
});

test('after-release.sh takes the website checkout from DORMETO_WEBSITE_DIR', () => {
  const f = fixture({ shadow: false, website: false });
  try {
    const elsewhere = join(f.root, 'elsewhere');
    write(join(elsewhere, 'site', 'install.sh'), `#!/bin/sh\necho "elsewhere install.sh DORMETO_VERSION=$DORMETO_VERSION" >> "${f.log}"\nmkdir -p "$DORMETO_INSTALL_DIR" && ${installs('"$DORMETO_INSTALL_DIR/dormeto"', '"$DORMETO_VERSION"')}\n`);
    const out = f.run({ DORMETO_WEBSITE_DIR: elsewhere });
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(f.calls(), /^elsewhere install\.sh DORMETO_VERSION=0\.2\.0$/m);
  } finally { f.cleanup(); }
});

test('after-release.sh fails when the install.sh it runs fails', () => {
  for (const served of [false, true]) {
    const f = fixture({ shadow: false, served });
    try {
      const out = f.run({ INSTALL_FAIL: '1' });
      assert.notEqual(out.status, 0, out.stdout + out.stderr);
      assert.match(out.stderr, /install\.sh .*failed/);
      assert.doesNotMatch(out.stdout, /all checks pass/);
    } finally { f.cleanup(); }
  }
});

test('after-release.sh fails, saying where it looked, without the deployed install.sh nor a website checkout', () => {
  const f = fixture({ shadow: false, website: false });
  try {
    const out = f.run();
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /DORMETO_WEBSITE_DIR/);
    assert.match(out.stderr, /website\/site\/install\.sh/);
  } finally { f.cleanup(); }
});

// --- brew ------------------------------------------------------------------------------------------------------

test('after-release.sh installs the cask when brew does not have it, and checks Homebrew\'s dormeto, not the first on PATH', () => {
  const f = fixture({ shadow: true });
  try {
    const out = f.run();
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.match(calls, /^brew list --cask dormeto$/m);
    assert.match(calls, /^brew install --cask dormeto\/tap\/dormeto$/m);
    assert.doesNotMatch(calls, /^brew upgrade/m);
    assert.doesNotMatch(calls, /--formula/);
  } finally { f.cleanup(); }
});

test('after-release.sh upgrades the cask brew has already', () => {
  const f = fixture({ shadow: true, installed: true });
  try {
    const out = f.run();
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.match(f.calls(), /^brew upgrade --cask dormeto\/tap\/dormeto$/m);
    assert.doesNotMatch(f.calls(), /^brew install/m);
  } finally { f.cleanup(); }
});

test('after-release.sh fails when Homebrew\'s dormeto is not the version released', () => {
  const f = fixture({ shadow: true, installed: true });
  try {
    const out = f.run({ BREW_GIVES: '0.1.0' });
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /brew gave 'dormeto 0\.1\.0'.*not 'dormeto 0\.2\.0'/);
  } finally { f.cleanup(); }
});

// --- the quarantine ---------------------------------------------------------------------------------------------

test('after-release.sh passes on macOS when the installed binary has no quarantine attribute', () => {
  const f = fixture({ shadow: true });
  try {
    const out = f.run();
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.ok(f.calls().includes(`xattr -p com.apple.quarantine ${f.prefix}/Caskroom/dormeto/${VERSION}/dormeto\n`), f.calls());
  } finally { f.cleanup(); }
});

test('after-release.sh fails on macOS when the installed binary is quarantined', () => {
  const f = fixture({ shadow: true });
  try {
    const out = f.run({ XATTR_QUARANTINED: '1' });
    assert.notEqual(out.status, 0, out.stdout);
    assert.match(out.stderr, /quarantine/);
    assert.doesNotMatch(out.stdout, /all checks pass/);
  } finally { f.cleanup(); }
});

test('after-release.sh does not look for the quarantine off macOS', () => {
  const f = fixture({ shadow: true });
  try {
    const out = f.run({ UNAME: 'Linux', XATTR_QUARANTINED: '1' });
    assert.equal(out.status, 0, out.stdout + out.stderr);
    assert.doesNotMatch(f.calls(), /^xattr /m);
  } finally { f.cleanup(); }
});

test('after-release.sh fails when the cask\'s binary is not where the quarantine is checked', () => {
  const f = fixture({ shadow: true });
  try {
    const out = f.run({ CASK_MISSING: '1' });
    assert.notEqual(out.status, 0, out.stdout);
    assert.match(out.stderr, /not at .*Caskroom\/dormeto\/0\.2\.0\/dormeto/);
    assert.doesNotMatch(f.calls(), /^xattr /m);
  } finally { f.cleanup(); }
});
