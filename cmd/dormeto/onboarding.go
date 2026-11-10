package main

// `dormeto aws workloads onboard`, `offboard` and `exclude`: the manager's operations on workloads, started and followed as
// the dashboard's Discovery and Workload views do (see runOperations). Onboard resolves its candidate among those pending
// onboarding and sends what the onboarding dialog sends; offboard and exclude are always confirmed on a terminal
// (--yes without one; --dry-run says each request). Every argument the manager refuses by a rule the CLI knows is
// refused here first (exit 2), nothing sent.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/gcponboard"
)

const (
	// opConcurrency is how many operations are started at once by default.
	opConcurrency = 4
	// maxExclusionReason is the longest reason of an exclusion: the manager's 256, less its "excluded: " prefix.
	maxExclusionReason = 246
	// maxFallbackTypes is the most fallback instance types a workload takes.
	maxFallbackTypes = 5
)

// instanceType is an EC2 instance type as the manager and the dashboard accept it (t3a.medium).
var instanceType = regexp.MustCompile(`^[a-z][a-z0-9-]*\d[a-z0-9-]*\.[a-z0-9]+$`)

// machineType is a Compute Engine machine type (e2-medium) and cloudSQLName a Cloud SQL instance name.
var (
	machineType  = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+$`)
	cloudSQLName = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,96}[a-z0-9])?$`)
)

// notStarted is an error from before an operation's start was sent: the item is reported as not started.
type notStarted struct{ error }

func (e notStarted) Unwrap() error      { return e.error }
func (notStarted) Is(target error) bool { return target == fleet.ErrNotSent }

// opItems makes an item per label, started by start(label's index) and read with fleet.GetOperation.
func opItems(api *fleet.Client, labels []string, start func(ctx context.Context, i int) (fleet.Operation, error)) []opItem {
	items := make([]opItem, len(labels))
	for i, label := range labels {
		items[i] = opItem{
			label: label,
			start: func(ctx context.Context) (fleet.Operation, error) { return start(ctx, i) },
			get:   fleet.GetOperation(api),
		}
	}
	return items
}

// checkCandidateFlags refuses (exit 2) a flag value that is not the candidate's cloud's: Cloud SQL instance names and
// Compute Engine machine types for a Google Cloud candidate, EC2 instance types for an AWS one (RDS identifiers are
// left to the manager), and --router for a Google Cloud candidate only.
func checkCandidateFlags(cand fleet.Candidate, router string, databases, fallbacks []string) error {
	if !cand.IsGoogle() {
		if router != "" {
			return usagef("--router: only for a Google Cloud candidate; %s is an AWS one", cand.Name())
		}
		for _, t := range fallbacks {
			if !instanceType.MatchString(t) {
				return usagef("--fallback-type: %q is not an EC2 instance type such as t3a.medium", t)
			}
		}
		return nil
	}
	for i, d := range databases {
		if !cloudSQLName.MatchString(d) {
			return usagef("--database: %q is not a Cloud SQL instance name such as orders-db", d)
		}
		if slices.Contains(databases[:i], d) {
			return usagef("--database: Cloud SQL instance %s is named twice", d)
		}
	}
	for i, t := range fallbacks {
		if !machineType.MatchString(t) {
			return usagef("--fallback-type: %q is not a Compute Engine machine type such as e2-medium", t)
		}
		if slices.Contains(fallbacks[:i], t) {
			return usagef("--fallback-type: %s is named twice", t)
		}
	}
	return nil
}

