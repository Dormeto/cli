package install

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"

	"github.com/Dormeto/cli/internal/progress"
)

const (
	pollEvery     = 5 * time.Second
	tailLines     = 40
	maxFailures   = 3
	closingReads  = 3
	closingEvery  = 3 * time.Second
	statusRunning = "IN_PROGRESS"
)

// FollowOptions are how Follow shows a build.
type FollowOptions struct {
	Label  string          // "Plan", "Apply", "Destroy", "Organization access"
	Total  int             // resources the plan changes (0: unknown, no "n/total")
	Status progress.Status // nil: not a terminal → lines to Out
	Out    io.Writer
	Lines  bool // --verbose: Terraform's lines to Out even in a terminal (no status line)
	Every  func(time.Duration) <-chan time.Time
	Now    func() time.Time
	// Count tells whether a log line is one more done out of Total (organization.sh's line per account); nil: Terraform's
	// lines, the resource in progress shown.
	Count func(line string) bool
	// Closing is that the build ends with Terraform's closing line (an apply, a destroy): a success without it read is
	// waited for, the log's ingestion lagging the build's end.
	Closing bool
}

// Result is how a build ended.
type Result struct {
	Status     string // SUCCEEDED, FAILED, FAULT, TIMED_OUT, STOPPED, or IN_PROGRESS when detached
	Failed     bool
	Tail       []string // the log's last 40 lines (on a failure)
	ConsoleURL string
	// Applied is what Terraform's closing line said (Apply complete! or Destroy complete!); nil when the log has none
	// (cut, lost, or not an apply).
	Applied *PlanSummary
}

// CredentialsError is the AWS credentials expiring while a build is followed: the build goes on. The run that followed
// it gives the command that signs in again (Login) and the flags that name the install again (Args).
type CredentialsError struct {
	Build       string
	Err         error
	Login, Args string
}

func (e *CredentialsError) Error() string {
	login, status := e.Login, "dormeto install status"
	if login == "" {
		login = "aws sso login --profile <your profile>"
	}
	if e.Args != "" {
		status += " " + e.Args
	}
	return fmt.Sprintf("the AWS credentials expired while following build %s; the build goes on: sign in again (%s), then run %s", e.Build, login, status)
}

func (e *CredentialsError) Unwrap() error { return e.Err }

func isCredentials(err error) bool {
	switch apiCode(err) {
	case "ExpiredToken", "ExpiredTokenException", "InvalidClientTokenId", "UnrecognizedClientException":
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "SSO session has expired") || strings.Contains(msg, "failed to refresh cached credentials")
}

// ConsoleURL is the build's page in the AWS console.
func ConsoleURL(p Project, build string) string {
	return fmt.Sprintf("https://%s.console.aws.amazon.com/codesuite/codebuild/%s/projects/%s/build/%s/?region=%s",
		p.Region, p.Account, p.Name, url.PathEscape(build), p.Region)
}

var (
	ansiRe     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	startedRe  = regexp.MustCompile(`^(.+): (Creating|Modifying|Destroying)\.\.\.`)
	stillRe    = regexp.MustCompile(`^(.+): Still (creating|modifying|destroying)\.\.\. \[(.+) elapsed\]`)
	completeRe = regexp.MustCompile(`^(.+): (?:Creation|Modifications|Destruction) complete after `)
	appliedRe  = regexp.MustCompile(`^Apply complete! Resources: (?:\d+ imported, )?(\d+) added, (\d+) changed, (\d+) destroyed\.`)
	destroyRe  = regexp.MustCompile(`^Destroy complete! Resources: (\d+) destroyed\.`)
)

