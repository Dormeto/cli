package main

// `dormeto discovery`: the manager's discovery of workloads (status, run, candidates, dismiss, restore, access and the
// configuration), through its /iam routes. A run can be waited for; dismiss and restore take the candidates by id or by
// their target group's name, the names resolved before anything is sent (an id is an ARN: it travels in bodies only;
// dismiss sends it as given); the configuration is replaced as the file is, after a confirmation. Exit codes as the
// fleet commands.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/fleet"
)

// runPoll is how often --wait reads the last run again.
const runPoll = 5 * time.Second

// dismissBatch is the most candidates the manager dismisses or restores in one call; maxNote the longest note, in
// characters.
const (
	dismissBatch = 100
	maxNote      = 256
)

// newDiscovery is `dormeto discovery` and its subcommands.
func newDiscovery(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("discovery", "See what discovery found, run it, dismiss candidates and set its configuration")
	f.bind(cmd)
	config := fleetCommand("config", "Show or replace the discovery configuration")
	config.AddCommand(newDiscoveryConfigShow(f, deps), newDiscoveryConfigSet(f, deps))
	cmd.AddCommand(newDiscoveryStatus(f, deps), newDiscoveryRun(f, deps), newDiscoveryCandidates(f, deps),
		newDiscoveryDismiss(f, deps), newDiscoveryRestore(f, deps), newDiscoveryAccess(f, deps), config)
	return cmd
}

