package main

// `dormeto aws schedule list|show|set|clear|add|remove|except|edit`: the workloads' schedules through the manager's /iam
// routes. show, set and clear were `workloads schedule`'s; add, remove, except and edit read a workload's schedule, change it
// (internal/schedule, every other field kept) and write it whole as set does, so that a change made meanwhile by someone else
// is lost, as in the dashboard. The router checks what is written and its refusal is printed as set prints it. Exit codes as
// the fleet commands: 0; 1 when the manager or a router refused; 2 for a bad flag or argument, or a window's number out of
// range; 128 and the signal's number on a signal.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
	"github.com/Dormeto/cli/internal/output"
	"github.com/Dormeto/cli/internal/schedule"
)

// changeLost is what every change's help says of a change made meanwhile.
const changeLost = "It reads the schedule, changes it and writes it whole, as schedule set does: a change made meanwhile by\n" +
	"someone else is lost. The router checks it and a refusal is printed as it words it. It prints what schedule set\n" +
	"prints, then the schedule as schedule show does (--dry-run: the schedule it would write, nothing written)."

// newSchedule is `dormeto aws schedule` and its subcommands.
func newSchedule(deps fleetDeps) *cobra.Command {
	f := &fleetFlags{}
	cmd := fleetCommand("schedule", "List, show and change the awake and nap windows of workloads")
	f.bind(cmd)
	cmd.AddCommand(newScheduleList(f, deps), newScheduleShow(f, deps), newScheduleSet(f, deps), newScheduleClear(f, deps),
		newScheduleAdd(f, deps), newScheduleRemove(f, deps), newScheduleExcept(f, deps), newScheduleEdit(f, deps))
	return cmd
}

func newScheduleList(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var (
		filters listFilters
		router  string
		cloud   cloudFilter
		all     bool
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use: "list", Short: "List the workloads' schedules: zone, windows and next event", Args: noArgs, SilenceErrors: true,
		Long: "List the workloads with a schedule, of both clouds: router, workload, name, cloud, state, the schedule's time zone, its\n" +
			"number of windows and its next event in its zone. --all adds the workloads without a schedule. The filters are\n" +
			"workloads list's. Each workload with a schedule is read for it, 8 at a time; one that cannot be read is said on\n" +
			"stderr and left out (exit 1). --json prints {workloads: [{router, workloadId, name, cloud, state, schedule, next}]}.",
		RunE: func(c *cobra.Command, _ []string) error {
			if _, err := fleet.ParseTags(filters.tags); err != nil {
				return finish(c, usageError{err})
			}
			if err := cloud.check(); err != nil {
				return finish(c, err)
			}
			q := url.Values{}
			if s := upper(filters.states); len(s) > 0 {
				q.Set("state", strings.Join(s, ","))
			}
			if len(filters.tags) > 0 {
				q["tag"] = filters.tags
			}
			if router != "" {
				q.Set("router", router)
			}
			if filters.search != "" {
				q.Set("q", filters.search)
			}
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				listed, err := everyWorkload(ctx, c, api, q)
				if err != nil {
					return err
				}
				listed = onlyCloud(cloud, listed, func(r fleet.Workload) string { return r.Cloud })
				rows := make([]fleet.ScheduleRow, 0, len(listed))
				scheduled := map[string]bool{} // read for its schedule: the others have none
				for _, w := range listed {
					if w.Scheduled || all {
						rows = append(rows, fleet.ScheduleRow{Router: w.Router, WorkloadID: w.WorkloadID, Name: w.Name, Cloud: w.Cloud,
							State: w.State, Schedule: json.RawMessage("null"), Next: []fleet.NextEvent{}})
					}
					scheduled[w.Router+"/"+w.WorkloadID] = w.Scheduled
				}
				failed := fleet.ReadSchedules(rows, func(r *fleet.ScheduleRow) error {
					if !scheduled[r.Router+"/"+r.WorkloadID] {
						return nil
					}
					var st struct {
						State    string            `json:"state"`
						Schedule json.RawMessage   `json:"schedule"`
						Next     []fleet.NextEvent `json:"next"`
					}
					if err := api.Get(ctx, fleet.Target{Router: r.Router, ID: r.WorkloadID}.Path(""), nil, &st); err != nil {
						return err
					}
					if len(st.Schedule) > 0 {
						r.Schedule = st.Schedule
					}
					if st.Next != nil {
						r.Next = st.Next
					}
					if st.State != "" {
						r.State = st.State
					}
					return nil
				})
				var shown []fleet.ScheduleRow
				for i, r := range rows {
					if err, ok := failed[i]; ok {
						fmt.Fprintf(c.ErrOrStderr(), "%s/%s: %s\n", r.Router, r.WorkloadID, err)
						continue
					}
					if all || string(r.Schedule) != "null" {
						shown = append(shown, r)
					}
				}
				return printSchedules(c, shown, all, asJSON, len(failed) > 0)
			})
		},
	}
	filters.bind(cmd)
	fl := cmd.Flags()
	fl.StringVar(&router, "router", "", "only the workloads of this router")
	cloud.bind(cmd)
	fl.BoolVar(&all, "all", false, "also the workloads without a schedule")
	fl.BoolVar(&asJSON, "json", false, "print {workloads: [...]}, each with its schedule as the router answers it and its next events")
	return cmd
}