func newWorkloadsOnboard(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		name, wakeMode, router string
		idle, wakeTimeout      int
		databases              []string
		fallbacks              []string
		setTags                []string
		hibernate              string
		wait                   waitFlags
		asJSON                 bool
	)
	cmd := &cobra.Command{
		Use: "onboard <candidate>", Short: "Onboard a discovery candidate: Dormeto starts managing its workload", SilenceErrors: true,
		Long: "Start the manager's onboarding of a candidate pending onboarding, given by its id (an AWS target group's ARN,\n" +
			"or a Google Cloud backend service's path) or the name of its target group or backend service (an unknown or\n" +
			"ambiguous name exits 2, its ids listed, nothing sent), and follow it; then print the workload as\n" +
			"<router>/<workload id>. It sends what the dashboard's dialog sends: --name (the workload's display name, up to\n" +
			"64 characters; empty, the target group's or backend service's name), --idle-minutes (5 to 1440) and --wake-mode\n" +
			"(any, or browser: only the waiter page, the header or a wake rule wakes), sent only when given: without them the\n" +
			"levels decide (the dashboard's Inherit; their built-ins are 15 minutes and any), --wake-timeout-seconds (60 to 3600\n" +
			"on AWS, to 1700 on Google Cloud: how long a wake may take, given likewise), --database (an RDS instance\n" +
			"identifier or cluster:<identifier>, or for a Google Cloud candidate a Cloud SQL instance name: stopped after the\n" +
			"application, started before it), --fallback-type (an EC2 instance type such as t3a.medium, or for a Google Cloud\n" +
			"candidate a Compute Engine machine type such as e2-medium; at most 5; for the instances of a workload), --router\n" +
			"(a Google Cloud candidate only: names the router to onboard a global candidate on when its project has several; a\n" +
			"regional one goes on its region's router), --set-tag (Dormeto tags k=v;\n" +
			"administrators) and --hibernate (true or false: on AWS, hibernate its instances launched with hibernation at a\n" +
			"nap, from routers 0.32.0; on Google Cloud, suspend its VMs, not stop them; without it the workload inherits the\n" +
			"router's or the manager's). --no-wait only\n" +
			"starts it; Ctrl-C and --timeout stop following, the operation goes on in the manager:\n" +
			"`dormeto aws operations watch <id>`. --dry-run says the candidate, its eligibility as discovery listed it and the\n" +
			"body it would send, and sends nothing (the router's own preview runs as the onboarding's first step). Needs an operator.",
		RunE: func(c *cobra.Command, args []string) error {
			name = strings.TrimSpace(name)
			var tags map[string]string
			err := wait.check()
			switch {
			case err != nil:
			case len(args) != 1 || strings.TrimSpace(args[0]) == "":
				err = usagef("name exactly one candidate (its id, or its target group's or backend service's name)")
			case utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
				err = usagef("--name: at most 64 characters, no control characters")
			case c.Flags().Changed("idle-minutes") && (idle < 5 || idle > 1440):
				err = usagef("--idle-minutes: %d is not from 5 to 1440", idle)
			case c.Flags().Changed("wake-timeout-seconds") && (wakeTimeout < 60 || wakeTimeout > 3600):
				err = usagef("--wake-timeout-seconds: %d is not from 60 to 3600", wakeTimeout)
			case c.Flags().Changed("wake-mode") && wakeMode != "any" && wakeMode != "browser":
				err = usagef("--wake-mode: %q is not any or browser", wakeMode)
			case len(fallbacks) > maxFallbackTypes:
				err = usagef("--fallback-type: at most %d (%d given)", maxFallbackTypes, len(fallbacks))
			}
			for _, t := range fallbacks { // of neither cloud's form: refused before the manager is reached
				if err == nil && !instanceType.MatchString(t) && !machineType.MatchString(t) {
					err = usagef("--fallback-type: %q is neither an EC2 instance type such as t3a.medium nor a Compute Engine machine type such as e2-medium", t)
				}
			}
			if err == nil {
				tags, err = parseTagFlag("--set-tag", setTags)
			}
			var hibernating *bool
			if err == nil && c.Flags().Changed("hibernate") {
				hibernating, err = parseHibernate(hibernate, false)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				all, err := fleet.ListCandidates(ctx, api, "pending-onboarding", "", "", 0)
				if err != nil {
					return err
				}
				chosen, err := resolveCandidates(all, args, "pending-onboarding")
				if err != nil {
					return err
				}
				cand := chosen[0]
				if err := checkCandidateFlags(cand, router, databases, fallbacks); err != nil {
					return err
				}
				if cand.IsGoogle() && c.Flags().Changed("wake-timeout-seconds") && wakeTimeout > 1700 {
					return usagef("--wake-timeout-seconds: %d is not from 60 to 1700, a Google Cloud workload's", wakeTimeout)
				}
				// What is not given is not sent: the levels decide, as the dashboard's Inherit.
				settings := map[string]any{}
				if c.Flags().Changed("idle-minutes") {
					settings["idleMinutes"] = idle
				}
				if c.Flags().Changed("wake-mode") {
					settings["wakeMode"] = wakeMode
				}
				if c.Flags().Changed("wake-timeout-seconds") {
					settings["wakeTimeoutSeconds"] = wakeTimeout
				}
				if len(fallbacks) > 0 {
					settings["fallbackInstanceTypes"] = fallbacks
				}
				dbs := []map[string]string{}
				for _, d := range databases {
					if cand.IsGoogle() {
						dbs = append(dbs, map[string]string{"kind": "cloudsql-instance", "id": d})
					} else if id, ok := strings.CutPrefix(d, "cluster:"); ok {
						dbs = append(dbs, map[string]string{"kind": "rds-cluster", "id": id})
					} else {
						dbs = append(dbs, map[string]string{"kind": "rds-instance", "id": d})
					}
				}
				if hibernating != nil {
					settings["hibernate"] = *hibernating
				}
				body := map[string]any{"type": "onboard", "candidateId": cand.ID, "settings": settings, "databases": dbs}
				if name != "" {
					body["name"] = name
				}
				if router != "" {
					body["router"] = router
				}
				if len(tags) > 0 {
					body["tags"] = tags
				}
				// The manager has no preview of an onboarding (the router's runs as the operation's step): a dry run says the
				// candidate and its eligibility as discovery listed it, then the body (the client's dry run).
				if dry := api.DryRun(); dry != nil {
					fmt.Fprint(textOut(c), candidateText(cand))
					dry.Context = map[string]any{"candidate": cand}
				}
				label := orDefault(name, cand.Name())
				ops, err := runOperations(ctx, c, deps, opItems(api, []string{label}, func(ctx context.Context, _ int) (fleet.Operation, error) {
					return fleet.StartOperation(ctx, api, body)
				}), 1, wait)
				sayOnboarded(ctx, c, api, ops)
				return printStartedOne(c, asJSON, ops, err)
			})
		},
	}
	fl := cmd.Flags()
	fl.SortFlags = false // the shared flags first, in gcp workloads onboard's order, then AWS's own
	fl.StringVar(&name, "name", "", sharedOnboardHelp["name"])
	fl.StringArrayVar(&setTags, "set-tag", nil, sharedOnboardHelp["set-tag"])
	fl.IntVar(&idle, "idle-minutes", 0, sharedOnboardHelp["idle-minutes"])
	fl.StringVar(&wakeMode, "wake-mode", "", sharedOnboardHelp["wake-mode"])
	fl.IntVar(&wakeTimeout, "wake-timeout-seconds", 0, sharedOnboardHelp["wake-timeout-seconds"])
	fl.StringVar(&hibernate, "hibernate", "", sharedOnboardHelp["hibernate"])
	fl.StringArrayVar(&fallbacks, "fallback-type", nil, sharedOnboardHelp["fallback-type"])
	fl.StringArrayVar(&databases, "database", nil, sharedOnboardHelp["database"])
	fl.StringVar(&router, "router", "", "a Google Cloud candidate: the router to onboard a global candidate on when its project has several (a regional one goes on its region's router)")
	fl.BoolVar(&asJSON, "json", false, "print the operation, as the manager last said it")
	wait.bind(cmd)
	return cmd
}