func newDiscoveryStatus(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "status", Short: "Show the last discovery run, the candidates per status and the lint", Args: noArgs, SilenceErrors: true,
		Long: "Show the last discovery run (when, by whom, how many account-regions it scanned, skipped and failed), a Run\n" +
			"now refused since, the candidates per status, the roles the manager uses and what the lint found. Times are in\n" +
			"the local time zone. --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var st fleet.DiscoveryStatus
				if err := api.Get(ctx, "/discovery", nil, &st); err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), st)
				}
				fmt.Fprint(c.OutOrStdout(), st.Text(deps.now().Location()))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newDiscoveryRun(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		wait    bool
		timeout time.Duration
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use: "run", Short: "Ask for a discovery run now", Args: noArgs, SilenceErrors: true,
		Long: "Ask the manager for a discovery run now; it runs on its own. With --wait, read the last run every 5 seconds\n" +
			"until a new run has ended (printed as `discovery status` does) or the manager refused this one because another\n" +
			"was in progress (exit 1); a read that fails for a passing reason (a network error, 429, 5xx) is waited out.\n" +
			"Ctrl-C and --timeout stop waiting, the run goes on in the manager. --json prints the manager's answer, with\n" +
			"--wait the status once the run ended. Needs an operator.",
		RunE: func(c *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return finish(c, usagef("--timeout: %s is not a positive duration", timeout))
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var before, refusedBefore string
				if wait { // the runs shown now are not this one
					var st fleet.DiscoveryStatus
					if err := api.Get(ctx, "/discovery", nil, &st); err != nil {
						return err
					}
					before, refusedBefore = startedAt(st.LastRun), startedAt(st.Refused)
				}
				var accepted json.RawMessage
				if err := api.Post(ctx, "/discovery/run", nil, &accepted); err != nil {
					return err
				}
				if !wait {
					if asJSON {
						return printJSON(c.OutOrStdout(), accepted)
					}
					fmt.Fprintln(c.OutOrStdout(), "discovery run requested: dormeto discovery status shows it")
					return nil
				}
				fmt.Fprintln(textOut(c), "discovery run requested, waiting for it")
				followed, cancel := deps.timeout(ctx, timeout)
				defer cancel()
				st, err := waitForRun(followed, deps, api, before, refusedBefore)
				if err != nil {
					if followed.Err() != nil {
						fmt.Fprintln(c.ErrOrStderr(), "still running in the manager: dormeto discovery status")
						return exitCode(1)
					}
					return err
				}
				if asJSON {
					if err := printJSON(c.OutOrStdout(), st); err != nil {
						return err
					}
				} else {
					fmt.Fprint(c.OutOrStdout(), st.Text(deps.now().Location()))
				}
				if r := st.Refused; r != nil && startedAt(r) != refusedBefore {
					fmt.Fprintf(c.ErrOrStderr(), "refused: %s\n", r.SkippedReason)
					return exitCode(1)
				}
				if st.LastRun.Status == "failed" {
					fmt.Fprintf(c.ErrOrStderr(), "discovery run failed: %s\n", orNoReason(st.LastRun.Error))
					return exitCode(1)
				}
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&wait, "wait", false, "wait until the run has ended")
	fl.DurationVar(&timeout, "timeout", 30*time.Minute, "with --wait, stop waiting after this long (the run goes on in the manager)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func orNoReason(s string) string {
	if s == "" {
		return "no reason given"
	}
	return s
}

func startedAt(r *fleet.RunRecord) string {
	if r == nil {
		return ""
	}
	return r.StartedAt
}

// waitForRun reads GET /discovery every runPoll until the last run is not the one started at before and has ended, or a
// run refused since refusedBefore shows; the status read last, or the error that stopped the reading (the context's, or
// one that will not pass: a transient one is waited out, as an operation's reads are).
func waitForRun(ctx context.Context, deps fleetDeps, api *fleet.Client, before, refusedBefore string) (fleet.DiscoveryStatus, error) {
	for {
		if err := deps.sleep(ctx, runPoll); err != nil {
			return fleet.DiscoveryStatus{}, err
		}
		var st fleet.DiscoveryStatus
		if err := api.Get(ctx, "/discovery", nil, &st); err != nil {
			if fleet.Transient(err) && ctx.Err() == nil {
				continue
			}
			return st, err
		}
		if st.Refused != nil && st.Refused.StartedAt != refusedBefore {
			return st, nil
		}
		if r := st.LastRun; r != nil && r.StartedAt != before && r.Ended() {
			return st, nil
		}
	}
}

func newDiscoveryCandidates(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		status, account, region string
		limit                   int
		asJSON                  bool
	)
	cmd := &cobra.Command{
		Use: "candidates", Short: "List the discovery candidates of a status", Args: noArgs, SilenceErrors: true,
		Long: "List the candidates of one status (default pending-onboarding): target group, account, region, hosts, idle\n" +
			"hours and requests over 7 days, the workload or the operation running on it, and why it has that status. Every\n" +
			"page is read unless --limit stops at that many (0: all). --candidate-region needs --account (--region is the\n" +
			"manager's).\n" +
			"--json prints the rows as the manager sends them.",
		RunE: func(c *cobra.Command, _ []string) error {
			var err error
			switch {
			case !slices.Contains(fleet.CandidateStatuses, status):
				err = usagef("--status: %q is not one of %s", status, strings.Join(fleet.CandidateStatuses, ", "))
			case account != "" && !discover.IsAccountID(account):
				err = usagef("--account: %q is not 12 digits", account)
			case region != "" && account == "":
				err = usagef("--candidate-region needs --account")
			case region != "" && !regionName.MatchString(region):
				err = usagef("--candidate-region: %q is not a region such as eu-west-1", region)
			default:
				err = checkLimit(limit)
			}
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				cs, err := fleet.ListCandidates(ctx, api, status, account, region, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), orEmpty(cs))
				}
				fmt.Fprint(c.OutOrStdout(), candidatesText(cs, status))
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&status, "status", "pending-onboarding", "the status: "+strings.Join(fleet.CandidateStatuses, ", "))
	fl.StringVar(&account, "account", "", "only the candidates of this AWS account (12 digits)")
	fl.StringVar(&region, "candidate-region", "", "only the candidates in this region of --account (--region is the manager's)")
	fl.IntVar(&limit, "limit", 0, "stop at this many candidates (0, the default: all)")
	fl.BoolVar(&asJSON, "json", false, "print the candidates as the manager sends them")
	return cmd
}

