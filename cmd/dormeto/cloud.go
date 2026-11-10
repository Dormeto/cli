package main

// --cloud (aws or gcp) on the manager's listings that print both clouds, and --project, the alias of --account on the
// manager's commands that take it (a Google Cloud project is the account of its rows).

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Dormeto/cli/internal/fleet"
)

// cloudFilter is --cloud: "" for both clouds, else aws or gcp.
type cloudFilter string

func (c *cloudFilter) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar((*string)(c), "cloud", "", "only the rows of this cloud: aws or gcp (default: both)")
}

// check refuses a value other than aws or gcp, before any call.
func (c cloudFilter) check() error {
	if c != "" && c != "aws" && c != "gcp" {
		return usagef("--cloud: %q is not aws or gcp", string(c))
	}
	return nil
}

// keeps tells whether a row of that cloud (empty: AWS, as a manager before Google Cloud's) is kept.
func (c cloudFilter) keeps(cloud string) bool { return c == "" || string(c) == fleet.CloudName(cloud) }

// onlyCloud is the rows of one cloud, a new slice.
func onlyCloud[T any](c cloudFilter, rows []T, cloud func(T) string) []T {
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		if c.keeps(cloud(r)) {
			out = append(out, r)
		}
	}
	return out
}

// filterWorkloadsJSON is the manager's workloads answer with the rows of one cloud, its total and counts per state
// those of the rows kept; every other field is passed as it was.
func filterWorkloadsJSON(raw json.RawMessage, c cloudFilter) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	var rows []fleet.Workload
	if err := json.Unmarshal(m["workloads"], &rows); err != nil {
		return nil, err
	}
	var all []json.RawMessage
	if err := json.Unmarshal(m["workloads"], &all); err != nil {
		return nil, err
	}
	kept := []json.RawMessage{}
	states := map[string]int{}
	for i, r := range rows {
		if c.keeps(r.Cloud) {
			kept = append(kept, all[i])
			states[r.State]++
		}
	}
	m["workloads"], _ = json.Marshal(kept)
	m["total"], _ = json.Marshal(len(kept))
	if _, ok := m["byState"]; ok {
		m["byState"], _ = json.Marshal(states)
	}
	return json.Marshal(m)
}

// aliasProject gives --project to the commands below top that take --account, the same flag under another name; the
// commands in except (by path under top) take an AWS account alone.
func aliasProject(top *cobra.Command, except ...string) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		if account := c.Flags().Lookup("account"); account != nil && c.Flags().Lookup("project") == nil && !slices.Contains(except, path) {
			c.Flags().AddFlag(&pflag.Flag{Name: "project", Usage: "an alias of --account: a Google Cloud project", Value: account.Value, DefValue: account.DefValue})
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(top, top.Name())
}

// bothCloudsSentence is what a manager command's help says of the two clouds (design C22): the manager serves AWS and
// Google Cloud, and what the command covers of them.
func bothCloudsSentence(covers string) string {
	return "The manager serves AWS and Google Cloud: " + covers
}

// sayBothClouds adds to the help of the manager's commands that cover both clouds a sentence saying so: the routers'
// reads, every workloads command (a target is <router>/<workload> on either cloud), discovery's candidates and access,
// settings, types and savings. A command whose help already names Google Cloud is left as it is.
func sayBothClouds(top *cobra.Command) {
	covers := map[string]string{
		"routers list": "this lists the routers of both clouds.", "routers show": "this shows an AWS or a Google Cloud router.",
		"discovery candidates": "this lists the candidates of both clouds.", "discovery access": "this reports the accounts and projects of both clouds.",
		"settings": "these defaults are for the workloads of both clouds.", "types": "these types are for the workloads of both clouds.",
		"savings": "this estimates the savings of the workloads of both clouds.",
	}
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		subs := c.Commands()
		if len(subs) == 0 || path == "settings" || path == "types" || path == "savings" {
			what := covers[path]
			if what == "" && strings.HasPrefix(path, "workloads ") {
				what = "this acts on the workloads of both clouds, a target being <router>/<workload>."
			}
			if what != "" && !strings.Contains(c.Short+c.Long, "Google Cloud") {
				long := c.Long
				if long == "" {
					long = c.Short + "."
				}
				c.Long = long + "\n\n" + bothCloudsSentence(what)
			}
		}
		for _, sub := range subs {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(top, top.Name())
}
