import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = dirname(dirname(fileURLToPath(import.meta.url)));
const env = { ...process.env };
for (const name of ['VERSION', 'MAKEFLAGS', 'MAKELEVEL', 'MFLAGS']) delete env[name];

test('make dry-run without VERSION fails at once and says how to give it', () => {
  for (const args of [['dry-run'], ['dry-run', 'VERSION=']]) {
    const r = spawnSync('make', args, { cwd: repo, encoding: 'utf8', env });
    assert.notEqual(r.status, 0);
    assert.match(r.stderr, /VERSION is required: make dry-run VERSION=x\.y\.z/);
    assert.doesNotMatch(r.stdout + r.stderr, /usage:|release\.sh/);
  }
});

test('make release and make after-release without VERSION fail at once and say how to give it', () => {
  for (const target of ['release', 'after-release']) {
    for (const args of [[target], [target, 'VERSION=']]) {
      const r = spawnSync('make', args, { cwd: repo, encoding: 'utf8', env });
      assert.notEqual(r.status, 0);
      assert.match(r.stderr, new RegExp(`VERSION is required: make ${target} VERSION=x\\.y\\.z`));
      assert.doesNotMatch(r.stdout + r.stderr, /usage:|release\.sh/);
    }
  }
});

// -n prints the recipes and runs none of them: nothing here tags or publishes.
const recipe = (...args) => {
  const r = spawnSync('make', ['-n', ...args], { cwd: repo, encoding: 'utf8', env });
  assert.equal(r.status, 0, r.stderr);
  return r.stdout;
};

test('each release target runs its script with the version', () => {
  assert.match(recipe('dry-run', 'VERSION=1.2.3'), /^scripts\/release\.sh 1\.2\.3 --dry-run$/m);
  assert.match(recipe('release', 'VERSION=1.2.3'), /^scripts\/release\.sh 1\.2\.3$/m);
  assert.match(recipe('after-release', 'VERSION=1.2.3'), /^scripts\/after-release\.sh 1\.2\.3$/m);
});

test('make brew-linux runs the Linux cask check, for the architectures given', () => {
  assert.match(recipe('brew-linux'), /^scripts\/test-brew-linux\.sh\s*$/m);
  assert.match(recipe('brew-linux', 'ARCHS=arm64 amd64'), /^scripts\/test-brew-linux\.sh arm64 amd64$/m);
});

test('make help lists every target, with its name apart from its text', () => {
  const r = spawnSync('make', ['help'], { cwd: repo, encoding: 'utf8', env });
  assert.equal(r.status, 0, r.stderr);
  for (const target of ['build', 'test', 'snapshot', 'dry-run', 'release', 'after-release', 'brew-linux']) {
    assert.match(r.stdout, new RegExp(`^  ${target} +\\S`, 'm'));
  }
});

test('the cask leaves the quarantine to Gatekeeper: the macOS binaries are signed and notarized, nothing removes it', () => {
  const yaml = readFileSync(join(repo, '.goreleaser.yaml'), 'utf8');
  const cask = yaml.slice(yaml.indexOf('\nhomebrew_casks:'));
  assert.match(cask, /binaries: \[dormeto\]/);
  assert.doesNotMatch(cask, /custom_block|postflight|on_macos|xattr|quarantine|staged_path|hooks:/);
  assert.doesNotMatch(yaml, /unsigned/);
});
