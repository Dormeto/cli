package gcponboard

// `dormeto gcp whoami`: the Google account the calls are made as and the project they reach. It reads what `gcp check`
// reads first (the account, the project through Cloud Resource Manager) and nothing else, so it needs no permission
// beyond the manifest's `check` command. The project's routers are not listed: reading them takes Firestore and
// Cloud Run reads that `check` does not test.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/output"
)

// activeConfiguration is what whoami says when no --gcloud-configuration is given: gcloud's active one.
const activeConfiguration = "(active)"

// WhoamiReport is `gcp whoami --json`.
type WhoamiReport struct {
	Account       string      `json:"account"`
	Configuration string      `json:"configuration"`
	Project       ProjectInfo `json:"project"`
}

// RunWhoami is `dormeto gcp whoami`: 0, or 1 when the project is not visible to the account or Google refuses.
func RunWhoami(ctx context.Context, d Deps, r Request) int {
	rep, err := whoami(ctx, d, r)
	if err == nil {
		if r.JSON {
			err = output.JSON(d.Out, rep)
		} else {
			_, err = fmt.Fprint(d.Out, fleet.Labelled([][2]string{
				{"account", rep.Account}, {"configuration", rep.Configuration}, {"project", rep.Project.ID},
				{"number", rep.Project.Number}, {"parent", parentWords(rep.Project.Parent)},
			}))
		}
	}
	if err != nil {
		fmt.Fprintln(d.Err, err)
		return 1
	}
	return 0
}

func whoami(ctx context.Context, d Deps, r Request) (WhoamiReport, error) {
	account, err := d.Google.Tokens.Account(ctx)
	if err != nil {
		return WhoamiReport{}, err
	}
	var p projectBody
	if err := d.Google.Do(ctx, http.MethodGet, crm+url.PathEscape(r.Project), nil, &p); err != nil {
		if denied(err) {
			return WhoamiReport{}, fmt.Errorf("Project %s is not visible to %s: no role in it", r.Project, account)
		}
		return WhoamiReport{}, err
	}
	configuration := r.Configuration
	if configuration == "" {
		configuration = activeConfiguration
	}
	return WhoamiReport{Account: account, Configuration: configuration,
		Project: ProjectInfo{ID: r.Project, Number: strings.TrimPrefix(p.Name, "projects/"), Parent: p.Parent}}, nil
}

// parentWords is a project's parent as whoami says it: `organization 42`, `folder 7`, or `none`.
func parentWords(parent string) string {
	switch kind, n, _ := strings.Cut(parent, "/"); kind {
	case "organizations":
		return "organization " + n
	case "folders":
		return "folder " + n
	}
	return "none"
}
