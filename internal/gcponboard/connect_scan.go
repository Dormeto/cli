package gcponboard

// What connect and disconnect read at the scopes: the projects (a project scope's own; a folder's or an organization's, from
// Resource Manager's project search and each project's ancestry, which also gives a folder scope its organization), their
// routers (the Cloud Run services dmtr-<region>-controller, listed in every location at once) with their controller's URL
// and version (its DMT_VERSION), and each router project's tag value <project>/dormeto/managed. Ten projects at a time; it
// only reads, with the permissions of the read step, tested at the scopes; the tag values once the router projects are
// tested for the tag step, since Google answers a value the caller may not read as one that does not exist.

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/Dormeto/cli/internal/fleet"
)

const (
	crmV1      = "https://cloudresourcemanager.googleapis.com/v1/"
	scanAtOnce = 10
)

// scan finds the projects at the scopes and their routers, and the projects of the record's routers that hold none any more.
// A folder scope whose organization is not found stops connect (its role would have nowhere to be defined); disconnect
// leaves that role.
func (x *link) scan() error {
	if err := x.findProjects(); err != nil {
		return err
	}
	routers, err := x.findRouters(x.projects)
	if err != nil {
		return err
	}
	x.routers = routers
	// The record's routers not found: a project left without any has its command grants taken back. One in a region Cloud
	// Run did not list is not seen, not gone: its grants and its entry stay, and this run grants nothing there.
	if x.rec != nil {
		for _, r := range x.rec.Routers {
			if slices.Contains(x.unseen[r.Project], r.Region) {
				x.unseenRouters = append(x.unseenRouters, r)
				continue
			}
			if !slices.Contains(x.routerProjects(), r.Project) && !slices.Contains(x.gone, r.Project) {
				x.gone = append(x.gone, r.Project)
			}
		}
	}
	return nil
}

// readTagValues reads, once checkRest has tested the router projects, their tag values (one missing stops connect) and those
// of the projects left without a router.
func (x *link) readTagValues() error {
	if err := x.findTagValues(true); err != nil {
		return err
	}
	for _, p := range x.gone {
		name, found, err := x.tag("tagValues", p+"/dormeto/managed")
		if err != nil {
			return err
		}
		if found {
			x.tagValue[p] = name
		}
	}
	return nil
}

// findProjects are the projects at the scopes, and the folder scopes' organizations.
func (x *link) findProjects() error {
	in := map[string]bool{}
	var wide []string
	for _, s := range x.scopes {
		if id, ok := strings.CutPrefix(s, "projects/"); ok {
			in[id] = true
		} else {
			wide = append(wide, s)
		}
	}
	if len(wide) > 0 {
		visible, err := x.searchProjects()
		if err != nil {
			return err
		}
		ancestors := make([][]string, len(visible))
		err = x.each(len(visible), func(i int) error {
			var err error
			ancestors[i], err = x.ancestry(visible[i])
			return err
		})
		if err != nil {
			return err
		}
		for i, id := range visible {
			for _, a := range ancestors[i] {
				if slices.Contains(wide, a) {
					in[id] = true
				}
				if strings.HasPrefix(a, "folders/") && slices.Contains(wide, a) {
					if org := ancestors[i][len(ancestors[i])-1]; strings.HasPrefix(org, "organizations/") {
						x.orgOf[a] = org
					}
				}
			}
		}
		for _, s := range wide {
			if strings.HasPrefix(s, "folders/") && x.orgOf[s] == "" {
				if x.command == "connect" {
					return &stop{1, fmt.Sprintf("%s holds no project %s sees, so its organization, where the role dormetoDiscovery is defined, is unknown: give its organization as a scope, or the folder once it holds a project\nNothing was changed.",
						resourceWords(s), x.account)}
				}
				x.unknownOrgs = append(x.unknownOrgs, s)
			}
		}
	}
	x.projects = nil
	for id := range in {
		x.projects = append(x.projects, id)
	}
	slices.Sort(x.projects)
	return nil
}

// searchProjects are the active projects the caller sees (Resource Manager v3's projects.search, which answers those it
// holds resourcemanager.projects.get on).
func (x *link) searchProjects() ([]string, error) {
	var out []string
	token := ""
	for {
		u := crmV3 + "projects:search?query=" + url.QueryEscape("state:ACTIVE") + "&pageSize=500"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var page struct {
			Projects []struct {
				ProjectID string `json:"projectId"`
				State     string `json:"state"`
			} `json:"projects"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := x.d.Google.Do(x.ctx, http.MethodGet, u, nil, &page); err != nil {
			return nil, fmt.Errorf("the projects are not searched: %w", err)
		}
		for _, p := range page.Projects {
			if p.State == "" || p.State == "ACTIVE" {
				out = append(out, p.ProjectID)
			}
		}
		if token = page.NextPageToken; token == "" {
			return out, nil
		}
	}
}

// ancestry are a project's folders and organization, parent first (Resource Manager v1's getAncestry, which needs
// resourcemanager.projects.get).
func (x *link) ancestry(project string) ([]string, error) {
	var out struct {
		Ancestor []struct {
			ResourceID struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"resourceId"`
		} `json:"ancestor"`
	}
	if err := x.d.Google.Do(x.ctx, http.MethodPost, crmV1+"projects/"+url.PathEscape(project)+":getAncestry", map[string]any{}, &out); err != nil {
		return nil, fmt.Errorf("the ancestry of project %s is not read: %w", project, err)
	}
	var ancestors []string
	for _, a := range out.Ancestor {
		switch a.ResourceID.Type {
		case "folder":
			ancestors = append(ancestors, "folders/"+a.ResourceID.ID)
		case "organization":
			ancestors = append(ancestors, "organizations/"+a.ResourceID.ID)
		}
	}
	return ancestors, nil
}

