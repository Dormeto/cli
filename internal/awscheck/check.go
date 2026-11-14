// Package awscheck is `dormeto aws check`: which permissions the profile lacks for each `dormeto aws` method, from IAM's
// simulation of its policies (iam:SimulatePrincipalPolicy). A caller of the manager's API is an administrator in the
// manager, so IAM is its only limit: apigateway:GET to find the API, execute-api:Invoke on the routes a method calls
// (fleet.Routes), and the install's and the estimate's own actions. It reads; it never writes.
package awscheck

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	dmtaws "github.com/Dormeto/cli/internal/aws"
	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/install"
	"github.com/Dormeto/cli/internal/output"
)

// IAM is the iam client's calls the check makes: the simulation, and the account's alias.
type IAM interface {
	install.Simulator
	ListAccountAliases(ctx context.Context, in *iam.ListAccountAliasesInput, optFns ...func(*iam.Options)) (*iam.ListAccountAliasesOutput, error)
}

// STS is the caller's identity.
type STS interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// Manager is the manager the command found: its API (its name when found by name, else the URL given), the API's id
// ("" when the URL is not an execute-api one) and region, its version ("" when it could not be read, VersionErr says
// why), and whether it was found by name, which needs apigateway:GET.
type Manager struct {
	API, APIID, Region, Version string
	VersionErr                  error
	ByName                      bool
}

// Request is what the check is about: the environment and region searched, and the manager found (nil: none, NoManager
// says why: no API of that name, or LookupRefused, the lookup refused).
type Request struct {
	Env, Region   string
	Manager       *Manager
	LookupRefused bool
}

// Invoke is the permission of a route.
const Invoke = "execute-api:Invoke"

const getAPIs = "apigateway:GET"

// CommandReport is the verdict for one method: what it needs, held or missing; Checked false when IAM's simulation could
// not tell (a warning says why).
type CommandReport struct {
	Command string   `json:"command"`
	Checked bool     `json:"checked"`
	Held    []string `json:"held"`
	Missing []string `json:"missing"`
}

// OK tells a method not known to lack a permission.
func (c CommandReport) OK() bool { return len(c.Missing) == 0 }

// ManagerReport is the manager as the report names it.
type ManagerReport struct {
	API     string `json:"api"`
	Version string `json:"version"`
}

// Report is the result of Check.
type Report struct {
	Account  string          `json:"account"`
	Caller   string          `json:"caller"`
	Manager  *ManagerReport  `json:"manager"`
	Commands []CommandReport `json:"commands"`
	Warnings []string        `json:"warnings"`

	alias string
}

// OK tells that no method lacks a permission.
func (r Report) OK() bool {
	return !slices.ContainsFunc(r.Commands, func(c CommandReport) bool { return !c.OK() })
}

// EstimateActions are the estimate's read actions: the adapters' operations (internal/aws.ReadOnlyOperations) as IAM
// names them.
func EstimateActions() []string {
	var out []string
	for _, op := range dmtaws.ReadOnlyOperations {
		service, name, _ := strings.Cut(op, ".")
		if service == "elasticloadbalancingv2" {
			service = "elasticloadbalancing"
		}
		out = append(out, service+":"+name)
	}
	return out
}

// need is one permission of a method: an action on a resource, said as text.
type need struct {
	action, resource, text string
}

// needs are each method's permissions, by its name.
func needs(r Request, partition, account string) map[string][]need {
	out := map[string][]need{}
	for _, m := range []string{"install", "uninstall"} {
		for _, a := range install.RightsActions() {
			out[m] = append(out[m], need{a, "*", a})
		}
	}
	for _, a := range EstimateActions() {
		out["estimate"] = append(out["estimate"], need{a, "*", a})
	}
	switch m := r.Manager; {
	case r.LookupRefused:
		for method := range fleet.Routes {
			out[method] = []need{{getAPIs, "*", getAPIs}}
		}
	case m != nil:
		for method, routes := range fleet.Routes {
			var ns []need
			if m.ByName {
				ns = append(ns, need{getAPIs, "*", getAPIs})
			}
			for _, rt := range routes {
				ns = append(ns, need{Invoke, rt.ARN(partition, m.Region, account, orAny(m.APIID)), Invoke + " on " + rt.String()})
			}
			out[method] = ns
		}
	}
	return out
}

