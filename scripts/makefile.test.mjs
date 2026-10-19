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

test('the cask removes the quarantine with Homebrew 7 install steps, not a postflight hook', () => {
  const yaml = readFileSync(join(repo, '.goreleaser.yaml'), 'utf8');
  assert.match(yaml, /postflight_steps do/);
  assert.match(yaml, /on_macos do/);
  assert.doesNotMatch(yaml, /^ {4}hooks:/m);
  // the template escapes that leave `{{staged_path}}` literal for Homebrew
  assert.ok(yaml.includes('{{ "{{" }}staged_path{{ "}}" }}'), 'staged_path is escaped for goreleaser');
});
