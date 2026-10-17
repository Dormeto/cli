package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scripts run under sh with fake aws, terraform, curl, sha256sum and unzip first on PATH, each logging its
// arguments to $FAKE/log. Nothing leaves the machine.

const fakeLib = `
# names the release publishes: a line "good  name" per file, "bad" when FAKE_BAD names it
sums() {
  for n in "terraform_${TF_VERSION:-0}_linux_amd64.zip" "terraform-aws-dormeto-${RELEASE_VERSION:-0}.zip" "dormeto-access-commands-${RELEASE_VERSION:-0}.sh"; do
    case " ${FAKE_BAD:-} " in *" $n "*) echo "bad  $n";; *) echo "good  $n";; esac
  done
}
`

var fakeTools = map[string]string{
	"aws": `#!/bin/sh
. "$FAKEBIN/lib.sh"
echo "aws $*" >> "$FAKE/log"
case "$1 $2" in
"s3 cp")
  shift 2
  while [ "${1#--}" != "$1" ]; do shift; done
  src=$1; dst=$2
  case $src in
  s3://*)
    f="$FAKE/s3/${src#s3://}"
    if [ -f "$f" ]; then cp "$f" "$dst"
    else
      case $src in
      */dormeto-access-commands-*) cp "$FAKE/access.sh" "$dst" ;;
      */SHA256SUMS) sums > "$dst" ;;
      *) echo "fake $src" > "$dst" ;;
      esac
    fi ;;
  *)
    f="$FAKE/s3/${dst#s3://}"; mkdir -p "$(dirname "$f")"; cp "$src" "$f" ;;
  esac ;;
"s3api head-object")
  b=; k=
  while [ $# -gt 0 ]; do case $1 in --bucket) b=$2 ;; --key) k=$2 ;; esac; shift; done
  if [ -f "$FAKE/s3/$b/$k" ]; then echo '{}'
  elif [ -n "${FAKE_HEAD_ERR:-}" ]; then echo "An error occurred (403) when calling the HeadObject operation: Forbidden" >&2; exit 254
  else echo "An error occurred (404) when calling the HeadObject operation: Not Found" >&2; exit 254; fi ;;
"s3 rm") ;;
"organizations describe-resource-policy")
  if [ -f "$FAKE/policy" ]; then cat "$FAKE/policy"
  else echo "An error occurred (ResourcePolicyNotFoundException) when calling the DescribeResourcePolicy operation" >&2; exit 254; fi ;;
"organizations put-resource-policy") ;;
"sts assume-role")
  arn=
  while [ $# -gt 0 ]; do [ "$1" = --role-arn ] && arn=$2; shift; done
  a=${arn#arn:aws:iam::}; a=${a%%:*}
  case " ${FAKE_ASSUME_FAIL:-} " in
  *" $a "*) echo "An error occurred (AccessDenied) when calling the AssumeRole operation: not \"allowed\"" >&2; exit 254 ;;
  esac
  printf 'AK%s\tSK\tTK\n' "$a" ;;
*) echo "fake aws: unexpected $*" >&2; exit 99 ;;
esac
`,
	"terraform": `#!/bin/sh
echo "terraform $*" >> "$FAKE/log"
echo "env CHECKPOINT_DISABLE=${CHECKPOINT_DISABLE:-} TF_IN_AUTOMATION=${TF_IN_AUTOMATION:-} TF_CLI_ARGS=${TF_CLI_ARGS:-}" >> "$FAKE/log"
case $1 in
init) [ -f .terraform.lock.hcl ] || echo lock-from-init > .terraform.lock.hcl ;;
plan) for a in "$@"; do case $a in -out=*) echo plan > "${a#-out=}" ;; esac; done ;;
show) echo '{"plan":true}' ;;
output) echo '{"o":1}' ;;
esac
`,
	"curl": `#!/bin/sh
. "$FAKEBIN/lib.sh"
echo "curl $*" >> "$FAKE/log"
out=; url=
while [ $# -gt 0 ]; do
  case $1 in -o) out=$2; shift ;; -*) ;; *) url=$1 ;; esac
  shift
done
case $url in
*SHA256SUMS) sums > "$out" ;;
*/dormeto-access-commands-*) cp "$FAKE/access.sh" "$out" ;;
*) echo "fake $url" > "$out" ;;
esac
`,
	"sha256sum": `#!/bin/sh
echo "sha256sum $*" >> "$FAKE/log"
[ "$1" = -c ] || exit 98
while read -r h n; do
  if [ "$h" = bad ]; then echo "$n: FAILED"; exit 1; fi
  echo "$n: OK"
done < "$2"
`,
	"unzip": `#!/bin/sh
echo "unzip $*" >> "$FAKE/log"
dir=
while [ $# -gt 0 ]; do [ "$1" = -d ] && dir=$2; shift; done
mkdir -p "$dir"
printf '#!/bin/sh\necho terraform-from-bin >> "$FAKE/log"\nexec "$FAKEBIN/terraform" "$@"\n' > "$dir/terraform"
chmod +x "$dir/terraform"
`,
}