func orEmpty(cs []fleet.Candidate) []fleet.Candidate {
	if cs == nil {
		return []fleet.Candidate{}
	}
	return cs
}

// candidatesText is the candidates as a table, "no candidates in <status>" for none.
func candidatesText(cs []fleet.Candidate, status string) string {
	if len(cs) == 0 {
		return "no candidates in " + status + "\n"
	}
	rows := [][]string{{"TARGET GROUP", "ACCOUNT", "REGION", "HOSTS", "IDLE 7D", "REQUESTS 7D", "WORKLOAD", "REASON"}}
	for _, c := range cs {
		hosts := strings.Join(c.Hosts, ",")
		if hosts == "" {
			hosts = "—"
		}
		workload := "—"
		switch {
		case c.Operation != nil:
			workload = c.Operation.Type + " running"
		case c.Workload != nil:
			workload = c.Workload.Router + "/" + c.Workload.Name
		}
		rows = append(rows, []string{c.Name(), c.Account, c.Region, hosts, figure(c.IdleHours7d, "h"), figure(c.Requests7d, ""), workload, c.StatusReason})
	}
	return fleet.Table(rows)
}

// figure is a measure as a whole number and its unit, "—" when unknown.
func figure(v *float64, unit string) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%s", *v, unit)
}

func newDiscoveryDismiss(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		note   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "dismiss <candidate>...", Short: "Dismiss candidates: they leave the pending onboarding", SilenceErrors: true,
		Long: "Dismiss candidates, each given by its id (the target group's ARN, sent as it is: a candidate of any status) or\n" +
			"the name of its target group, among those pending onboarding. Every name is resolved before anything is sent: an\n" +
			"unknown one, or one that names the target groups of several accounts or regions (their ARNs are listed), exits 2\n" +
			"and nothing is dismissed. --note says why (cut at 256 characters). `discovery restore` brings them back. --json\n" +
			"prints the manager's candidates. Needs an operator.",
		RunE: func(c *cobra.Command, args []string) error {
			return moveCandidates(c, f, deps, args, "pending-onboarding", "dismiss", "dismissed", cutNote(note), true, asJSON)
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "why they are dismissed (256 characters at most)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's candidates")
	return cmd
}

func newDiscoveryRestore(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "restore <candidate>...", Short: "Restore dismissed candidates", SilenceErrors: true,
		Long: "Restore dismissed candidates, each given by its id (the target group's ARN) or the name of its target group,\n" +
			"resolved among the dismissed ones before anything is sent as `discovery dismiss` does. --json prints the\n" +
			"manager's candidates. Needs an operator.",
		RunE: func(c *cobra.Command, args []string) error {
			return moveCandidates(c, f, deps, args, "dismissed", "restore", "restored", "", false, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's candidates")
	return cmd
}

// cutNote is the note as far as the manager keeps it: 256 characters.
func cutNote(note string) string {
	if utf8.RuneCountInString(note) <= maxNote {
		return note
	}
	return string([]rune(note)[:maxNote])
}

// moveCandidates resolves refs among the candidates of status from (with idsAsGiven, an id is sent as it is: see
// chooseCandidates), then sends their ids, 100 at a time, to POST /discovery/<action> (with the note when there is one):
// the ids never go in a path. It prints what was done, past ("dismissed 3 candidates"), or under --json the candidates
// the manager answered.
func moveCandidates(c *cobra.Command, f *fleetFlags, deps fleetDeps, refs []string, from, action, past, note string, idsAsGiven, asJSON bool) error {
	refs, err := candidateRefs(refs)
	if err != nil {
		return finish(c, err)
	}
	return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
		chosen, err := chooseCandidates(ctx, api, refs, from, idsAsGiven)
		if err != nil {
			return err
		}
		var done []fleet.Candidate
		var failure error
		for start := 0; start < len(chosen) && failure == nil; start += dismissBatch {
			batch := chosen[start:min(start+dismissBatch, len(chosen))]
			ids := make([]string, len(batch))
			for i, b := range batch {
				ids[i] = b.ID
			}
			body := map[string]any{"ids": ids}
			if note != "" {
				body["note"] = note
			}
			var answer struct {
				Candidates []fleet.Candidate `json:"candidates"`
			}
			if failure = api.Post(ctx, "/discovery/"+action, body, &answer); failure == nil {
				done = append(done, answer.Candidates...)
			}
		}
		if asJSON {
			if err := printJSON(c.OutOrStdout(), orEmpty(done)); err != nil && failure == nil {
				failure = err
			}
		} else if len(done) > 0 {
			fmt.Fprintf(c.OutOrStdout(), "%s %d %s%s\n", past, len(done), fleet.Plural(len(done), "candidate"), namesText(chosen[:len(done)]))
		}
		if failure != nil && len(done) > 0 {
			fmt.Fprintf(c.ErrOrStderr(), "%d of %d candidates were %s before this failed\n", len(done), len(chosen), past)
		}
		return failure
	})
}

