import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const launcher = join(here, 'cli', 'bin', 'dormeto.js');
const platform = process.platform;
const arch = process.arch;
const name = `cli-${platform}-${arch}`;
const windows = platform === 'win32';

// An installation as npm lays it out: node_modules/@dormeto/cli (the launcher)
// and, unless `binary` is null, node_modules/@dormeto/cli-<platform>-<arch>.
function install(binary) {
  const root = mkdtempSync(join(tmpdir(), 'dormeto-launcher-'));
  const modules = join(root, 'node_modules', '@dormeto');
  mkdirSync(join(modules, 'cli', 'bin'), { recursive: true });
  copyFileSync(launcher, join(modules, 'cli', 'bin', 'dormeto.js'));
  if (binary !== null) {
    mkdirSync(join(modules, name, 'bin'), { recursive: true });
    writeFileSync(join(modules, name, 'package.json'), JSON.stringify({ name: `@dormeto/${name}`, version: '1.2.3' }));
    const file = join(modules, name, 'bin', windows ? 'dormeto.exe' : 'dormeto');
    writeFileSync(file, `#!${process.execPath}\n${binary}`);
    chmodSync(file, 0o755);
  }
  return { root, script: join(modules, 'cli', 'bin', 'dormeto.js') };
}

const run = (script, args = []) => spawnSync(process.execPath, [script, ...args], { encoding: 'utf8' });
const skip = windows ? 'the fake binary is a script with a shebang' : false;

test('the launcher runs the platform package binary with the arguments', { skip }, () => {
  const { root, script } = install('console.log(JSON.stringify(process.argv.slice(2)));');
  try {
    const out = run(script, ['estimate', '--profile', 'a b', '--', '-x']);
    assert.equal(out.status, 0);
    assert.deepEqual(JSON.parse(out.stdout), ['estimate', '--profile', 'a b', '--', '-x']);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('the launcher exits with the binary exit code and passes stderr through', { skip }, () => {
  const { root, script } = install('console.error("boom"); process.exit(7);');
  try {
    const out = run(script, ['x']);
    assert.equal(out.status, 7);
    assert.equal(out.stderr, 'boom\n');
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('the launcher passes stdin to the binary', { skip }, () => {
  const { root, script } = install('process.stdin.pipe(process.stdout);');
  try {
    const out = spawnSync(process.execPath, [script], { input: 'hello', encoding: 'utf8' });
    assert.equal(out.stdout, 'hello');
  } finally { rmSync(root, { recursive: true, force: true }); }
});

// A binary that says it is ready, then waits for a signal and exits with a code of its own.
const waiter = (signal) => `
process.on('${signal}', () => { console.log('got ${signal}'); process.exit(${signal === 'SIGINT' ? 3 : signal === 'SIGTERM' ? 4 : 5}); });
console.log('ready');
setInterval(() => {}, 1000);
`;

for (const [signal, code] of [['SIGINT', 3], ['SIGTERM', 4], ['SIGHUP', 5]]) {
  test(`the launcher forwards ${signal} to the binary and exits with its code ${code}`, { skip }, async () => {
    const { root, script } = install(waiter(signal));
    try {
      const child = spawn(process.execPath, [script], { stdio: ['ignore', 'pipe', 'pipe'] });
      let out = '';
      const ready = new Promise((resolve) => child.stdout.on('data', (d) => { out += d; if (out.includes('ready')) resolve(); }));
      const exited = new Promise((resolve) => child.on('exit', (c, s) => resolve({ code: c, signal: s })));
      await ready;
      child.kill(signal);
      const end = await exited;
      assert.deepEqual(end, { code, signal: null });
      assert.match(out, new RegExp(`got ${signal}`));
    } finally { rmSync(root, { recursive: true, force: true }); }
  });
}

test('the launcher dies of the signal that killed the binary', { skip }, () => {
  const { root, script } = install('process.kill(process.pid, "SIGKILL");');
  try {
    const out = run(script);
    assert.equal(out.signal, 'SIGKILL');
    assert.equal(out.status, null);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('the launcher says which platform package is missing and that --omit=optional removes it', () => {
  const { root, script } = install(null);
  try {
    const out = run(script, ['version']);
    assert.equal(out.status, 1);
    assert.match(out.stderr, new RegExp(`npm install @dormeto/${name}\\b`));
    assert.match(out.stderr, /--omit=optional/);
    assert.equal(out.stdout, '');
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('the launcher names the platform it cannot serve', () => {
  const { root, script } = install(null);
  try {
    const out = spawnSync(process.execPath, ['-e', `Object.defineProperty(process, 'platform', { value: 'freebsd' }); process.argv[1] = ${JSON.stringify(script)}; require(${JSON.stringify(script)});`], { encoding: 'utf8' });
    assert.equal(out.status, 1);
    assert.match(out.stderr, /freebsd/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

// A binary as the real one behaves: the first SIGINT starts a graceful stop (exit code 3 a moment later),
// the second has its default effect and kills it at once (signal.Stop in cmd/dormeto/estimate.go).
const graceful = `
process.once('SIGINT', () => { console.log('graceful stop'); setTimeout(() => process.exit(3), 400); });
console.log('ready');
setInterval(() => {}, 1000);
`;

test('on a terminal Ctrl-C reaches the group once: the binary stops gracefully and its code is the launcher\'s', { skip }, async () => {
  const { root, script } = install(graceful);
  try {
    // detached: the launcher leads its own process group, as a foreground job does; the signal goes to the group.
    const child = spawn(process.execPath, [script], { stdio: ['ignore', 'pipe', 'pipe'], detached: true, env: { ...process.env, DORMETO_LAUNCHER_TTY: '1' } });
    let out = '';
    const ready = new Promise((resolve) => child.stdout.on('data', (d) => { out += d; if (out.includes('ready')) resolve(); }));
    const exited = new Promise((resolve) => child.on('exit', (c, s) => resolve({ code: c, signal: s })));
    await ready;
    process.kill(-child.pid, 'SIGINT');
    assert.deepEqual(await exited, { code: 3, signal: null });
    assert.match(out, /graceful stop/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('without a terminal SIGINT sent to the launcher alone is forwarded once', { skip }, async () => {
  const { root, script } = install(graceful);
  try {
    const child = spawn(process.execPath, [script], { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, DORMETO_LAUNCHER_TTY: '0' } });
    let out = '';
    const ready = new Promise((resolve) => child.stdout.on('data', (d) => { out += d; if (out.includes('ready')) resolve(); }));
    const exited = new Promise((resolve) => child.on('exit', (c, s) => resolve({ code: c, signal: s })));
    await ready;
    child.kill('SIGINT');
    assert.deepEqual(await exited, { code: 3, signal: null });
  } finally { rmSync(root, { recursive: true, force: true }); }
});