const fakeAccess = `echo "access ACCOUNT=$ACCOUNT MANAGER_ACCOUNT=$MANAGER_ACCOUNT ENVS=$ENVS ORGANIZATION=$ORGANIZATION DISCOVERY_ROLE=${DISCOVERY_ROLE-unset} COMMAND_ROLE=${COMMAND_ROLE-unset} KEY=$AWS_ACCESS_KEY_ID" >> "$FAKE/log"
case " ${FAKE_ACCESS_FAIL:-} " in
*" $ACCOUNT "*) echo "line one"; echo 'boom "quoted" \ here' >&2; exit 1 ;;
esac
echo done
`

type sandbox struct {
	t               *testing.T
	fake, bin, work string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	root := t.TempDir()
	s := &sandbox{t: t, fake: filepath.Join(root, "fake"), bin: filepath.Join(root, "fake", "bin"), work: filepath.Join(root, "work")}
	for _, d := range []string{s.bin, s.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.write(filepath.Join(s.bin, "lib.sh"), fakeLib, 0o644)
	for name, body := range fakeTools {
		s.write(filepath.Join(s.bin, name), body, 0o755)
	}
	s.write(filepath.Join(s.fake, "access.sh"), fakeAccess, 0o644)
	return s
}

func (s *sandbox) write(path, body string, mode os.FileMode) {
	s.t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		s.t.Fatal(err)
	}
}

// run runs the embedded script under sh in the work directory; env adds to a minimal environment.
func (s *sandbox) run(script string, env map[string]string) (string, error) {
	s.t.Helper()
	path := filepath.Join(s.work, "run.sh")
	s.write(path, string(Script(script)), 0o644)
	cmd := exec.Command(shellPath(), path)
	cmd.Dir = s.work
	cmd.Env = []string{"PATH=" + s.bin + ":/usr/bin:/bin", "FAKE=" + s.fake, "FAKEBIN=" + s.bin, "HOME=" + s.work}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *sandbox) log() []string {
	b, _ := os.ReadFile(filepath.Join(s.fake, "log"))
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// uploaded is what the script put in the fake bucket at key (bucket/key).
func (s *sandbox) uploaded(key string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(s.fake, "s3", key))
	return string(b), err == nil
}

// index is the position of the first log line with the prefix, or -1.
func index(lines []string, prefix string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func has(lines []string, prefix string) bool { return index(lines, prefix) >= 0 }

func (s *sandbox) write2(path, body string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	s.write(path, body, 0o644)
}

// shellPath is the shell the scripts run under: DASH=1 forces dash (CodeBuild's sh is a strict POSIX shell).
func shellPath() string {
	if p, err := exec.LookPath("dash"); err == nil && os.Getenv("DASH") != "" {
		return p
	}
	return "/bin/sh"
}
