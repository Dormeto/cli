#!/usr/bin/env node
// Builds the seven npm packages of a release in dist/npm/ from GoReleaser's dist/:
// cli/ (the launcher, @dormeto/cli) and cli-<platform>-<arch>/ (a binary each).
// The version is dist/metadata.json's; the binaries are the "Binary" entries of dist/artifacts.json.
//
//   node npm/assemble.mjs [--dist dist] [--out dist/npm]
import { chmodSync, copyFileSync, cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = dirname(here);

// Go's (goos, goarch) -> Node's (platform, arch).
const PLATFORMS = [
  ['darwin', 'arm64', 'darwin', 'arm64'],
  ['darwin', 'amd64', 'darwin', 'x64'],
  ['linux', 'arm64', 'linux', 'arm64'],
  ['linux', 'amd64', 'linux', 'x64'],
  ['windows', 'arm64', 'win32', 'arm64'],
  ['windows', 'amd64', 'win32', 'x64'],
];

function fail(message) {
  console.error(`assemble: ${message}`);
  process.exit(1);
}

function option(name, fallback) {
  const args = process.argv.slice(2);
  const at = args.indexOf(`--${name}`);
  if (at < 0) return fallback;
  if (at + 1 >= args.length) fail(`--${name} needs a value`);
  return args[at + 1];
}

function readJSON(file) {
  if (!existsSync(file)) fail(`${file} not found (run goreleaser first)`);
  return JSON.parse(readFileSync(file, 'utf8'));
}

const dist = resolve(option('dist', 'dist'));
const out = resolve(option('out', join(dist, 'npm')));

const version = readJSON(join(dist, 'metadata.json')).version;
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version ?? '')) fail(`${join(dist, 'metadata.json')} has no valid version: ${JSON.stringify(version)}`);
const artifacts = readJSON(join(dist, 'artifacts.json'));
// artifacts.json paths are relative to the directory GoReleaser ran in, the one that holds dist/.
const base = dirname(dist);

const binaries = new Map();
for (const artifact of artifacts) {
  if (artifact.type === 'Binary') binaries.set(`${artifact.goos}/${artifact.goarch}`, resolve(base, artifact.path));
}

for (const [goos, goarch] of PLATFORMS) {
  const file = binaries.get(`${goos}/${goarch}`);
  if (!file || !existsSync(file)) fail(`no binary for ${goos}/${goarch} in ${dist}/artifacts.json`);
}

rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

const template = readFileSync(join(here, 'platform', 'package.json.tmpl'), 'utf8');
const optionalDependencies = {};

for (const [goos, goarch, platform, arch] of PLATFORMS) {
  const exe = platform === 'win32' ? 'dormeto.exe' : 'dormeto';
  const name = `cli-${platform}-${arch}`;
  const dir = join(out, name);
  mkdirSync(join(dir, 'bin'), { recursive: true });
  const manifest = template.replaceAll('{{platform}}', platform).replaceAll('{{arch}}', arch).replaceAll('{{version}}', version).replaceAll('{{exe}}', exe);
  writeFileSync(join(dir, 'package.json'), `${JSON.stringify(JSON.parse(manifest), null, 2)}\n`);
  copyFileSync(binaries.get(`${goos}/${goarch}`), join(dir, 'bin', exe));
  chmodSync(join(dir, 'bin', exe), 0o755);
  copyFileSync(join(repo, 'LICENSE'), join(dir, 'LICENSE'));
  optionalDependencies[`@dormeto/${name}`] = version;
}

const main = readJSON(join(here, 'cli', 'package.json'));
main.version = version;
main.optionalDependencies = optionalDependencies;
const cli = join(out, 'cli');
mkdirSync(join(cli, 'bin'), { recursive: true });
writeFileSync(join(cli, 'package.json'), `${JSON.stringify(main, null, 2)}\n`);
copyFileSync(join(here, 'cli', 'bin', 'dormeto.js'), join(cli, 'bin', 'dormeto.js'));
chmodSync(join(cli, 'bin', 'dormeto.js'), 0o755);
copyFileSync(join(repo, 'LICENSE'), join(cli, 'LICENSE'));
if (existsSync(join(here, 'cli', 'README.md'))) cpSync(join(here, 'cli', 'README.md'), join(cli, 'README.md'));

console.log(`assemble: 7 packages of ${version} in ${out}`);