// everyWorkload is every page of GET /workloads?<q>; a router that did not answer is warned of, a listing that cannot be
// completed said on stderr.
func everyWorkload(ctx context.Context, c *cobra.Command, api *fleet.Client, q url.Values) ([]fleet.Workload, error) {
	var out []fleet.Workload
	warnings := newRouterWarnings(c)
	seen := map[string]bool{}
	for page := 0; page < 1000; page++ {
		var w fleet.Workloads
		if err := api.Get(ctx, "/workloads", q, &w); err != nil {
			return nil, err
		}
		for _, e := range w.Errors {
			warnings.warn(e.Router, e.Message, "")
		}
		out = append(out, w.Rows...)
		if !w.More() {
			return out, nil
		}
		var next string
		if json.Unmarshal(w.Cursor, &next) != nil || next == "" || seen[next] {
			break
		}
		seen[next] = true
		q = cloneQuery(q)
		q.Set("cursor", next)
	}
	fmt.Fprintln(c.ErrOrStderr(), "the manager's listing could not be completed: some workloads may be missing")
	return out, nil
}

// cloneQuery is a copy of q.
func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = slices.Clone(v)
	}
	return out
}

// printSchedules prints `schedule list`'s table and its line, or --json; a row that could not be read makes the exit 1.
func printSchedules(c *cobra.Command, rows []fleet.ScheduleRow, all, asJSON, failed bool) error {
	out := c.OutOrStdout()
	if asJSON {
		if rows == nil {
			rows = []fleet.ScheduleRow{}
		}
		if err := output.JSON(out, struct {
			Workloads []fleet.ScheduleRow `json:"workloads"`
		}{rows}); err != nil {
			return err
		}
	} else {
		if len(rows) > 0 {
			fmt.Fprint(out, fleet.SchedulesTable(rows))
		}
		fmt.Fprintln(out, fleet.SchedulesSummary(rows, all))
	}
	if failed {
		return exitCode(1)
	}
	return nil
}