func orAny(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

// partitionOf is an ARN's partition (aws, aws-cn…), aws when it has none.
func partitionOf(arn string) string {
	if parts := strings.SplitN(arn, ":", 3); len(parts) == 3 && parts[0] == "arn" && parts[1] != "" {
		return parts[1]
	}
	return "aws"
}

// Check tells, per method, the permissions the caller lacks. A simulation IAM refuses is a report (every method not
// checked, a warning why), not an error; the caller's identity that cannot be read is one.
func Check(ctx context.Context, i IAM, s STS, r Request) (Report, error) {
	id, err := s.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Report{}, err
	}
	rep := Report{Account: aws.ToString(id.Account), Caller: aws.ToString(id.Arn), Warnings: []string{}}
	if out, err := i.ListAccountAliases(ctx, &iam.ListAccountAliasesInput{}); err == nil && len(out.AccountAliases) > 0 {
		rep.alias = out.AccountAliases[0]
	}
	if m := r.Manager; m != nil {
		rep.Manager = &ManagerReport{API: m.API, Version: m.Version}
		if m.VersionErr != nil {
			rep.Warnings = append(rep.Warnings, "the manager's version is not read: "+m.VersionErr.Error())
		}
	}
	all := needs(r, partitionOf(rep.Caller), rep.Account)
	var methods []string
	for m := range all {
		methods = append(methods, m)
	}
	slices.Sort(methods)

	denied, why, root, err := simulate(ctx, i, rep.Caller, all)
	if err != nil {
		return Report{}, err
	}
	for _, m := range methods {
		c := CommandReport{Command: m, Checked: why == "", Held: []string{}, Missing: []string{}}
		for _, n := range all[m] {
			switch {
			case why != "":
			case denied[install.Grant{Action: n.action, Resource: n.resource}] || denied[install.Grant{Action: n.action}],
				n.action == getAPIs && r.LookupRefused:
				c.Missing = append(c.Missing, n.text)
			default:
				c.Held = append(c.Held, n.text)
			}
		}
		rep.Commands = append(rep.Commands, c)
	}
	switch m := r.Manager; {
	case r.LookupRefused:
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("the manager's API could not be looked up in %s (%s refused): its routes are not checked; --api-url does without the lookup", r.Region, getAPIs))
	case m == nil:
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("no manager found in %s for env %s: only install, uninstall and estimate are checked", r.Region, r.Env))
	case m.APIID == "":
		rep.Warnings = append(rep.Warnings, "--api-url names no API id: a policy that names its API shows its routes missing")
	}
	switch {
	case why != "":
		rep.Warnings = append(rep.Warnings, "not checked ("+why+" refused): IAM's simulation is needed to tell the rights")
	case root:
		rep.Warnings = append(rep.Warnings, "the account's root holds every right: not simulated")
	default:
		rep.Warnings = append(rep.Warnings, "IAM's simulation leaves out service control policies and the API's own refusals")
	}
	return rep, nil
}

// simulate asks IAM which of the needs it denies the caller, in two simulations: execute-api:Invoke on the routes, the
// other actions on any resource. why is the action IAM refused ("" when it answered): then nothing is denied. The account's
// root (root) holds every right, not simulated.
func simulate(ctx context.Context, i IAM, caller string, all map[string][]need) (denied map[install.Grant]bool, why string, root bool, err error) {
	source, root, err := install.PolicySource(ctx, i, caller)
	switch {
	case root:
		return nil, "", true, nil
	case err != nil && install.Refused(err):
		return nil, "iam:GetRole", false, nil
	case err != nil:
		return nil, "", false, err
	}
	var routes, actions []string
	for _, ns := range all {
		for _, n := range ns {
			if n.action == Invoke {
				routes = append(routes, n.resource)
			} else {
				actions = append(actions, n.action)
			}
		}
	}
	denied = map[install.Grant]bool{}
	for _, ask := range []struct{ actions, resources []string }{{actions, []string{"*"}}, {[]string{Invoke}, routes}} {
		ask.actions, ask.resources = compact(ask.actions), compact(ask.resources)
		if len(ask.actions) == 0 || len(ask.resources) == 0 {
			continue
		}
		d, err := install.Denied(ctx, i, source, ask.actions, ask.resources)
		switch {
		case err != nil && install.Refused(err):
			return nil, "iam:SimulatePrincipalPolicy", false, nil
		case err != nil:
			return nil, "", false, err
		}
		for g := range d {
			denied[g] = true
		}
	}
	return denied, "", false, nil
}

func compact(l []string) []string {
	l = slices.Clone(l)
	slices.Sort(l)
	return slices.Compact(l)
}

// PrintText prints the report as `gcp check` does: the account and the manager, then a line per method, then the
// warnings.
func PrintText(w io.Writer, r Report) error {
	var b strings.Builder
	b.WriteString("Account " + r.Account)
	if r.alias != "" {
		b.WriteString(" (" + r.alias + ")")
	}
	if m := r.Manager; m != nil {
		version := m.Version
		if version == "" {
			version = "(version unknown)"
		}
		b.WriteString(", manager " + m.API + " " + version)
	}
	b.WriteString(", checked as " + r.Caller + "\n")
	for _, c := range r.Commands {
		switch {
		case !c.Checked:
			fmt.Fprintf(&b, "  - %s: not checked\n", c.Command)
		case c.OK():
			fmt.Fprintf(&b, "  ✓ %s: every permission held\n", c.Command)
		default:
			fmt.Fprintf(&b, "  ✗ %s: missing %s\n", c.Command, strings.Join(c.Missing, ", "))
		}
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", warning)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// PrintJSON prints the report as one JSON object.
func PrintJSON(w io.Writer, r Report) error { return output.JSON(w, r) }
