import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const assemble = join(here, 'assemble.mjs');

const TARGETS = [
  ['darwin', 'arm64', 'darwin', 'arm64'], ['darwin', 'amd64', 'darwin', 'x64'],
  ['linux', 'arm64', 'linux', 'arm64'], ['linux', 'amd64', 'linux', 'x64'],
  ['windows', 'arm64', 'win32', 'arm64'], ['windows', 'amd64', 'win32', 'x64'],
];

// A GoReleaser dist/ with its metadata.json, artifacts.json and one fake binary per target.
function fakeDist(version, { skip } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'dormeto-assemble-'));
  const dist = join(root, 'dist');
  mkdirSync(dist);
  writeFileSync(join(dist, 'metadata.json'), JSON.stringify({ project_name: 'dormeto', tag: `v${version}`, version }));
  const artifacts = [{ name: 'checksums.txt', path: 'dist/checksums.txt', type: 'Checksum' }];
  for (const [goos, goarch] of TARGETS) {
    if (`${goos}/${goarch}` === skip) continue;
    const exe = goos === 'windows' ? 'dormeto.exe' : 'dormeto';
    const dir = join(dist, `dormeto_${goos}_${goarch}_v1`);
    mkdirSync(dir);
    writeFileSync(join(dir, exe), `binary ${goos} ${goarch}`);
    artifacts.push({ name: exe, path: `dist/dormeto_${goos}_${goarch}_v1/${exe}`, goos, goarch, type: 'Binary', extra: { ID: 'dormeto' } });
  }
  writeFileSync(join(dist, 'artifacts.json'), JSON.stringify(artifacts));
  return { root, dist };
}

const run = (root, ...args) => spawnSync(process.execPath, [assemble, ...args], { cwd: root, encoding: 'utf8' });
const json = (file) => JSON.parse(readFileSync(file, 'utf8'));

test('assemble builds the main package and six platform packages of one version', () => {
  const { root, dist } = fakeDist('1.4.2');
  try {
    const out = run(root, '--dist', dist, '--out', join(dist, 'npm'));
    assert.equal(out.status, 0, out.stderr);
    const npm = join(dist, 'npm');
    const main = json(join(npm, 'cli', 'package.json'));
    assert.equal(main.name, '@dormeto/cli');
    assert.equal(main.version, '1.4.2');
    assert.deepEqual(main.bin, { dormeto: 'bin/dormeto.js' });
    assert.ok(existsSync(join(npm, 'cli', 'bin', 'dormeto.js')));
    assert.deepEqual(Object.keys(main.optionalDependencies).sort(), TARGETS.map(([, , os, cpu]) => `@dormeto/cli-${os}-${cpu}`).sort());
    for (const version of Object.values(main.optionalDependencies)) assert.equal(version, '1.4.2');
    let packages = 1;
    for (const [goos, goarch, os, cpu] of TARGETS) {
      const dir = join(npm, `cli-${os}-${cpu}`);
      const pkg = json(join(dir, 'package.json'));
      assert.equal(pkg.name, `@dormeto/cli-${os}-${cpu}`);
      assert.equal(pkg.version, '1.4.2');
      assert.deepEqual(pkg.os, [os]);
      assert.deepEqual(pkg.cpu, [cpu]);
      assert.equal(pkg.license, 'MIT');
      assert.ok(pkg.repository);
      const exe = os === 'win32' ? 'dormeto.exe' : 'dormeto';
      assert.deepEqual(pkg.files, [`bin/${exe}`]);
      assert.equal(readFileSync(join(dir, 'bin', exe), 'utf8'), `binary ${goos} ${goarch}`);
      if (os !== 'win32') assert.equal(statSync(join(dir, 'bin', exe)).mode & 0o111, 0o111, 'executable');
      packages += 1;
    }
    assert.equal(packages, 7);
    assert.match(out.stdout, /7 packages/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('assemble keeps a snapshot version and replaces an earlier output', () => {
  const { root, dist } = fakeDist('0.0.0-SNAPSHOT-abc1234');
  try {
    const npm = join(dist, 'npm');
    mkdirSync(join(npm, 'stale'), { recursive: true });
    assert.equal(run(root, '--dist', dist, '--out', npm).status, 0);
    assert.equal(json(join(npm, 'cli', 'package.json')).version, '0.0.0-SNAPSHOT-abc1234');
    assert.equal(existsSync(join(npm, 'stale')), false);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('assemble refuses a dist with a missing binary and names it', () => {
  const { root, dist } = fakeDist('1.0.0', { skip: 'linux/arm64' });
  try {
    const out = run(root, '--dist', dist, '--out', join(dist, 'npm'));
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /linux\/arm64/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('assemble refuses a dist that is not GoReleaser\'s', () => {
  const root = mkdtempSync(join(tmpdir(), 'dormeto-assemble-'));
  try {
    const out = run(root, '--dist', join(root, 'nothing'), '--out', join(root, 'npm'));
    assert.notEqual(out.status, 0);
    assert.match(out.stderr, /metadata\.json/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});