func newScheduleShow(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <router>/<workload>", Short: "Show a workload's schedule and its next events",
		Long: "Show the schedule: the time zone, the windows (numbered, a recurrence in words), the exceptions, the maximum nap\n" +
			"and the next events, in the schedule's time zone. --json prints {schedule, next, maintenanceDue} (schedule null when\n" +
			"none, and left out when the router, before 0.31.0, does not report schedules), and from router 0.36.0 joinMinutes,\n" +
			"joinFrom and joinInherited, the join in force beside the schedule's own. `dormeto aws schedule set --file -`\n" +
			"accepts it back and sends its schedule.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := (&targetFlags{}).check(args, true); err != nil {
				return finish(c, err)
			}
			t, _ := fleet.ParseTarget(args[0])
			return f.run(c, deps, func(ctx context.Context, api *fleet.Client) error {
				var st struct {
					Schedule       json.RawMessage   `json:"schedule"`
					Next           []fleet.NextEvent `json:"next"`
					MaintenanceDue string            `json:"maintenanceDue"`
					schedule.Join
				}
				if err := api.Get(ctx, t.Path(""), nil, &st); err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}
				// A status without a schedule key comes from a router before 0.31.0; a null one is "no schedule".
				shown := fleet.ScheduleShown{Schedule: st.Schedule, Next: st.Next, MaintenanceDue: st.MaintenanceDue, Join: st.Join}
				if asJSON {
					return output.JSON(c.OutOrStdout(), shown.JSON())
				}
				text, err := shown.Text(t.String())
				if err != nil {
					return err
				}
				fmt.Fprint(c.OutOrStdout(), text)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {schedule, next, maintenanceDue}, as schedule set --file accepts")
	return cmd
}

func newScheduleSet(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var (
		file   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use: "set (<router>/<workload>... | --tag k=v...) --file <path or ->", Short: "Set the schedule of workloads from a JSON file",
		Long: "Set the schedule of the workloads from a JSON file (- for standard input), sent as it is: the router checks it\n" +
			"and a refusal is printed as it words it. The file is the schedule, or `schedule show --json` output (its\n" +
			"`schedule` member is sent; a null one is refused). Needs manager 0.16.0 and\n" +
			"routers 0.31.0. The schedule's JSON:\n\n" +
			"  {\"timeZone\": \"Europe/Paris\",\n" +
			"   \"windows\": [{\"kind\": \"awake\", \"days\": [\"mon\",\"tue\",\"wed\",\"thu\",\"fri\"], \"start\": \"08:30\", \"end\": \"19:00\"},\n" +
			"               {\"kind\": \"nap\", \"days\": [\"sat\"], \"start\": \"00:00\", \"end\": \"00:00\", \"wake\": \"header\"}],\n" +
			"   \"exceptions\": [\"2026-12-25\"], \"maxNapDays\": 7}\n\n" +
			"From router 0.36.0, a window also takes repeat (daily, weekly, monthly or a cron expression, in place of days),\n" +
			"from and until, and the schedule joinMinutes; the README has the JSON:\n\n" +
			"  {\"kind\": \"nap\", \"start\": \"20:00\", \"end\": \"07:00\", \"from\": \"2027-01-04\",\n" +
			"   \"repeat\": {\"pattern\": \"weekly\", \"interval\": 2, \"days\": [\"mon\", \"thu\"]}}",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			err := w.targets.check(args, false)
			var raw json.RawMessage
			if err == nil {
				raw, err = readScheduleFile(c, file)
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "Set the schedule of %s?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					line, _, answer, err := putSchedule(ctx, api, t, raw)
					return line, answer, err
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	bindJSON(cmd, &asJSON)
	cmd.Flags().StringVar(&file, "file", "", "the schedule's JSON file, - for standard input")
	return cmd
}

// putSchedule writes the schedule raw of t: set's lines, the answer's schedule as shown, and the answer; a refusal as set says it.
func putSchedule(ctx context.Context, api *fleet.Client, t fleet.Target, raw json.RawMessage) (string, fleet.ScheduleShown, json.RawMessage, error) {
	var res struct {
		Schedule       json.RawMessage   `json:"schedule"`
		Next           []fleet.NextEvent `json:"next"`
		MaintenanceDue string            `json:"maintenanceDue"`
		schedule.Join
	}
	var answer json.RawMessage
	if err := api.Put(ctx, t.Path("/schedule"), raw, &answer); err != nil {
		return "", fleet.ScheduleShown{}, nil, needsRecurrenceRouter(err)
	}
	_ = json.Unmarshal(answer, &res)
	// The next events are read in the zone as stored (its canonical name), the file's when the answer has none.
	var s fleet.ScheduleView
	if json.Unmarshal(res.Schedule, &s) != nil || s.TimeZone == "" {
		s = fleet.ScheduleView{}
		_ = json.Unmarshal(raw, &s)
	}
	shown := fleet.ScheduleShown{Schedule: res.Schedule, Next: res.Next, MaintenanceDue: res.MaintenanceDue, Join: res.Join}
	if len(shown.Schedule) == 0 || string(shown.Schedule) == "null" {
		shown.Schedule = raw
	}
	return s.SetText(t.String(), res.Next, res.MaintenanceDue), shown, answer, nil
}

// needsRecurrenceRouter adds what a refusal means when it comes from a router too old for recurrences.
func needsRecurrenceRouter(err error) error {
	var ae *fleet.APIError
	if errors.As(err, &ae) && schedule.RefusedAsUnknown(ae.Message) {
		return fmt.Errorf("%w (recurrences and joined windows need router %s: upgrade the router to %s or later)", err, schedule.RecurrenceRouter, schedule.RecurrenceRouter)
	}
	return err
}

// readScheduleFile reads the schedule's JSON from path or, for "-", the command's input; only its being a JSON object
// is checked here, the router checks the rest.
func readScheduleFile(c *cobra.Command, path string) (json.RawMessage, error) {
	if path == "" {
		return nil, usagef("--file: the schedule's JSON file is needed (- for standard input)")
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(c.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, usagef("--file: %v", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, usagef("--file: not a JSON object: %v", err)
	}
	none := usagef("--file: no schedule in %s; use schedule clear", map[bool]string{true: "standard input", false: path}[path == "-"])
	if probe == nil { // null
		return nil, none
	}
	// AWS's next events, or a Google Cloud controller's.
	_, hasNext := probe["next"]
	_, hasDue := probe["maintenanceDue"]
	_, hasEvents := probe["nextEvents"]
	if _, ok := probe["schedule"]; !ok && (hasNext || hasDue || hasEvents) { // `schedule show --json` of a router that reports none
		return nil, none
	}
	if member, ok := probe["schedule"]; ok { // the shape of `schedule show --json` and of a PUT's answer
		var obj map[string]json.RawMessage
		if json.Unmarshal(member, &obj) != nil || obj == nil {
			return nil, none
		}
		return member, nil
	}
	return json.RawMessage(data), nil
}

func newScheduleClear(f *fleetFlags, deps fleetDeps) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var asJSON bool
	cmd := &cobra.Command{
		Use: "clear (<router>/<workload>... | --tag k=v...)", Short: "Remove the schedule of workloads",
		Long:          "Remove the schedule of the workloads: they follow their idle rules alone.",
		SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := w.targets.check(args, false); err != nil {
				return finish(c, err)
			}
			return w.run(c, args, "Clear the schedule of %s?", func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var raw json.RawMessage
					if err := api.Delete(ctx, t.Path("/schedule"), &raw); err != nil {
						return "", nil, err
					}
					var res struct{ Cleared bool }
					_ = json.Unmarshal(raw, &res)
					if !res.Cleared {
						return t.String() + ": no schedule to clear\n", raw, nil
					}
					return t.String() + ": schedule cleared\n", raw, nil
				})
				return err
			})
		},
	}
	w.bindShared(cmd)
	bindJSON(cmd, &asJSON)
	return cmd
}

