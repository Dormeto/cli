#!/usr/bin/env bash
# An opt-in check that the Homebrew cask installs and runs on Linux (Ruling P19), from the Mac; neither go test nor
# npm test runs it:
#
#   scripts/test-brew-linux.sh [arm64] [amd64]     (default: this machine's architecture)
#
# It builds a GoReleaser snapshot (dist/; nothing published, no token) and checks that the cask's two Linux entries
# name the snapshot's Linux archives with their checksums.txt SHA-256. It serves dist/ over HTTP on 127.0.0.1 (a podman
# machine's containers reach it as host.containers.internal) and, for each architecture, runs a docker.io/homebrew/brew
# container (podman) that makes a local tap from the generated cask, its URLs pointed at that server, installs it with
# `brew install --cask` and runs `dormeto version`, which must say the snapshot's version. The containers and the
# server are removed on every exit; a container still running after DORMETO_BREW_TIMEOUT seconds (900) fails.
#
# The other architecture runs under emulation: on an ARM Mac (podman machine, qemu-user), Homebrew 4.6's forked child
# hangs in amd64 (its threads wait on a futex forever), so amd64 is checked natively, on an Intel machine.
#
# Overridable for the tests: DORMETO_PODMAN, DORMETO_GORELEASER, DORMETO_BREW_IMAGE, DORMETO_BREW_TIMEOUT.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
podman="${DORMETO_PODMAN:-podman}"
goreleaser="${DORMETO_GORELEASER:-goreleaser}"
image="${DORMETO_BREW_IMAGE:-docker.io/homebrew/brew:latest}"
timeout="${DORMETO_BREW_TIMEOUT:-900}"

fail() { echo "test-brew-linux: $*" >&2; exit 1; }

arches=("$@")
if [ "${#arches[@]}" -eq 0 ]; then
  case "$(uname -m)" in arm64 | aarch64) arches=(arm64) ;; *) arches=(amd64) ;; esac
fi
for arch in "${arches[@]}"; do
  case "$arch" in arm64 | amd64) ;; *) fail "usage: scripts/test-brew-linux.sh [arm64] [amd64]" ;; esac
done

# The server, the containers and the temporary files: removed on every exit.
scratch=""
server=""
runner=""
containers=()
cleanup() {
  local status=$? name
  for name in "${containers[@]+"${containers[@]}"}"; do "$podman" rm -f "$name" >/dev/null 2>&1 || true; done
  [ -z "$runner" ] || kill "$runner" 2>/dev/null || true
  if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true; fi
  [ -z "$scratch" ] || rm -rf "$scratch"
  return "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
scratch="$(mktemp -d "${TMPDIR:-/tmp}/dormeto-brew-linux.XXXXXX")"

cd "$root"
echo "test-brew-linux: the snapshot"
env -u GITHUB_TOKEN "$goreleaser" release --snapshot --clean
cask="$root/dist/homebrew/Casks/dormeto.rb"
[ -f "$cask" ] || fail "GoReleaser wrote no cask at $cask"
version="$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$root/dist/metadata.json")"

# The cask's Linux entries (on_linux: on_arm, on_intel): the snapshot's archives, with their checksums.txt SHA-256.
python3 - "$cask" "$root/dist/checksums.txt" "$version" <<'PY' || fail "the cask's Linux entries do not match dist/checksums.txt (above)"
import re, sys
cask, sums, version = open(sys.argv[1]).read(), open(sys.argv[2]).read().split(), sys.argv[3]
checksums = dict(zip(sums[1::2], sums[0::2]))
linux = cask[cask.index("on_linux do"):]
for block, arch in (("on_arm do", "arm64"), ("on_intel do", "amd64")):
    entry = linux[linux.index(block):]
    sha = re.search(r'sha256 "([0-9a-f]{64})"', entry).group(1)
    url = re.search(r'url "([^"]+)"', entry).group(1).replace("#{version}", version)
    archive = f"dormeto_{version}_linux_{arch}.tar.gz"
    if not url.endswith("/" + archive) or checksums.get(archive) != sha:
        sys.exit(f"linux/{arch}: {url} {sha}, not {archive} {checksums.get(archive)}")
    print(f"test-brew-linux: the cask's linux/{arch} entry: {archive}, sha256 {sha}")
PY

# dist/ on a free port of 127.0.0.1, the port written once it listens.
python3 -u - "$root/dist" "$scratch/port" > "$scratch/server.log" 2>&1 <<'PY' &
import functools, http.server, os, sys
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=sys.argv[1])
with http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler) as httpd:
    with open(sys.argv[2] + ".new", "w") as f:
        f.write(str(httpd.server_address[1]))
    os.rename(sys.argv[2] + ".new", sys.argv[2])
    httpd.serve_forever()
PY
server=$!
for _ in $(seq 50); do [ -s "$scratch/port" ] && break; sleep 0.1; done
[ -s "$scratch/port" ] || fail "the server did not start: $(cat "$scratch/server.log")"
port="$(cat "$scratch/port")"
echo "test-brew-linux: dist/ served on 127.0.0.1:$port"

# In the container: a local tap holding the cask, its archives' URLs on the server; brew install --cask; dormeto. The
# tap is a directory where Homebrew looks for taps (brew tap-new is not needed).
inside='set -euo pipefail
uname -m
brew --version
tap="$(brew --repository)/Library/Taps/dormeto/homebrew-local"
mkdir -p "$tap/Casks"
sed -E "s#https://github.com/Dormeto/cli/releases/download/v[^/]+/#$DORMETO_SERVER/#" /cask/dormeto.rb > "$tap/Casks/dormeto.rb"
brew install --cask dormeto/local/dormeto
echo "linked: $(command -v dormeto)"
echo "version: $("$(brew --prefix)/bin/dormeto" version | sed -n 1p)"'

for arch in "${arches[@]}"; do
  name="dormeto-brew-linux-$$-$arch"
  containers+=("$name")
  echo "test-brew-linux: linux/$arch in $image"
  # In the background, waited for: a signal or the timeout stops the check at once, and cleanup removes the container.
  (
    "$podman" run --rm --name "$name" --platform "linux/$arch" -v "$root/dist/homebrew/Casks:/cask:ro" \
      -e HOMEBREW_NO_AUTO_UPDATE=1 -e HOMEBREW_NO_ANALYTICS=1 -e HOMEBREW_NO_ENV_HINTS=1 \
      -e DORMETO_SERVER="http://host.containers.internal:$port" \
      "$image" bash -c "$inside" 2>&1 | tee "$scratch/$arch.log"
  ) &
  runner=$!
  waited=0
  while kill -0 "$runner" 2>/dev/null; do
    [ "$waited" -lt "$timeout" ] || fail "linux/$arch: still running after ${timeout}s (DORMETO_BREW_TIMEOUT)"
    sleep 1
    waited=$((waited + 1))
  done
  wait "$runner" || fail "linux/$arch: brew install --cask failed (above)"
  runner=""
  grep -qx "version: dormeto $version" "$scratch/$arch.log" || fail "linux/$arch: dormeto version did not say 'dormeto $version'"
  echo "test-brew-linux: linux/$arch: the cask installs and dormeto $version runs"
done
echo "test-brew-linux: Homebrew on Linux works (${arches[*]})"