// formatDuration is 45s, 3m12s, 12m05s.
func formatDuration(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// tracker is what the log said so far.
type tracker struct {
	done    int
	seen    bool // a Terraform line was read
	verb    string
	address string
	since   time.Time
	applied *PlanSummary // Terraform's closing line, once read
}

func (t *tracker) read(line string, ts, now time.Time) {
	if m := appliedRe.FindStringSubmatch(line); m != nil {
		t.applied = &PlanSummary{Add: atoi(m[1]), Change: atoi(m[2]), Destroy: atoi(m[3])}
	} else if m := destroyRe.FindStringSubmatch(line); m != nil {
		t.applied = &PlanSummary{Destroy: atoi(m[1])}
	} else if m := completeRe.FindStringSubmatch(line); m != nil {
		t.done++
		t.seen = true
		if m[1] == t.address {
			t.verb, t.address = "", ""
		}
	} else if m := startedRe.FindStringSubmatch(line); m != nil {
		t.seen = true
		t.verb, t.address, t.since = strings.ToLower(m[2]), m[1], ts
	} else if m := stillRe.FindStringSubmatch(line); m != nil {
		t.seen = true
		t.verb, t.address, t.since = m[2], m[1], ts
		if d, err := time.ParseDuration(m[3]); err == nil {
			t.since = now.Add(-d)
		}
	}
}

// atoi reads the digits a pattern matched.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// follower is one Follow run.
type follower struct {
	c     Clients
	p     Project
	build string
	o     FollowOptions
	t     tracker
	token *string
	ring  []string
}

func (f *follower) head() string {
	project, id, _ := strings.Cut(f.build, ":")
	if id == "" {
		project, id = f.p.Name, f.build
	}
	return fmt.Sprintf("%s (build %s:%s…)", f.o.Label, project, id[:min(4, len(id))])
}

// readLog reads the new lines of the build's log stream.
func (f *follower) readLog(ctx context.Context, b cbtypes.Build) error {
	if b.Logs == nil || b.Logs.GroupName == nil || b.Logs.StreamName == nil {
		return nil
	}
	for {
		out, err := f.c.Logs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
			LogGroupName: b.Logs.GroupName, LogStreamName: b.Logs.StreamName, StartFromHead: aws.Bool(true), NextToken: f.token,
		})
		if isNotFound(err, "ResourceNotFoundException") {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range out.Events {
			line := strings.TrimRight(ansiRe.ReplaceAllString(aws.ToString(e.Message), ""), " \r\n")
			switch {
			case f.o.Count == nil:
				f.t.read(line, time.UnixMilli(aws.ToInt64(e.Timestamp)), f.o.Now())
			case f.o.Count(line):
				f.t.done++
				f.t.seen = true
			}
			f.ring = append(f.ring, line)
			if len(f.ring) > tailLines {
				f.ring = f.ring[1:]
			}
			if f.o.Status == nil || f.o.Lines {
				fmt.Fprintln(f.o.Out, line)
			}
		}
		next := out.NextForwardToken
		if next == nil || (f.token != nil && *next == *f.token) {
			return nil
		}
		f.token = next
	}
}

func (f *follower) statusLine(b cbtypes.Build, now time.Time) string {
	var total time.Duration
	if b.StartTime != nil {
		total = now.Sub(*b.StartTime)
	}
	elapsed := formatDuration(total)
	if !f.t.seen {
		return fmt.Sprintf("%s  %s  %s", f.head(), strings.ToLower(aws.ToString(b.CurrentPhase)), elapsed)
	}
	count := fmt.Sprint(f.t.done)
	if f.o.Total > 0 {
		count += fmt.Sprintf("/%d", f.o.Total)
	}
	if f.t.address == "" {
		return fmt.Sprintf("%s  %s  %s", f.head(), count, elapsed)
	}
	return fmt.Sprintf("%s  %s  %s %s (%s)  %s", f.head(), count, f.t.verb, f.t.address, formatDuration(now.Sub(f.t.since)), elapsed)
}

// step is one poll: the build, then its new log lines.
func (f *follower) step(ctx context.Context) (cbtypes.Build, error) {
	got, err := f.c.CodeBuild.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{f.build}})
	if err != nil {
		return cbtypes.Build{}, err
	}
	if len(got.Builds) == 0 {
		return cbtypes.Build{}, fmt.Errorf("build %s not found", f.build)
	}
	b := got.Builds[0]
	return b, f.readLog(ctx, b)
}

// Follow shows the build until it ends. A cancelled ctx detaches: it returns IN_PROGRESS and ctx's error, and never
// stops the build.
func Follow(ctx context.Context, c Clients, p Project, build string, o FollowOptions) (Result, error) {
	if o.Every == nil {
		o.Every = time.After
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	f := &follower{c: c, p: p, build: build, o: o}
	// leave ends the terminal's status line, so the caller's next message does not land on the redrawn line.
	leave := func(how string, err error) (Result, error) {
		if o.Status != nil && !o.Lines {
			o.Status.End(f.head() + "  " + how)
		}
		return Result{Status: statusRunning}, err
	}
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return leave("detached", err)
		}
		b, err := f.step(ctx)
		switch {
		case ctx.Err() != nil:
			return leave("detached", ctx.Err())
		case err != nil && isCredentials(err):
			return leave("stopped following", &CredentialsError{Build: build, Err: err})
		case err != nil:
			if failures++; failures >= maxFailures {
				return leave("stopped following", fmt.Errorf("follow build %s: %w", build, err))
			}
		default:
			failures = 0
			now := o.Now()
			if b.BuildStatus != cbtypes.StatusTypeInProgress {
				if b.BuildStatus == cbtypes.StatusTypeSucceeded && f.o.Closing {
					// The closing line may reach CloudWatch after the build's end: read again, 3 s apart, up to 3 times.
					for i := 0; i < closingReads && f.t.applied == nil; i++ {
						select {
						case <-ctx.Done():
							i = closingReads
						case <-o.Every(closingEvery):
							f.readLog(ctx, b) //nolint:errcheck // best effort: the fallback line says the plan
						}
					}
				} else if b.BuildStatus != cbtypes.StatusTypeSucceeded {
					// CloudWatch ingestion lags the build's end: read the stream once more before the tail is taken.
					select {
					case <-ctx.Done():
					case <-o.Every(pollEvery):
						f.readLog(ctx, b) //nolint:errcheck // best effort: the tail is what was read
					}
				}
				return f.end(b, now), nil
			}
			if o.Status != nil && !o.Lines {
				o.Status.Update(f.statusLine(b, now))
			}
		}
		select {
		case <-ctx.Done():
			return leave("detached", ctx.Err())
		case <-o.Every(pollEvery):
		}
	}
}