// changeCommand is a change of the schedule (add, remove, except, edit): its flags give the change, checked before any call.
type changeCommand struct {
	use, short, long, question string
	several                    bool // takes several targets or --tag, as set; else one target
	bind                       func(cmd *cobra.Command)
	change                     func(c *cobra.Command) (schedule.Change, error)
}

func newScheduleChange(f *fleetFlags, deps fleetDeps, cc changeCommand) *cobra.Command {
	w := &writeCommand{f: f, deps: deps}
	var asJSON bool
	cmd := &cobra.Command{
		Use: cc.use, Short: cc.short, Long: cc.long + "\n\n" + changeLost, SilenceErrors: true,
		RunE: func(c *cobra.Command, args []string) error {
			err := w.targets.check(args, !cc.several)
			var change schedule.Change
			if err == nil {
				change, err = cc.change(c)
			}
			if err != nil {
				return finish(c, err)
			}
			return w.run(c, args, cc.question, func(ctx context.Context, api *fleet.Client, targets []fleet.Target) error {
				refused := false // a change that cannot be built: exit 2
				_, err := applyAnswers(ctx, c, targets, asJSON, func(t fleet.Target) (string, json.RawMessage, error) {
					var st struct {
						Schedule json.RawMessage `json:"schedule"`
					}
					if err := api.Get(ctx, t.Path(""), nil, &st); err != nil {
						return "", nil, err
					}
					if len(st.Schedule) == 0 {
						return "", nil, errors.New("the router does not report schedules (upgrade it to 0.31.0 or later)")
					}
					raw, err := change.Apply(st.Schedule)
					if err != nil {
						refused = true
						return "", nil, err
					}
					line, shown, answer, err := putSchedule(ctx, api, t, raw)
					if err != nil {
						return "", nil, lastWindow(change, raw, err)
					}
					more, err := shown.Changed(t.String())
					if err != nil {
						return "", nil, err
					}
					return line + more, answer, nil
				})
				if refused && ctx.Err() == nil {
					return exitCode(2)
				}
				return err
			})
		},
	}
	if cc.several {
		w.bindShared(cmd)
	} else {
		w.bindConfirmation(cmd)
	}
	bindJSON(cmd, &asJSON)
	cc.bind(cmd)
	return cmd
}