// chooseCandidates resolves each ref among the candidates of status from, in the order given, each candidate once. With
// idsAsGiven, a ref that is an ARN (a candidate's id) is taken as it is, whatever its candidate's status, and the
// candidates are listed only when a name is given.
func chooseCandidates(ctx context.Context, api *fleet.Client, refs []string, from string, idsAsGiven bool) ([]fleet.Candidate, error) {
	asGiven := func(ref string) bool { return idsAsGiven && strings.HasPrefix(ref, "arn:") }
	var all []fleet.Candidate
	if slices.ContainsFunc(refs, func(ref string) bool { return !asGiven(ref) }) { // a name to resolve
		var err error
		if all, err = fleet.ListCandidates(ctx, api, from, "", "", 0); err != nil {
			return nil, err
		}
	}
	var out []fleet.Candidate
	seen := map[string]bool{}
	for _, ref := range refs {
		cand := fleet.Candidate{ID: ref}
		if !asGiven(ref) {
			found, err := resolveCandidates(all, []string{ref}, from)
			if err != nil {
				return nil, err
			}
			cand = found[0]
		}
		if !seen[cand.ID] {
			seen[cand.ID] = true
			out = append(out, cand)
		}
	}
	return out, nil
}

// namesText is ": a, b" for a few candidates, nothing for many.
func namesText(cs []fleet.Candidate) string {
	if len(cs) > 10 {
		return ""
	}
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name()
	}
	return ": " + strings.Join(names, ", ")
}

// candidateRefs are the arguments, each a candidate id or target group name: at least one, none empty.
func candidateRefs(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, usagef("name at least one candidate")
	}
	for _, a := range args {
		if strings.TrimSpace(a) == "" {
			return nil, usagef("a candidate name is empty")
		}
	}
	return args, nil
}

// resolveCandidates is fleet.ResolveCandidates, its refusals as usage errors, naming the status when there are no
// candidates in it at all.
func resolveCandidates(cs []fleet.Candidate, refs []string, status string) ([]fleet.Candidate, error) {
	if len(cs) == 0 {
		return nil, usagef("no candidate %s in %s", refs[0], status)
	}
	out, err := fleet.ResolveCandidates(cs, refs)
	if err != nil {
		return nil, usageError{err}
	}
	return out, nil
}

