package cli

import (
	"os"
	"strings"
	"testing"
)

// disallowed returns the required modules of a go.mod that are not allowed.
func disallowed(gomod string) []string {
	exact := map[string]bool{
		"github.com/aws/smithy-go":             true,
		"github.com/spf13/cobra":               true,
		"github.com/spf13/pflag":               true,
		"github.com/inconshreveable/mousetrap": true,
		"golang.org/x/sync":                    true,
	}
	var bad []string
	inRequire := false
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		var path string
		switch {
		case line == "require (":
			inRequire = true
			continue
		case line == ")":
			inRequire = false
			continue
		case strings.HasPrefix(line, "require "):
			path = strings.Fields(line)[1]
		case inRequire && line != "" && !strings.HasPrefix(line, "//"):
			path = strings.Fields(line)[0]
		default:
			continue
		}
		if !exact[path] && path != "github.com/aws/aws-sdk-go-v2" && !strings.HasPrefix(path, "github.com/aws/aws-sdk-go-v2/") {
			bad = append(bad, path)
		}
	}
	return bad
}

func TestDisallowed(t *testing.T) {
	gomod := "module x\n\nrequire (\n\tgithub.com/aws/aws-sdk-go-v2/service/ec2 v1.0.0\n\tk8s.io/client-go v0.30.0 // indirect\n)\nrequire github.com/evil/mod v1.0.0\n"
	got := strings.Join(disallowed(gomod), ",")
	if got != "k8s.io/client-go,github.com/evil/mod" {
		t.Fatalf("got %q", got)
	}
}

func TestGoModHoldsOnlyTheAllowedModules(t *testing.T) {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range disallowed(string(b)) {
		t.Errorf("go.mod requires %s, which is not an allowed module", m)
	}
}
