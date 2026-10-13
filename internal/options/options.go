// Package options parses the estimate command's options, ported from the Node estimate
// (manager packages/estimate/src/options.ts): the same options, values, defaults, checks and messages, in the same order
// of checks, so a bad input gives the same first message. Only the usage names `dormeto estimate`.
package options

import (
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zones known on a machine without a zone database (Windows)

	"github.com/Dormeto/cli/internal/prices"
)

// Options is the estimate command's options. Marshaled, it is the report's "options": Node's Options without out and
// profiles, field for field and in Node's order.
type Options struct {
	// One AWS profile or more: the accounts each reaches, merged.
	Profiles []string `json:"-"`
	// The organization's accounts the estimate is restricted to (--accounts): nil, every account reached.
	Accounts []string `json:"accounts"`
	Regions  []string `json:"regions"`
	// Workloads left out by their tags (--exclude-tags, repeated): any criterion excludes; in one, every key (and), each
	// with one of its values (empty: any value). Never nil.
	ExcludeTags []map[string][]string `json:"excludeTags"`
	Ec2Uptime   bool                  `json:"ec2Uptime"`
	EcsUptime   bool                  `json:"ecsUptime"`
	RdsUptime   bool                  `json:"rdsUptime"`
	Role        string                `json:"role"`
	Baseline    prices.Baseline       `json:"baseline"`
	Discounts   prices.Discounts      `json:"discounts"`
	// "discovery", "metrics" or nil.
	Refresh *string `json:"refresh"`
	Out     string  `json:"-"`
	// The idle window, in minutes, the report's figures use.
	Window int `json:"window"`
	// nil: none (no limit).
	MaxWakes    *float64 `json:"maxWakes"`
	MinSaving   *float64 `json:"minSaving"`
	Parallel    int      `json:"parallel"`
	MaxWakesRds *float64 `json:"maxWakesRds"`
}

// UsageError is a bad option: the command prints its message and Usage, and exits 2.
type UsageError struct{ Message string }

func (e *UsageError) Error() string { return e.Message }

func usageErr(message string) error { return &UsageError{Message: message} }

// ErrHelp is what Parse returns for --help or -h: the command prints Usage and exits 0.
var ErrHelp = errors.New("help requested")

// Usage is Node's USAGE with `npm run estimate --` replaced by `dormeto estimate`.
const Usage = `Usage: dormeto estimate --profile <profile>[,<profile>...] [--accounts 111111111111,222222222222]
  [--regions eu-west-1,eu-west-3] [--exclude-tags 'env=dev|test,team=web' ...] [--uptime | --ec2-uptime | --ecs-uptime | --rds-uptime] [--role <name>]
  [--baseline always|business-hours] [--timezone Europe/Paris] [--discount-compute 0-90] [--discount-databases 0-90]
  [--refresh discovery|metrics] [--window 60] [--max-wakes 3|none]
  [--min-saving 1|none] [--max-wakes-rds 2|none] [--parallel 5] [--out <dir>]`

// Accounts created by AWS Organizations have it; the role is supposed to be there.
const defaultRole = "OrganizationAccountAccessRole"

var (
	regionRe  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)
	accountRe = regexp.MustCompile(`^\d{12}$`)
	wholeRe   = regexp.MustCompile(`^\d+$`)
	decimalRe = regexp.MustCompile(`^\d+(\.\d+)?$`)
)

// Options without a value; --uptime is every uptime option.
var flagNames = []string{"uptime", "ec2-uptime", "ecs-uptime", "rds-uptime"}
var valueNames = []string{"profile", "accounts", "regions", "exclude-tags", "role", "baseline", "timezone", "discount-compute", "discount-databases", "refresh", "out", "window", "max-wakes", "min-saving", "parallel", "max-wakes-rds"}

const (
	windowMin, windowMax, windowDefault       = 5, 1440, 60
	parallelMin, parallelMax, parallelDefault = 1, 50, 5
)

func percent(name string, value string, given bool) (float64, error) {
	if !given {
		return 0, nil
	}
	n, _ := strconv.ParseFloat(value, 64)
	if !wholeRe.MatchString(value) || n > 90 {
		return 0, usageErr("--" + name + ": a whole number from 0 to 90")
	}
	return n, nil
}