func newDiscoveryAccess(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		limit  int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "access", Short: "Show the accounts whose discovery or command role is missing", Args: noArgs, SilenceErrors: true,
		Long: "Show the organization access of the last run: how many accounts are ready, the delegation policy, the event\n" +
			"forwarders, and a table of the accounts that are not ready with what is missing. Every page is read unless\n" +
			"--limit stops at that many accounts (0: all). --json prints the manager's answer.",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := checkLimit(limit); err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				r, err := fleet.ReadAccess(ctx, api, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), r)
				}
				fmt.Fprint(c.OutOrStdout(), r.Text(deps.now().Location()))
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "stop at this many accounts (0, the default: all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newDiscoveryConfigShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show", Short: "Show the discovery configuration and its lint", Args: noArgs, SilenceErrors: true,
		Long: "Print the discovery configuration as JSON alone on stdout, so that `show > config.json` is a file `config set`\n" +
			"takes, and what the lint finds in it on stderr. --json prints the manager's whole answer instead: the\n" +
			"configuration as its `config` member, who set it and when, the lint.",
		RunE: func(c *cobra.Command, _ []string) error {
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var cfg fleet.DiscoveryConfig
				if err := api.Get(ctx, "/discovery/config", nil, &cfg); err != nil {
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), cfg)
				}
				fmt.Fprint(c.OutOrStdout(), cfg.Text())
				fmt.Fprint(c.ErrOrStderr(), fleet.LintText(cfg.Lint))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

func newDiscoveryConfigSet(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		file   string
		yes    bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "set --file <path>|-", Short: "Replace the discovery configuration with a JSON file", Args: noArgs, SilenceErrors: true,
		Long: "Replace the whole discovery configuration with the JSON file (- reads standard input): the output of `config\n" +
			"show` (the configuration as it is), or the answer of `config show --json`, whose `config` member is sent. The\n" +
			"manager checks it, and a refusal prints each of its errors on a line (exit 1). It is always confirmed on a\n" +
			"terminal (--yes without one). --json prints the manager's answer. Needs an administrator.",
		RunE: func(c *cobra.Command, _ []string) error {
			data, err := readJSONFile(c, file)
			if err != nil {
				return finish(c, err)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				if done, err := confirmNames(ctx, c, deps, []string{"discovery configuration"}, "configuration",
					"Replace the discovery configuration?", true, false, yes); done || err != nil {
					return err
				}
				var cfg fleet.DiscoveryConfig
				if err := api.Put(ctx, "/discovery/config", json.RawMessage(data), &cfg); err != nil {
					var ae *fleet.APIError
					if errors.As(err, &ae) && len(ae.Errors) > 0 {
						return errors.New(strings.Join(append([]string{ae.Error()}, ae.Errors...), "\n"))
					}
					return err
				}
				if asJSON {
					return printJSON(c.OutOrStdout(), cfg)
				}
				fmt.Fprintln(c.OutOrStdout(), "discovery configuration replaced")
				fmt.Fprint(c.OutOrStdout(), fleet.LintText(cfg.Lint))
				return nil
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&file, "file", "", "the configuration, a JSON file (- for standard input)")
	fl.BoolVar(&yes, "yes", false, "do not ask for a confirmation (needed without a terminal)")
	fl.BoolVar(&asJSON, "json", false, "print the manager's answer")
	return cmd
}

// readJSONFile reads path (- for the command's input): it must be given and hold JSON; the answer of `config show
// --json` is unwrapped to its `config` member (which must be an object), anything else is sent as it is.
func readJSONFile(c *cobra.Command, path string) ([]byte, error) {
	if path == "" {
		return nil, usagef("--file: name the configuration file (- for standard input)")
	}
	var data []byte
	var err error
	if path == "-" {
		path = "standard input"
		data, err = io.ReadAll(c.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, usagef("--file: %v", err)
	}
	if !json.Valid(data) {
		return nil, usagef("--file: %s is not JSON", path)
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) == nil {
		if member, ok := probe["config"]; ok {
			var obj map[string]json.RawMessage
			if json.Unmarshal(member, &obj) != nil || obj == nil {
				return nil, usagef("--file: the config member of %s is not a configuration object", path)
			}
			return member, nil
		}
	}
	return data, nil
}