// candidateText is a candidate as onboard's dry run says it: what and where it is, then its eligibility as discovery listed it
// (its status, its reason, the last 7 days' figures).
func candidateText(cand fleet.Candidate) string {
	label := "account"
	if cand.IsGoogle() {
		label = "project"
	}
	text := fmt.Sprintf("candidate %s: %s, %s %s, %s (%s)\n", cand.Name(), fleet.CloudName(cand.Cloud), label, cand.Account, cand.Region, cand.ID)
	eligibility := cand.Status
	if cand.StatusReason != "" {
		eligibility += ": " + cand.StatusReason
	}
	var figures []string
	if cand.IdleHours7d != nil {
		figures = append(figures, "idle 7d "+figure(cand.IdleHours7d, "h"))
	}
	if cand.Requests7d != nil {
		figures = append(figures, "requests 7d "+figure(cand.Requests7d, ""))
	}
	if len(figures) > 0 {
		eligibility += "; " + strings.Join(figures, ", ")
	}
	return text + "eligibility, as discovery listed it: " + eligibility + "\n"
}

// sayOnboarded prints "onboarded <router>/<workload id>" for each onboarding that succeeded (on textOut). The operation
// names the workload and, to an administrator, its router in its context; else the router of its account and region is
// looked up.
func sayOnboarded(ctx context.Context, c *cobra.Command, api *fleet.Client, ops []fleet.Operation) {
	out := textOut(c)
	for _, op := range ops {
		if op.Type != "onboard" || op.Status != "succeeded" {
			continue
		}
		router, id := op.Workload()
		if id == "" {
			fmt.Fprintf(c.ErrOrStderr(), "the manager does not name the onboarded workload of operation %s\n", op.ID)
			continue
		}
		if router == "" {
			var rs fleet.Routers
			if api.Get(ctx, "/routers", nil, &rs) == nil {
				for _, r := range rs.Rows {
					if r.Account == op.Target.Account && r.Region == op.Target.Region {
						router = r.Name
						break
					}
				}
			}
		}
		if router == "" {
			fmt.Fprintf(out, "onboarded %s\n", id)
		} else {
			fmt.Fprintf(out, "onboarded %s/%s\n", router, id)
		}
	}
}

