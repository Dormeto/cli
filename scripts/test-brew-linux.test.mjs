import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { connect } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// scripts/test-brew-linux.sh, the opt-in check of the Homebrew cask on Linux (Ruling P19), with stubs for GoReleaser
// and podman: what it runs, and its server and containers removed on every exit (success, failure, a signal).

const repo = dirname(dirname(fileURLToPath(import.meta.url)));
const SNAPSHOT = '0.0.0-SNAPSHOT-abc1234';

function write(file, text, mode) {
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text);
  if (mode) chmodSync(file, mode);
}

// A checkout with the script; goreleaser writes dist/ (a cask, metadata.json); podman asks the server for
// metadata.json, as the container's brew would ask for an archive, then answers as PODMAN_SAYS tells it.
function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'dormeto-brew-linux-'));
  const work = join(root, 'cli');
  const log = join(root, 'calls.log');
  write(join(work, 'scripts', 'test-brew-linux.sh'), readFileSync(join(repo, 'scripts', 'test-brew-linux.sh'), 'utf8'), 0o755);
  // goreleaser: dist/ with the cask (its Linux entries' SHA-256 those of checksums.txt, or BAD_SUM's) and metadata.json.
  const sum = (c) => c.repeat(64);
  write(join(root, 'bin', 'goreleaser'), `#!/bin/sh
echo "goreleaser $* GITHUB_TOKEN=\${GITHUB_TOKEN:-unset}" >> "${log}"
mkdir -p dist/homebrew/Casks
cat > dist/homebrew/Casks/dormeto.rb <<'RB'
cask "dormeto" do
  version "${SNAPSHOT}"
  on_macos do
    on_arm do
      sha256 "${sum('1')}"
      url "https://github.com/Dormeto/cli/releases/download/v0.0.0/dormeto_#{version}_darwin_arm64.tar.gz"
    end
  end
  on_linux do
    on_arm do
      sha256 "${sum('3')}"
      url "https://github.com/Dormeto/cli/releases/download/v0.0.0/dormeto_#{version}_linux_arm64.tar.gz"
    end
    on_intel do
      sha256 "${sum('4')}"
      url "https://github.com/Dormeto/cli/releases/download/v0.0.0/dormeto_#{version}_linux_amd64.tar.gz"
    end
  end
  binary "dormeto"
end
RB
printf '%s  %s\\n' ${sum('1')} dormeto_${SNAPSHOT}_darwin_arm64.tar.gz \${BAD_SUM:-${sum('3')}} dormeto_${SNAPSHOT}_linux_arm64.tar.gz ${sum('4')} dormeto_${SNAPSHOT}_linux_amd64.tar.gz > dist/checksums.txt
printf '{"project_name":"dormeto","version":"${SNAPSHOT}"}' > dist/metadata.json
`, 0o755);
  write(join(root, 'bin', 'podman'), `#!/bin/sh
echo "podman $*" >> "${log}"
[ "$1" = rm ] && touch "${root}/release" # the container removed: its podman run ends
[ "$1" = run ] || exit 0
server=""; name=""
while [ $# -gt 0 ]; do case "$1" in --name) name="$2"; shift;; DORMETO_SERVER=*) server="\${1#DORMETO_SERVER=}";; esac; shift; done
local="$(echo "$server" | sed 's#host.containers.internal#127.0.0.1#')"
echo "served $(curl -sS -o /dev/null -w '%{http_code}' "$local/metadata.json") at $server for $name" >> "${log}"
case "$PODMAN_SAYS" in
  fail) echo "Error: Cask 'dormeto' is unavailable" >&2; exit 1;;
  old) echo "version: dormeto 0.0.0-SNAPSHOT-old";;
  hang) echo "running $name" >> "${log}"; n=0; while [ ! -f "${root}/release" ] && [ $n -lt 100 ]; do sleep 0.1; n=$((n + 1)); done;;
  *) echo "version: dormeto ${SNAPSHOT}";;
esac
`, 0o755);
  const env = { ...process.env, DORMETO_PODMAN: join(root, 'bin', 'podman'), DORMETO_GORELEASER: join(root, 'bin', 'goreleaser'), TMPDIR: join(root, 'tmp'), GITHUB_TOKEN: 'not-a-token' };
  mkdirSync(env.TMPDIR);
  const args = (extra) => [join(work, 'scripts', 'test-brew-linux.sh'), ...extra];
  const run = (extra = [], says = '', more = {}) => spawnSync('bash', args(extra), { cwd: work, encoding: 'utf8', env: { ...env, PODMAN_SAYS: says, ...more } });
  const start = (extra = [], says = '') => spawn('bash', args(extra), { cwd: work, env: { ...env, PODMAN_SAYS: says } });
  const calls = () => (existsSync(log) ? readFileSync(log, 'utf8') : '');
  return { root, work, run, start, calls, cleanup: () => rmSync(root, { recursive: true, force: true }) };
}