// lastWindow adds to a refusal of a removal that left no window what clear does.
func lastWindow(change schedule.Change, raw json.RawMessage, err error) error {
	var ae *fleet.APIError
	if change.Remove != 0 && schedule.Windows(raw) == 0 && errors.As(err, &ae) {
		return fmt.Errorf("%w (%s)", err, schedule.LastWindowHint)
	}
	return err
}

func newScheduleAdd(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var wf windowFlags
	return newScheduleChange(f, deps, changeCommand{
		use:   "add (<router>/<workload>... | --tag k=v...) --kind nap|awake [window flags]",
		short: "Add a nap or awake window to the schedule of workloads", long: addLong,
		question: "Add a window to the schedule of %s?", several: true, bind: wf.bind, change: wf.change,
	})
}

func newScheduleRemove(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var rf removeFlags
	return newScheduleChange(f, deps, changeCommand{
		use: "remove <router>/<workload> --window <n>", short: "Remove a window from a workload's schedule", long: removeLong,
		question: "Remove a window of the schedule of %s?", bind: rf.bind, change: rf.change,
	})
}

func newScheduleExcept(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var ef exceptFlags
	return newScheduleChange(f, deps, changeCommand{
		use: "except <router>/<workload> --date <YYYY-MM-DD>... [--window <n>] [--remove]", short: "Add or remove dates without a window in a workload's schedule",
		long: exceptLong, question: "Change the dates without a window of %s?", bind: ef.bind, change: ef.change,
	})
}

func newScheduleEdit(f *fleetFlags, deps fleetDeps) *cobra.Command {
	var ed editFlags
	return newScheduleChange(f, deps, changeCommand{
		use:   "edit (<router>/<workload>... | --tag k=v...) [--time-zone <zone>] [--join-minutes <n>|inherit] [--max-nap-days <n>]",
		short: "Change the time zone, joined windows or maximum nap of workloads' schedules", long: editLong,
		question: "Change the schedule of %s?", several: true, bind: ed.bind, change: ed.change,
	})
}

// The help of the changes, the same in both subtrees.
const (
	addLong = "Add a window to the schedule: --kind nap or awake. --days (mon to sun, comma-separated or repeated) with --start and\n" +
		"--end (HH:MM) make a weekly window. --repeat daily, weekly, monthly or cron makes a recurrence instead, with --interval\n" +
		"(every n days, weeks or months), --days (weekly), --day or --nth and --weekday (monthly), --cron and --minutes (cron,\n" +
		"which takes no --start or --end). --from and --until (YYYY-MM-DD) bound it. --wake header lets a request with the\n" +
		"x-dormeto-wake header wake a nap. A workload without a schedule needs --time-zone, an IANA name such as Europe/Paris."
	removeLong = "Remove window <n> of the schedule, as schedule show numbers them. A router that refuses a schedule left with no\n" +
		"window says so: schedule clear removes the schedule."
	exceptLong = "Add dates (YYYY-MM-DD) on which no window of the schedule starts, or with --window <n> on which that window does not;\n" +
		"--remove takes them out."
	editLong = "Change the schedule's own fields: --time-zone (an IANA name), --join-minutes (the largest gap that joins two\n" +
		"windows of one kind; inherit follows the router's and the organization's) and --max-nap-days (the longest nap before a\n" +
		"maintenance wake). An empty value removes the field."
)