func newWorkloadsOffboard(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps, always: true}
	var (
		concurrency int
		wait        waitFlags
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use: "offboard (<router>/<workload>... | --tag k=v...)", Short: "Offboard workloads: Dormeto stops managing them",
		Long: "Start the manager's offboarding of each workload and follow them (--concurrency at once, 4 by default, 1 to 20):\n" +
			"the workload is woken, its application given back as it was, and it leaves Dormeto. It is always confirmed on a\n" +
			"terminal (--yes without one; --dry-run says each request and sends nothing). A workload another operation\n" +
			"holds is refused with that operation's id, the others go on (exit 1). --no-wait only starts them; Ctrl-C and\n" +
			"--timeout stop following, the operations go on in the manager: `dormeto aws operations watch <id>`. Needs an operator.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			err := wait.check()
			if err == nil && (concurrency < 1 || concurrency > 20) {
				err = usagef("--concurrency: %d is not from 1 to 20", concurrency)
			}
			if err == nil {
				err = w.targets.check(args, false)
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "Offboard %s?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				labels := make([]string, len(targets))
				for i, t := range targets {
					labels[i] = t.String()
				}
				ops, err := runOperations(ctx, c, deps, opItems(api, labels, func(ctx context.Context, i int) (fleet.Operation, error) {
					return fleet.StartOperation(ctx, api, map[string]any{"type": "offboard", "router": targets[i].Router, "workloadId": targets[i].ID})
				}), concurrency, wait)
				return printStarted(c, asJSON, ops, err)
			})
		},
	}
	w.bindShared(cmd)
	cmd.Flags().IntVar(&concurrency, "concurrency", opConcurrency, "how many offboardings run at once, 1 to 20")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the operations, as the manager last said them")
	wait.bind(cmd)
	return cmd
}

// exclusionReason is --reason as the manager keeps it: trimmed, 1 to 246 characters.
func exclusionReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxExclusionReason {
		return "", usagef("--reason: give 1 to %d characters", maxExclusionReason)
	}
	return reason, nil
}

// exclusionBody is the body of POST /exclusions for target, with the expiry when there is one.
func exclusionBody(target map[string]any, reason string, end time.Time, set bool) map[string]any {
	body := map[string]any{"target": target, "reason": reason}
	if set {
		body["expiresAt"] = end.UTC().Format("2006-01-02T15:04:05Z")
	}
	return body
}

// excludeWording names what an exclusion leaves out as the targets' routers' clouds call it: a target group on AWS, a
// backend service on Google Cloud, both when mixed or when the routers cannot be read.
func excludeWording(ctx context.Context, api *fleet.Client, targets []fleet.Target, question string) string {
	var rs fleet.Routers
	known := api.Get(ctx, "/routers", nil, &rs) == nil
	cloud := map[string]string{}
	for _, r := range rs.Rows {
		cloud[r.Name] = fleet.CloudName(r.Cloud)
	}
	aws, gcp := false, false
	for _, t := range targets {
		switch cloud[t.Router] {
		case "gcp":
			gcp = true
		case "aws":
			aws = true
		default:
			known = false
		}
	}
	noun := ""
	switch {
	case known && !gcp:
		return question
	case known && !aws:
		noun = "backend service"
	default:
		noun = "target group or backend service"
	}
	return "Exclude the " + noun + " of %s from discovery?"
}