// The ports the server was asked on, from the podman calls.
const portsOf = (calls) => [...calls.matchAll(/^served \d+ at http:\/\/host\.containers\.internal:(\d+) /gm)].map((m) => Number(m[1]));

// Whether something listens on 127.0.0.1:port.
const listening = (port) => new Promise((resolve) => {
  const socket = connect({ host: '127.0.0.1', port }, () => { socket.destroy(); resolve(true); });
  socket.on('error', () => resolve(false));
});

test('test-brew-linux.sh builds a snapshot without a token, checks the cask\'s Linux entries, serves dist/, installs the cask in a container per architecture, then removes all', async () => {
  const f = fixture();
  try {
    const out = f.run(['arm64', 'amd64']);
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const calls = f.calls();
    assert.match(calls, /^goreleaser release --snapshot --clean GITHUB_TOKEN=unset$/m);
    for (const arch of ['arm64', 'amd64']) {
      assert.match(calls, new RegExp(`^podman run --rm --name (dormeto-brew-linux-\\d+-${arch}) --platform linux/${arch} -v \\S+/dist/homebrew/Casks:/cask:ro .* docker\\.io/homebrew/brew:latest bash -c `, 'm'));
      assert.match(calls, new RegExp(`^served 200 at \\S+ for dormeto-brew-linux-\\d+-${arch}$`, 'm'));
      assert.match(calls, new RegExp(`^podman rm -f dormeto-brew-linux-\\d+-${arch}$`, 'm'));
      assert.match(out.stdout, new RegExp(`linux/${arch}: the cask installs and dormeto ${SNAPSHOT} runs`));
    }
    assert.match(out.stdout, new RegExp(`the cask's linux/arm64 entry: dormeto_${SNAPSHOT}_linux_arm64\\.tar\\.gz, sha256 3{64}`));
    assert.match(out.stdout, new RegExp(`the cask's linux/amd64 entry: dormeto_${SNAPSHOT}_linux_amd64\\.tar\\.gz, sha256 4{64}`));
    const ports = portsOf(calls);
    assert.equal(ports.length, 2);
    assert.equal(await listening(ports[0]), false, 'the server is stopped');
    assert.deepEqual(readdirSync(join(f.root, 'tmp')), [], 'temporary files removed');
  } finally { f.cleanup(); }
});

test('test-brew-linux.sh fails when the install fails or dormeto is not the snapshot, and still removes the server and the container', async () => {
  for (const says of ['fail', 'old']) {
    const f = fixture();
    try {
      const out = f.run(['arm64'], says);
      assert.notEqual(out.status, 0, out.stdout);
      assert.match(out.stderr, says === 'fail' ? /linux\/arm64: brew install --cask failed/ : /linux\/arm64: dormeto version did not say 'dormeto 0\.0\.0-SNAPSHOT-abc1234'/);
      assert.match(f.calls(), /^podman rm -f dormeto-brew-linux-\d+-arm64$/m);
      assert.doesNotMatch(f.calls(), /--platform linux\/amd64/);
      assert.equal(await listening(portsOf(f.calls())[0]), false, 'the server is stopped');
    } finally { f.cleanup(); }
  }
});

