package gcponboard

import (
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/access"
)

// Every custom role of the manifest, the router's and the manager's, is written with a description Google takes (256
// characters at most), whatever the CLI's version: "Dormeto <version>: " before the manifest's own when it fits, the
// version's core when a dev build's is too long, the manifest's alone when even that would pass the limit.
func TestEveryRolesDescriptionFitsGooglesLimit(t *testing.T) {
	m := access.Load()
	descriptions := map[string]string{}
	for id, r := range m.Manager.Roles {
		descriptions["manager "+id] = r.Description
	}
	for id := range m.Roles {
		descriptions["router "+id] = roleDef(id).Description
	}
	for _, version := range []string{"0.14.0", "10.20.30", "0.14.1-0.20261109143000-d3ca66f12345+dirty"} {
		for id, d := range descriptions {
			got := roleDescription(version, d)
			if len(got) > maxRoleDescription || !strings.HasSuffix(got, d) {
				t.Errorf("%s at %s: %d characters: %q", id, version, len(got), got)
			}
		}
	}
	if got := roleDescription("0.14.0", "Short."); got != "Dormeto 0.14.0: Short." {
		t.Errorf("%q", got)
	}
}