func newWorkloadsExclude(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps, always: true, wording: excludeWording}
	var (
		reason, dur, until string
		wait               waitFlags
		asJSON             bool
	)
	cmd := &cobra.Command{
		Use: "exclude (<router>/<workload>... | --tag k=v...) --reason <text> [--for <duration> | --until <time>]", Short: "Exclude the target groups or backend services of workloads from discovery",
		Long: "Exclude each workload's target group (a Google Cloud workload's backend service) from discovery, with a reason: the manager pauses its managed workloads\n" +
			"(an operation, followed). --reason is 1 to 246 characters; --for is a duration up to 366 days (90m, 2h, 3d) and\n" +
			"--until an RFC 3339 time or HH:MM today in the local time zone, within 366 days; without either the exclusion\n" +
			"lasts until `dormeto aws exclusions lift`. It is always confirmed on a terminal (--yes without one; --dry-run says\n" +
			"each request, the workloads read, and sends nothing). --no-wait only starts them; Ctrl-C and --timeout stop\n" +
			"following, the operations go on in the manager: `dormeto aws operations watch <id>`. Needs an operator.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			r, err := exclusionReason(reason)
			var end time.Time
			var set bool
			if err == nil {
				end, set, err = endOf(deps.now(), dur, until, maxPause, false)
			}
			if err == nil {
				err = w.targets.check(args, false)
			}
			if err == nil {
				err = wait.check()
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "Exclude the target group of %s from discovery?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				labels := make([]string, len(targets))
				for i, t := range targets {
					labels[i] = t.String()
				}
				ops, err := runOperations(ctx, c, deps, opItems(api, labels, func(ctx context.Context, i int) (fleet.Operation, error) {
					t := targets[i]
					var st struct {
						Cloud          string `json:"cloud"`
						Account        string `json:"account"`
						Region         string `json:"region"`
						TargetGroupARN string `json:"targetGroupArn"`
						BackendService string `json:"backendService"`
						LB             *struct {
							Kind   string `json:"kind"`
							Region string `json:"region"`
						} `json:"lb"`
					}
					if err := api.Get(ctx, t.Path(""), nil, &st); err != nil {
						if ctx.Err() != nil {
							return fleet.Operation{}, err
						}
						return fleet.Operation{}, notStarted{fmt.Errorf("read the workload: %w", err)}
					}
					target := map[string]any{"kind": "target-group", "arn": st.TargetGroupARN}
					switch {
					case st.Cloud == "gcp": // the router answers the backend service's name: its path is built here
						if st.BackendService == "" {
							return fleet.Operation{}, notStarted{errors.New("the workload has no backend service to exclude")}
						}
						path := st.BackendService
						if !strings.HasPrefix(path, "projects/") {
							where := "global"
							if st.LB != nil && (st.LB.Kind == "regional" || st.LB.Kind == "internal") {
								where = "regions/" + orDefault(st.LB.Region, st.Region)
							}
							path = "projects/" + st.Account + "/" + where + "/backendServices/" + st.BackendService
						}
						target = map[string]any{"kind": "target-group", "path": path}
					case st.TargetGroupARN == "":
						return fleet.Operation{}, notStarted{errors.New("the workload has no target group to exclude")}
					}
					body := exclusionBody(target, r, end, set)
					body["workload"] = map[string]string{"router": t.Router, "id": t.ID}
					return fleet.Exclude(ctx, api, body)
				}), opConcurrency, wait)
				return printStarted(c, asJSON, ops, err)
			})
		},
	}
	w.bindShared(cmd)
	fl := cmd.Flags()
	fl.StringVar(&reason, "reason", "", "why, 1 to 246 characters, shown in the manager (required)")
	fl.StringVar(&dur, "for", "", "how long, a duration up to 366 days (90m, 2h, 3d); default until lifted")
	fl.StringVar(&until, "until", "", "until this time: RFC 3339, or HH:MM today in the local time zone")
	fl.BoolVar(&asJSON, "json", false, "print the operations, as the manager last said them")
	wait.bind(cmd)
	return cmd
}

// sharedOnboardHelp is what each flag both onboards take says, one description for both, a cloud's own limit said in
// the same sentence (the flags are listed in one order, aws workloads onboard's and gcp workloads onboard's alike).
var sharedOnboardHelp = map[string]string{
	"name":                 "the workload's display name, up to 64 characters (default: the target group's name on AWS, the workload's ID on Google Cloud)",
	"set-tag":              "a Dormeto tag of the workload, key=value (repeatable, at most 50; the dormeto: prefix is reserved; AWS: administrators)",
	"idle-minutes":         "the idle window, 5 to 1440 minutes (default: the levels', 15 built in)",
	"wake-mode":            "what wakes the workload: any request that passes the filters, or browser (default: the levels', any built in; behind Google Cloud's global load balancer the waiting page wakes by browser alone)",
	"wake-timeout-seconds": fmt.Sprintf("how long a wake may take, 60 to 3600 seconds, %d to %d on Google Cloud (default: the levels')", gcponboard.MinWakeTimeout, gcponboard.MaxWakeTimeout),
	"hibernate":            "hibernate at a nap: true or false (AWS: the instances launched with hibernation; Google Cloud: the VMs that can be suspended; default: inherit)",
	"fallback-type":        "a fallback instance type such as t3a.medium (AWS) or machine type such as e2-medium (Google Cloud: for the VMs of unmanaged instance groups and zonal network endpoint groups; a wake out of capacity runs them on it) (repeatable, at most 5)",
	"database":             "a companion RDS instance identifier, or cluster:<identifier> (AWS); a Cloud SQL instance name (Google Cloud: tagged as you, down after the application, up before it) (repeatable)",
}