test('test-brew-linux.sh removes the server and the container when it is stopped by a signal', async () => {
  const f = fixture();
  try {
    const child = f.start(['amd64'], 'hang');
    const exited = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));
    for (let i = 0; i < 200 && !/^running /m.test(f.calls()); i += 1) await new Promise((r) => setTimeout(r, 50));
    assert.match(f.calls(), /^running dormeto-brew-linux-\d+-amd64$/m);
    const [port] = portsOf(f.calls());
    assert.equal(await listening(port), true, 'the server is up while the container runs');
    const killed = Date.now();
    child.kill('SIGTERM');
    const { code } = await exited;
    assert.ok(Date.now() - killed < 5000, 'stopped at once, without waiting for the container');
    writeFileSync(join(f.root, 'release'), '');
    assert.equal(code, 143);
    assert.match(f.calls(), /^podman rm -f dormeto-brew-linux-\d+-amd64$/m);
    assert.equal(await listening(port), false, 'the server is stopped');
  } finally { f.cleanup(); }
});

test('test-brew-linux.sh fails a container still running after DORMETO_BREW_TIMEOUT, and removes it and the server', async () => {
  const f = fixture();
  try {
    const out = f.run(['amd64'], 'hang', { DORMETO_BREW_TIMEOUT: '1' });
    writeFileSync(join(f.root, 'release'), '');
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /linux\/amd64: still running after 1s \(DORMETO_BREW_TIMEOUT\)/);
    assert.match(f.calls(), /^podman rm -f dormeto-brew-linux-\d+-amd64$/m);
    assert.equal(await listening(portsOf(f.calls())[0]), false, 'the server is stopped');
  } finally { f.cleanup(); }
});

test('test-brew-linux.sh fails before any container when a Linux entry of the cask is not the archive of checksums.txt', () => {
  const f = fixture();
  try {
    const out = f.run(['arm64'], '', { BAD_SUM: '5'.repeat(64) });
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /linux\/arm64: .*dormeto_0\.0\.0-SNAPSHOT-abc1234_linux_arm64\.tar\.gz 3{64}, not .* 5{64}/);
    assert.match(out.stderr, /the cask's Linux entries do not match dist\/checksums\.txt/);
    assert.doesNotMatch(f.calls(), /^podman run/m);
  } finally { f.cleanup(); }
});

test('test-brew-linux.sh checks this machine\'s architecture by default', () => {
  const f = fixture();
  try {
    const out = f.run();
    assert.equal(out.status, 0, out.stdout + out.stderr);
    const native = ['arm64', 'aarch64'].includes(spawnSync('uname', ['-m'], { encoding: 'utf8' }).stdout.trim()) ? 'arm64' : 'amd64';
    assert.match(f.calls(), new RegExp(`--platform linux/${native} `));
    assert.equal([...f.calls().matchAll(/^podman run /gm)].length, 1);
  } finally { f.cleanup(); }
});

test('test-brew-linux.sh refuses an architecture it does not know, before building anything', () => {
  const f = fixture();
  try {
    const out = f.run(['riscv64']);
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /usage/);
    assert.equal(f.calls(), '');
  } finally { f.cleanup(); }
});

test('test-brew-linux.sh is executable, strict, cleans up on exit and runs podman, never docker', () => {
  const text = readFileSync(join(repo, 'scripts', 'test-brew-linux.sh'), 'utf8');
  assert.match(text, /^#!\/usr\/bin\/env bash/);
  assert.match(text, /set -euo pipefail/);
  assert.match(text, /trap cleanup EXIT/);
  assert.doesNotMatch(text, /^\s*docker\s/m);
  assert.ok(statSync(join(repo, 'scripts', 'test-brew-linux.sh')).mode & 0o100, 'executable');
});
