#!/usr/bin/env node
'use strict';
// The launcher of @dormeto/cli: runs the binary of the platform package npm
// installed (an optional dependency chosen by `os` and `cpu`), with the same
// arguments and standard streams, passes SIGINT, SIGTERM and SIGHUP on to it
// and ends the way it ended: with its exit code, or by the signal that killed it.

const { spawn } = require('node:child_process');
const path = require('node:path');

const PLATFORMS = { darwin: 'darwin', linux: 'linux', win32: 'win32' };
const ARCHES = { arm64: 'arm64', x64: 'x64' };

const platform = PLATFORMS[process.platform];
const arch = ARCHES[process.arch];
if (!platform || !arch) {
  console.error(`dormeto: ${process.platform}/${process.arch} is not supported (macOS, Linux and Windows on arm64 and x64 are). Download a release archive from https://github.com/Dormeto/cli/releases or build with go install github.com/Dormeto/cli/cmd/dormeto@latest.`);
  process.exit(1);
}

const pkg = `@dormeto/cli-${platform}-${arch}`;
const exe = platform === 'win32' ? 'dormeto.exe' : 'dormeto';

let binary;
try {
  binary = path.join(path.dirname(require.resolve(`${pkg}/package.json`)), 'bin', exe);
} catch {
  console.error(`dormeto: the package ${pkg}, which holds the binary for this machine, is not installed.
It is an optional dependency of @dormeto/cli, and npm leaves it out with --omit=optional (or --no-optional) or when its install failed.
Install it with: npm install ${pkg}
(or reinstall @dormeto/cli without --omit=optional).`);
  process.exit(1);
}

const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit' });

const forwarded = ['SIGINT', 'SIGTERM', 'SIGHUP'];
const forwarders = forwarded.map((signal) => {
  const forward = () => { try { child.kill(signal); } catch { /* already gone */ } };
  process.on(signal, forward);
  return [signal, forward];
});

child.on('error', (err) => {
  console.error(`dormeto: cannot run ${binary}: ${err.message}`);
  process.exit(1);
});

child.on('exit', (code, signal) => {
  for (const [name, forward] of forwarders) process.removeListener(name, forward);
  if (signal) {
    // Die of the same signal (the default action), so a shell sees it as the binary's.
    process.kill(process.pid, signal);
    // Only reached if the signal did not end this process (one with no default action): fail.
    setTimeout(() => process.exit(1), 100);
    return;
  }
  process.exit(code ?? 1);
});
