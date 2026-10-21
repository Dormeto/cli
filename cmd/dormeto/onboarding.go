package main

// `dormeto workloads onboard`, `offboard` and `exclude`: the manager's operations on workloads, started and followed as
// the dashboard's Discovery and Workload views do (see runOperations). Onboard resolves its candidate among those pending
// onboarding and sends what the onboarding dialog sends; offboard and exclude are always confirmed on a terminal
// (--yes without one; --dry-run lists the targets). Every argument the manager refuses by a rule the CLI knows is
// refused here first (exit 2), nothing sent.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
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

func newWorkloadsOnboard(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		name, wakeMode string
		idle           int
		databases      []string
		fallbacks      []string
		setTags        []string
		wait           waitFlags
		asJSON         bool
	)
	cmd := &cobra.Command{
		Use: "onboard <candidate>", Short: "Onboard a discovery candidate: Dormeto starts managing its workload", SilenceErrors: true,
		Long: "Start the manager's onboarding of a candidate pending onboarding, given by its id (the target group's ARN) or\n" +
			"the name of its target group (an unknown or ambiguous name exits 2, its ARNs listed, nothing sent), and follow\n" +
			"it; then print the workload as <router>/<workload id>. It sends what the dashboard's dialog sends: --name (the\n" +
			"workload's display name, up to 64 characters; empty, the target group's name), --idle-minutes (5 to 1440, 15 by\n" +
			"default), --wake-mode (any, or browser: only the waiter page, the header or a wake rule wakes), --database (an\n" +
			"RDS instance identifier, or cluster:<identifier>: stopped after the application, started before it),\n" +
			"--fallback-type (an EC2 instance type such as t3a.medium, at most 5; for the EC2 instances of a workload) and\n" +
			"--set-tag (Dormeto tags k=v; administrators). --no-wait only starts it; Ctrl-C and --timeout stop following,\n" +
			"the operation goes on in the manager: `dormeto operations watch <id>`. Needs an operator.",
		RunE: func(c *cobra.Command, args []string) error {
			name = strings.TrimSpace(name)
			var tags map[string]string
			err := wait.check()
			switch {
			case err != nil:
			case len(args) != 1 || strings.TrimSpace(args[0]) == "":
				err = usagef("name exactly one candidate (its id or target group name)")
			case utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
				err = usagef("--name: at most 64 characters, no control characters")
			case idle < 5 || idle > 1440:
				err = usagef("--idle-minutes: %d is not from 5 to 1440", idle)
			case wakeMode != "any" && wakeMode != "browser":
				err = usagef("--wake-mode: %q is not any or browser", wakeMode)
			case len(fallbacks) > maxFallbackTypes:
				err = usagef("--fallback-type: at most %d (%d given)", maxFallbackTypes, len(fallbacks))
			}
			for _, t := range fallbacks {
				if err == nil && !instanceType.MatchString(t) {
					err = usagef("--fallback-type: %q is not an EC2 instance type such as t3a.medium", t)
				}
			}
			if err == nil {
				tags, err = parseTagFlag("--set-tag", setTags)
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
				settings := map[string]any{"idleMinutes": idle, "wakeMode": wakeMode}
				if len(fallbacks) > 0 {
					settings["fallbackInstanceTypes"] = fallbacks
				}
				dbs := []map[string]string{}
				for _, d := range databases {
					if id, ok := strings.CutPrefix(d, "cluster:"); ok {
						dbs = append(dbs, map[string]string{"kind": "rds-cluster", "id": id})
					} else {
						dbs = append(dbs, map[string]string{"kind": "rds-instance", "id": d})
					}
				}
				body := map[string]any{"type": "onboard", "candidateId": cand.ID, "settings": settings, "databases": dbs}
				if name != "" {
					body["name"] = name
				}
				if len(tags) > 0 {
					body["tags"] = tags
				}
				label := orDefault(name, cand.Name())
				ops, err := runOperations(ctx, c, deps, opItems(api, []string{label}, func(ctx context.Context, _ int) (fleet.Operation, error) {
					return fleet.StartOperation(ctx, api, body)
				}), 1, wait)
				sayOnboarded(ctx, c, api, ops, asJSON)
				return printStarted(c, asJSON, ops, err)
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "the workload's display name, up to 64 characters (default, or empty: the target group's name)")
	fl.IntVar(&idle, "idle-minutes", 15, "the idle window, 5 to 1440 minutes")
	fl.StringVar(&wakeMode, "wake-mode", "any", "what wakes the workload: any request that passes the filters, or browser")
	fl.StringArrayVar(&databases, "database", nil, "a companion RDS instance identifier, or cluster:<identifier> (repeatable)")
	fl.StringArrayVar(&fallbacks, "fallback-type", nil, "a fallback EC2 instance type such as t3a.medium (repeatable, at most 5; for the EC2 instances of a workload)")
	fl.StringArrayVar(&setTags, "set-tag", nil, "a Dormeto tag of the workload, key=value (repeatable; administrators)")
	fl.BoolVar(&asJSON, "json", false, "print the operation, as the manager last said it")
	wait.bind(cmd)
	return cmd
}

// sayOnboarded prints "onboarded <router>/<workload id>" for each onboarding that succeeded (stderr under --json). The
// operation names the workload and, to an administrator, its router in its context; else the router of its account and
// region is looked up.
func sayOnboarded(ctx context.Context, c *cobra.Command, api *fleet.Client, ops []fleet.Operation, asJSON bool) {
	out := c.OutOrStdout()
	if asJSON {
		out = c.ErrOrStderr()
	}
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
			"terminal (--yes without one; --dry-run lists the workloads and sends nothing). A workload another operation\n" +
			"holds is refused with that operation's id, the others go on (exit 1). --no-wait only starts them; Ctrl-C and\n" +
			"--timeout stop following, the operations go on in the manager: `dormeto operations watch <id>`. Needs an operator.",
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
			return w.run(c, args, "offboard", "Offboard %d workloads?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
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

func newWorkloadsExclude(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps, always: true}
	var (
		reason, dur, until string
		wait               waitFlags
		asJSON             bool
	)
	cmd := &cobra.Command{
		Use: "exclude (<router>/<workload>... | --tag k=v...) --reason <text> [--for <duration> | --until <time>]", Short: "Exclude the target groups of workloads from discovery",
		Long: "Exclude each workload's target group from discovery, with a reason: the manager pauses its managed workloads\n" +
			"(an operation, followed). --reason is 1 to 246 characters; --for is a duration up to 366 days (90m, 2h, 3d) and\n" +
			"--until an RFC 3339 time or HH:MM today in the local time zone, within 366 days; without either the exclusion\n" +
			"lasts until `dormeto exclusions lift`. It is always confirmed on a terminal (--yes without one; --dry-run lists\n" +
			"the workloads). --no-wait only starts them; Ctrl-C and --timeout stop following, the operations go on in the\n" +
			"manager: `dormeto operations watch <id>`. Needs an operator.",
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
			return w.run(c, args, "exclude the target group of", "Exclude the target groups of %d workloads?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				labels := make([]string, len(targets))
				for i, t := range targets {
					labels[i] = t.String()
				}
				ops, err := runOperations(ctx, c, deps, opItems(api, labels, func(ctx context.Context, i int) (fleet.Operation, error) {
					t := targets[i]
					var st struct {
						TargetGroupARN string `json:"targetGroupArn"`
					}
					if err := api.Get(ctx, t.Path(""), nil, &st); err != nil {
						if ctx.Err() != nil {
							return fleet.Operation{}, err
						}
						return fleet.Operation{}, notStarted{fmt.Errorf("read the workload: %w", err)}
					}
					if st.TargetGroupARN == "" {
						return fleet.Operation{}, notStarted{errors.New("the workload has no target group to exclude")}
					}
					body := exclusionBody(map[string]any{"kind": "target-group", "arn": st.TargetGroupARN}, r, end, set)
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