// windowFlags are add's: a flag per member of a window, named after it, and the zone of a first schedule.
type windowFlags struct {
	kind, start, end, from, until, wake, repeat, weekday, cron, timeZone string
	days                                                                 []string
	interval, day, nth, minutes                                          int
}

func (w *windowFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&w.kind, "kind", "", "nap or awake (required)")
	fl.StringSliceVar(&w.days, "days", nil, "the days, mon to sun (comma-separated or repeated); with --repeat weekly, the repeat's days")
	fl.StringVar(&w.start, "start", "", "when it starts, HH:MM in the schedule's time zone")
	fl.StringVar(&w.end, "end", "", "when it ends, HH:MM (at or before --start: the next day)")
	fl.StringVar(&w.repeat, "repeat", "", "the recurrence's pattern: daily, weekly, monthly or cron (in place of --days)")
	fl.IntVar(&w.interval, "interval", 0, "with --repeat: every n days, weeks or months")
	fl.IntVar(&w.day, "day", 0, "with --repeat monthly: the day of the month")
	fl.IntVar(&w.nth, "nth", 0, "with --repeat monthly: the nth --weekday of the month, -1 for the last")
	fl.StringVar(&w.weekday, "weekday", "", "with --repeat monthly and --nth: the day, mon to sun")
	fl.StringVar(&w.cron, "cron", "", "with --repeat cron: the expression, minute hour day-of-month month day-of-week")
	fl.IntVar(&w.minutes, "minutes", 0, "with --repeat cron: how long each occurrence lasts, in minutes")
	fl.StringVar(&w.from, "from", "", "the first date it occurs, YYYY-MM-DD")
	fl.StringVar(&w.until, "until", "", "the last date it occurs, YYYY-MM-DD")
	fl.StringVar(&w.wake, "wake", "", "a nap's wake: never, or header (a request with the x-dormeto-wake header wakes it)")
	fl.StringVar(&w.timeZone, "time-zone", "", "the time zone of a workload without a schedule, an IANA name such as Europe/Paris")
}

// change checks the flags' forms and gives the window to add.
func (w *windowFlags) change(c *cobra.Command) (schedule.Change, error) {
	fl := c.Flags()
	if w.kind != "nap" && w.kind != "awake" {
		return schedule.Change{}, usagef("--kind: nap or awake")
	}
	nw := schedule.NewWindow{Kind: w.kind, Start: w.start, End: w.end, From: w.from, Until: w.until, Wake: w.wake}
	for _, t := range []struct{ flag, value string }{{"--start", w.start}, {"--end", w.end}} {
		if t.value != "" {
			if err := schedule.CheckTime(t.flag, t.value); err != nil {
				return schedule.Change{}, usageError{err}
			}
		}
	}
	for _, d := range []struct{ flag, value string }{{"--from", w.from}, {"--until", w.until}} {
		if d.value != "" {
			if err := schedule.CheckDate(d.flag, d.value); err != nil {
				return schedule.Change{}, usageError{err}
			}
		}
	}
	days := items(w.days)
	if w.repeat == "" {
		for _, name := range []string{"interval", "day", "nth", "weekday", "cron", "minutes"} {
			if fl.Changed(name) {
				return schedule.Change{}, usagef("--%s: needs --repeat", name)
			}
		}
		nw.Days = days
		return schedule.Change{Add: &nw, TimeZone: w.timeZone}, nil
	}
	r := schedule.NewRepeat{Pattern: w.repeat, Weekday: w.weekday, Cron: w.cron, Days: days}
	given := func(name string, v int) *int {
		if !fl.Changed(name) {
			return nil
		}
		return &v
	}
	r.Interval, r.Day, r.Nth, r.Minutes = given("interval", w.interval), given("day", w.day), given("nth", w.nth), given("minutes", w.minutes)
	nw.Repeat = &r
	return schedule.Change{Add: &nw, TimeZone: w.timeZone}, nil
}

