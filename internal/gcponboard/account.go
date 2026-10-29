package gcponboard

// The serviceAccount step: the router's identity, dmtr-<region>@<project>.iam.gserviceaccount.com, created when
// missing; and, last of all, the serviceAccountBinding step: roles/iam.serviceAccountUser on that account to itself, so
// that it signs its tasks' OIDC tokens as itself (the serviceAccount step's permissions).

import (
	"fmt"
	"net/http"
)

func (x *run) accountURL() string {
	return "https://iam.googleapis.com/v1/projects/" + esc(x.n.project) + "/serviceAccounts/" + esc(x.n.email)
}

func (x *run) accountName() string {
	return "projects/" + x.n.project + "/serviceAccounts/" + x.n.email
}

func (x *run) planServiceAccount() (*step, error) {
	var got struct {
		Disabled bool `json:"disabled"`
	}
	found, err := x.get(iamAPI, x.accountURL(), &got)
	if err != nil {
		return nil, err
	}
	if found && got.Disabled {
		// No step enables it: changing an account's state is the owner's call, said with the command to run.
		enable := "gcloud iam service-accounts enable " + x.n.email
		if x.r.Configuration != "" {
			enable += " --configuration " + x.r.Configuration
		}
		return nil, &stop{1, fmt.Sprintf("The service account %s is disabled: enable it (%s), then run the same command again.\nNothing was changed.", x.n.email, enable)}
	}
	if found {
		x.accountExists = true
		x.found("serviceAccount", x.accountName())
		return nil, nil
	}
	return &step{name: "serviceAccount", lines: []string{"+ service account " + x.n.email}, apply: x.createAccount}, nil
}

func (x *run) createAccount() (string, error) {
	in := map[string]any{"accountId": x.n.router, "serviceAccount": map[string]any{
		"displayName": "Dormeto router " + x.n.region,
		"description": "Dormeto router " + x.n.router + ", created by dormeto gcp install",
	}}
	if err := x.call(http.MethodPost, "https://iam.googleapis.com/v1/projects/"+esc(x.n.project)+"/serviceAccounts", in, nil); err != nil {
		return "", err
	}
	x.accountExists = true
	x.rec.addResource("serviceAccount", x.accountName())
	return "created " + x.n.email, nil
}

func (x *run) planAccountBinding() (*step, error) {
	g := x.grantOn("serviceAccount")
	rp := ServiceAccountPolicy(x.n.project, x.n.email)
	if x.accountExists {
		p, err := read(x.ctx, x.d.Google, rp)
		if err != nil {
			return nil, err
		}
		if holds(p, g.Grant) {
			x.foundGrant(g)
			return nil, nil
		}
	}
	return &step{name: "serviceAccountBinding", lines: []string{"+ grant " + x.describe(g.Grant, g.On)}, apply: func() (string, error) {
		if err := x.bind(rp, g); err != nil {
			return "", err
		}
		return "granted " + g.Role + " on " + x.n.email, nil
	}}, nil
}