func (f *follower) end(b cbtypes.Build, now time.Time) Result {
	status := string(b.BuildStatus)
	var total time.Duration
	if b.StartTime != nil {
		total = now.Sub(*b.StartTime)
	}
	line := fmt.Sprintf("%s  %s  %s", f.head(), strings.ToLower(status), formatDuration(total))
	if f.o.Status != nil && !f.o.Lines {
		f.o.Status.End(line)
	} else {
		fmt.Fprintln(f.o.Out, line)
	}
	res := Result{Status: status, Failed: b.BuildStatus != cbtypes.StatusTypeSucceeded, Applied: f.t.applied}
	if res.Failed {
		res.Tail = append([]string(nil), f.ring...)
		res.ConsoleURL = ConsoleURL(f.p, f.build)
	}
	return res
}

// VerifyOptions are how long Verify waits for the dashboard.
type VerifyOptions struct {
	Wait, Poll time.Duration // 10 min, 15 s when zero; a negative Wait: one try, no wait
	Every      func(time.Duration) <-chan time.Time
	Now        func() time.Time
}

// Verify checks after an apply that the dashboard answers and the administrators were invited.
func Verify(ctx context.Context, c Clients, h HTTP, out Outputs, admins []string, o VerifyOptions) []Check {
	if o.Wait == 0 {
		o.Wait = 10 * time.Minute
	}
	if o.Poll == 0 {
		o.Poll = 15 * time.Second
	}
	if o.Every == nil {
		o.Every = time.After
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	checks := []Check{verifyDashboard(ctx, h, out.DashboardURL, o)}
	if len(admins) > 0 {
		checks = append(checks, verifyAdministrators(ctx, c, out.UserPoolID, admins))
	}
	return checks
}

func verifyDashboard(ctx context.Context, h HTTP, dashboard string, o VerifyOptions) Check {
	const name = "dashboard"
	deadline := o.Now().Add(o.Wait)
	interrupted := Check{name, "not checked (interrupted)", Skip}
	for {
		if ctx.Err() != nil {
			return interrupted
		}
		if req, err := http.NewRequestWithContext(ctx, http.MethodGet, dashboard, nil); err == nil {
			if resp, err := h.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return Check{name, dashboard + " answers", OK}
				}
			}
		}
		if ctx.Err() != nil {
			return interrupted
		}
		if !o.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return interrupted
		case <-o.Every(o.Poll):
		}
	}
	return Check{name, dashboard + " does not answer yet (a new CloudFront distribution can take some more minutes)", Warn}
}

func verifyAdministrators(ctx context.Context, c Clients, pool string, admins []string) Check {
	var details []string
	verdict := OK
	for _, a := range admins {
		out, err := c.Cognito.ListUsers(ctx, &cognitoidentityprovider.ListUsersInput{UserPoolId: &pool, Filter: aws.String(fmt.Sprintf("email = %q", a))})
		switch {
		case err != nil && isCredentials(err):
			return Check{"administrators", "not checked (the AWS credentials expired: sign in again, then dormeto install status)", Warn}
		case err != nil:
			details = append(details, a+": "+err.Error())
			verdict = Warn
		case len(out.Users) == 0:
			details = append(details, a+" is not in the user pool")
			verdict = Warn
		case out.Users[0].UserStatus == "FORCE_CHANGE_PASSWORD":
			details = append(details, "invitation sent to "+a+" (valid 7 days)")
		case out.Users[0].UserStatus == "CONFIRMED":
			details = append(details, a+" signed in")
		default:
			details = append(details, string(out.Users[0].UserStatus))
		}
	}
	return Check{"administrators", strings.Join(details, "; "), verdict}
}