// removeFlags are remove's --window.
type removeFlags struct{ window int }

func (r *removeFlags) bind(cmd *cobra.Command) {
	cmd.Flags().IntVar(&r.window, "window", 0, "the window's number, as schedule show numbers them (required)")
}

func (r *removeFlags) change(*cobra.Command) (schedule.Change, error) {
	if err := checkWindow(r.window); err != nil {
		return schedule.Change{}, err
	}
	return schedule.Change{Remove: r.window}, nil
}

func checkWindow(n int) error {
	if n < 1 {
		return usagef("--window: a window's number, from 1 (schedule show numbers them)")
	}
	return nil
}

// exceptFlags are except's --date, --window and --remove.
type exceptFlags struct {
	dates  []string
	window int
	remove bool
}

func (e *exceptFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringSliceVar(&e.dates, "date", nil, "a date YYYY-MM-DD in the schedule's time zone (comma-separated or repeated; required)")
	fl.IntVar(&e.window, "window", 0, "the dates of this window only, by its number as schedule show numbers them (default: the schedule's)")
	fl.BoolVar(&e.remove, "remove", false, "take the dates out")
}

func (e *exceptFlags) change(c *cobra.Command) (schedule.Change, error) {
	dates := items(e.dates)
	if len(dates) == 0 {
		return schedule.Change{}, usagef("--date: at least one date YYYY-MM-DD is needed")
	}
	for _, d := range dates {
		if err := schedule.CheckDate("--date", d); err != nil {
			return schedule.Change{}, usageError{err}
		}
	}
	if c.Flags().Changed("window") {
		if err := checkWindow(e.window); err != nil {
			return schedule.Change{}, err
		}
	}
	return schedule.Change{Except: &schedule.Exceptions{Dates: dates, Window: e.window, Remove: e.remove}}, nil
}

// editFlags are edit's: the schedule's own fields, named after them.
type editFlags struct{ timeZone, joinMinutes, maxNapDays string }

func (e *editFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&e.timeZone, "time-zone", "", "the schedule's time zone, an IANA name such as Europe/Paris")
	fl.StringVar(&e.joinMinutes, "join-minutes", "", "the largest gap that joins two windows of one kind, in minutes, or inherit")
	fl.StringVar(&e.maxNapDays, "max-nap-days", "", "the longest nap, in days, before a maintenance wake")
}

func (e *editFlags) change(c *cobra.Command) (schedule.Change, error) {
	fl := c.Flags()
	fields := map[string]json.RawMessage{}
	if fl.Changed("time-zone") {
		fields["timeZone"] = nil
		if e.timeZone != "" {
			fields["timeZone"], _ = json.Marshal(e.timeZone)
		}
	}
	for _, n := range []struct{ flag, field, value string }{{"join-minutes", "joinMinutes", e.joinMinutes}, {"max-nap-days", "maxNapDays", e.maxNapDays}} {
		if !fl.Changed(n.flag) {
			continue
		}
		if n.value == "" || (n.flag == "join-minutes" && n.value == "inherit") {
			fields[n.field] = nil
			continue
		}
		v, err := strconv.ParseUint(n.value, 10, 31)
		if err != nil {
			if n.flag == "join-minutes" {
				return schedule.Change{}, usagef("--join-minutes: %q is not a number of minutes or inherit", n.value)
			}
			return schedule.Change{}, usagef("--%s: %q is not a whole number", n.flag, n.value)
		}
		fields[n.field] = json.RawMessage(strconv.FormatUint(v, 10))
	}
	if len(fields) == 0 {
		return schedule.Change{}, usagef("name a field to change: --time-zone, --join-minutes or --max-nap-days")
	}
	return schedule.Change{Fields: fields}, nil
}