// splitList splits on commas, trims and drops the empty items (JS: split(',').map(trim).filter(Boolean)).
func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func unique(items []string) []string {
	out := []string{}
	for _, item := range items {
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func trimmedSplit(value, sep string) []string {
	parts := strings.Split(value, sep)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// tagCriterion is one --exclude-tags: key=value|value or key alone, comma-separated.
func tagCriterion(value string) (map[string][]string, error) {
	terms := splitList(value)
	if len(terms) == 0 {
		return nil, usageErr("--exclude-tags: one tag or more, such as env=dev|test,team=web")
	}
	criterion := map[string][]string{}
	for _, term := range terms {
		at := strings.Index(term, "=")
		key := term
		if at >= 0 {
			key = term[:at]
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, usageErr("--exclude-tags: a tag without its key: " + term)
		}
		rest := term[at+1:] // at = -1: the whole term, as JS slice(0)
		if previous, twice := criterion[key]; twice {
			both := trimmedSplit(rest, "|")
			return nil, usageErr("--exclude-tags: " + key + " twice in one criterion: " + key + "=" + strings.Join(append(slices.Clone(previous), both...), "|") + " for either")
		}
		if at < 0 {
			criterion[key] = []string{}
		} else {
			criterion[key] = trimmedSplit(rest, "|")
		}
	}
	return criterion, nil
}

// knownZone is Intl.DateTimeFormat's verdict, with time.LoadLocation: Go also takes "" (UTC) and "Local".
func knownZone(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// optionalNumber is a --max-wakes style value: a decimal, or none (nil).
func optionalNumber(value, message string) (*float64, error) {
	if value == "none" {
		return nil, nil
	}
	if !decimalRe.MatchString(value) {
		return nil, usageErr(message)
	}
	n, _ := strconv.ParseFloat(value, 64)
	return &n, nil
}

// Parse parses the arguments after "estimate", which cobra passes unparsed. --help or -h gives ErrHelp.
func Parse(args []string) (Options, error) {
	given := map[string]string{}
	var excludeTags []map[string][]string
	flags := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			return Options{}, ErrHelp
		}
		name := ""
		if strings.HasPrefix(arg, "--") {
			name = arg[2:]
		}
		if slices.Contains(flagNames, name) {
			flags[name] = true
			continue
		}
		if !slices.Contains(valueNames, name) {
			return Options{}, usageErr("unknown option: " + arg)
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return Options{}, usageErr("--" + name + " needs a value")
		}
		value := args[i+1]
		if name == "exclude-tags" {
			criterion, err := tagCriterion(value)
			if err != nil {
				return Options{}, err
			}
			excludeTags = append(excludeTags, criterion)
		} else {
			given[name] = value
		}
		i++
	}
	has := func(name string) bool { _, ok := given[name]; return ok }
	or := func(name, fallback string) string {
		if v, ok := given[name]; ok {
			return v
		}
		return fallback
	}

	if !has("profile") {
		return Options{}, usageErr("--profile is required")
	}
	profiles := unique(splitList(given["profile"]))
	if len(profiles) == 0 {
		return Options{}, usageErr("--profile: one AWS profile or more, such as qal,prod")
	}
	var accounts []string
	if has("accounts") {
		accounts = unique(splitList(given["accounts"]))
		if len(accounts) == 0 {
			return Options{}, usageErr("--accounts: one AWS account id or more, such as 111111111111,222222222222")
		}
		for _, a := range accounts {
			if !accountRe.MatchString(a) {
				return Options{}, usageErr("--accounts: not an AWS account id: " + a)
			}
		}
	}
	var regions []string
	if has("regions") {
		regions = splitList(given["regions"])
		if len(regions) == 0 {
			return Options{}, usageErr("--regions: one region or more, such as eu-west-1,us-east-1")
		}
		for _, r := range regions {
			if !regionRe.MatchString(r) {
				return Options{}, usageErr("--regions: unknown region " + r)
			}
		}
	}
	kind := or("baseline", "always")
	if kind != "always" && kind != "business-hours" {
		return Options{}, usageErr("--baseline: always or business-hours")
	}
	timeZone := or("timezone", "UTC")
	if !knownZone(timeZone) {
		return Options{}, usageErr("--timezone: unknown time zone " + timeZone)
	}
	var refresh *string
	if has("refresh") {
		r := given["refresh"]
		if r != "discovery" && r != "metrics" {
			return Options{}, usageErr("--refresh: discovery or metrics")
		}
		refresh = &r
	}
	windowText := or("window", strconv.Itoa(windowDefault))
	window, _ := strconv.Atoi(windowText)
	if !wholeRe.MatchString(windowText) || window < windowMin || window > windowMax {
		return Options{}, usageErr("--window: whole minutes from " + strconv.Itoa(windowMin) + " to " + strconv.Itoa(windowMax))
	}
	maxWakes, err := optionalNumber(or("max-wakes", "3"), "--max-wakes: a number of wakes a day, such as 3 or 0.5, or none")
	if err != nil {
		return Options{}, err
	}
	minSaving, err := optionalNumber(or("min-saving", "1"), "--min-saving: dollars a month, such as 1 or 0.5, or none")
	if err != nil {
		return Options{}, err
	}
	maxWakesRds, err := optionalNumber(or("max-wakes-rds", "2"), "--max-wakes-rds: a number of wakes a day, such as 2 or 0.5, or none")
	if err != nil {
		return Options{}, err
	}
	parallelText := or("parallel", strconv.Itoa(parallelDefault))
	parallel, _ := strconv.Atoi(parallelText)
	if !wholeRe.MatchString(parallelText) || parallel < parallelMin || parallel > parallelMax {
		return Options{}, usageErr("--parallel: accounts discovered at once, " + strconv.Itoa(parallelMin) + " to " + strconv.Itoa(parallelMax))
	}
	compute, err := percent("discount-compute", given["discount-compute"], has("discount-compute"))
	if err != nil {
		return Options{}, err
	}
	databases, err := percent("discount-databases", given["discount-databases"], has("discount-databases"))
	if err != nil {
		return Options{}, err
	}
	out := given["out"]
	if !has("out") {
		if out, err = os.Getwd(); err != nil {
			return Options{}, err
		}
	}
	baseline := prices.Baseline{Kind: kind}
	if kind != "always" {
		baseline.TimeZone = timeZone
	}
	if excludeTags == nil {
		excludeTags = []map[string][]string{}
	}
	all := flags["uptime"]
	return Options{
		Profiles: profiles, Accounts: accounts, Regions: regions, ExcludeTags: excludeTags,
		Ec2Uptime: all || flags["ec2-uptime"], EcsUptime: all || flags["ecs-uptime"], RdsUptime: all || flags["rds-uptime"],
		Role: or("role", defaultRole), Baseline: baseline, Discounts: prices.Discounts{Compute: compute, Databases: databases},
		Refresh: refresh, Out: out, Window: window, MaxWakes: maxWakes, MinSaving: minSaving, Parallel: parallel, MaxWakesRds: maxWakesRds,
	}, nil
}