// findRouters are the routers of the projects, by project and region: their services dmtr-<region>-controller, the
// controller's URL and version. A project with Cloud Run off has none. A location Cloud Run did not list (a region the
// project can't use) is warned about once per project and noted in x.unseen: the run goes on without it.
func (x *link) findRouters(projects []string) ([]fleet.GoogleRouter, error) {
	found := make([][]fleet.GoogleRouter, len(projects))
	missed := make([][]string, len(projects))
	err := x.each(len(projects), func(i int) error {
		var err error
		found[i], missed[i], err = x.routersIn(projects[i])
		return err
	})
	if err != nil {
		return nil, err
	}
	var out []fleet.GoogleRouter
	for i, rs := range found {
		out = append(out, rs...)
		if len(missed[i]) > 0 {
			x.unseen[projects[i]] = missed[i]
			w := fmt.Sprintf("Cloud Run did not list the services of project %s in %s: a router there, if any, isn't seen by this run", projects[i], strings.Join(missed[i], ", "))
			x.warnings = append(x.warnings, w)
			fmt.Fprintf(x.d.Err, "  ! %s\n", w)
		}
	}
	return out, nil
}

// routersIn lists a project's routers and the regions Cloud Run did not list, sorted.
func (x *link) routersIn(project string) ([]fleet.GoogleRouter, []string, error) {
	var out []fleet.GoogleRouter
	var missed []string
	token := ""
	for {
		u := runV2 + "projects/" + url.PathEscape(project) + "/locations/-/services"
		if token != "" {
			u += "?pageToken=" + url.QueryEscape(token)
		}
		var page struct {
			Services []struct {
				Name     string `json:"name"`
				URI      string `json:"uri"`
				Template struct {
					Containers []struct {
						Env []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"template"`
			} `json:"services"`
			Unreachable   []string `json:"unreachable"`
			NextPageToken string   `json:"nextPageToken"`
		}
		err := x.d.Google.Do(x.ctx, http.MethodGet, u, nil, &page)
		switch {
		case serviceDisabled(err):
			return nil, nil, nil
		case err != nil:
			return nil, nil, fmt.Errorf("the Cloud Run services of project %s are not listed: %w", project, err)
		}
		for _, u := range page.Unreachable {
			_, region, ok := strings.Cut(u, "/locations/")
			if !ok {
				region = u
			}
			if !slices.Contains(missed, region) {
				missed = append(missed, region)
			}
		}
		for _, s := range page.Services {
			_, rest, _ := strings.Cut(s.Name, "/locations/")
			region, id, _ := strings.Cut(rest, "/services/")
			if id != "dmtr-"+region+"-controller" || !regionPattern.MatchString(region) {
				continue
			}
			r := fleet.GoogleRouter{Project: project, Region: region}
			if runAppPattern.MatchString(s.URI) {
				r.Controller = s.URI
			}
			if len(s.Template.Containers) > 0 {
				for _, e := range s.Template.Containers[0].Env {
					if e.Name == "DMT_VERSION" && corePattern.MatchString(e.Value) {
						r.Version = e.Value
					}
				}
			}
			out = append(out, r)
		}
		if token = page.NextPageToken; token == "" {
			slices.SortFunc(out, func(a, b fleet.GoogleRouter) int { return strings.Compare(a.Region, b.Region) })
			slices.Sort(missed)
			return out, missed, nil
		}
	}
}

// findTagValues reads each router project's tag value: one missing stops connect (required) with the install that creates
// it; disconnect has nothing to remove there.
func (x *link) findTagValues(required bool) error {
	for _, p := range x.commandProjects() {
		name, found, err := x.tag("tagValues", p+"/dormeto/managed")
		if err != nil {
			return err
		}
		if found {
			x.tagValue[p] = name
			continue
		}
		if required {
			region := x.routers[slices.IndexFunc(x.routers, func(r fleet.GoogleRouter) bool { return r.Project == p })].Region
			return &stop{1, fmt.Sprintf("router dmtr-%s of project %s has no tag value %s/dormeto/managed: dormeto gcp install --project %s --region %s creates it\nNothing was changed.",
				region, p, p, p, region)}
		}
	}
	return nil
}

// each runs f for 0..n-1, scanAtOnce at a time; the first error in that order is returned.
func (x *link) each(n int, f func(i int) error) error {
	errs := make([]error, n)
	slots := make(chan struct{}, scanAtOnce)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			errs[i] = f(i)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
