package build

import (
	"strings"
	"testing"
)

func TestBuildspec(t *testing.T) {
	b := Buildspec()
	for _, want := range []string{"version: 0.2", `aws s3 cp "s3://$BUCKET/$ENV/build/$SCRIPT" ./run.sh`, "sh ./run.sh"} {
		if !strings.Contains(b, want) {
			t.Errorf("buildspec lacks %q:\n%s", want, b)
		}
	}
	if strings.Count(b, "phases:") != 1 || strings.Contains(b, "install:") || strings.Contains(b, "pre_build") {
		t.Errorf("one build phase only:\n%s", b)
	}
}

func TestScriptsAreEmbeddedAndStrict(t *testing.T) {
	for _, n := range []string{"manager.sh", "organization.sh"} {
		s := string(Script(n))
		if !strings.Contains(s, "\nset -eu\n") {
			t.Errorf("%s lacks set -eu", n)
		}
		if strings.Contains(s, "auto-approve") && n == "manager.sh" && strings.Contains(strings.ReplaceAll(s, "never -auto-approve", ""), "-auto-approve") {
			t.Errorf("%s uses -auto-approve", n)
		}
	}
}
