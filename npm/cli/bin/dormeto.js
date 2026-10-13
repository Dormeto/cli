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

// Who gets which signal:
// - On a terminal, Ctrl-C reaches the whole foreground process group, the binary included, so the launcher must
//   not send SIGINT again: the binary stops gracefully on the first one and a second would kill it outright.
//   Without a terminal (a script, CI, a supervisor signalling the launcher alone) nothing else would tell it.
//   DORMETO_LAUNCHER_TTY=1|0 overrides the detection (for the tests).
// - On Windows, child.kill is a TerminateProcess and Ctrl-C already reaches both from the console: forward
//   nothing, and keep a listener so Node does not exit before the binary and ends with its exit code.
// - SIGTERM and SIGHUP are sent by kill(1) and by a closing terminal to this process alone: always forwarded (POSIX).
const override = process.env.DORMETO_LAUNCHER_TTY;
const terminal = override === undefined
  ? Boolean(process.stdin.isTTY || process.stdout.isTTY || process.stderr.isTTY)
  : override === '1';
const windows = process.platform === 'win32';
const forwarded = windows ? [] : ['SIGTERM', 'SIGHUP'].concat(terminal ? [] : ['SIGINT']);
const noop = () => {};
const forwarders = forwarded.map((signal) => {
  const forward = () => { try { child.kill(signal); } catch { /* already gone */ } };
  process.on(signal, forward);
  return [signal, forward];
});
if (!forwarded.includes('SIGINT')) {
  process.on('SIGINT', noop); // the binary has it (or Windows' console gave it): wait for the binary
  forwarders.push(['SIGINT', noop]);
}

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
