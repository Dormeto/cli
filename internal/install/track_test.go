package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cogtypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/smithy-go"
)

const trkID = "dmt-prod-install:1234abcd-0000-4000-8000-000000000000"

// trkPoll is what one poll of the build shows: its status and phase, and how many log lines exist by then.
type trkPoll struct {
	status cbtypes.StatusType
	phase  string
	lines  int
	err    error
}

// trkWorld is a build followed poll by poll, with its log.
type trkWorld struct {
	polls    []trkPoll
	log      []string
	at       []time.Duration // each line's timestamp, from the build's start (default 0)
	pageSize int
	calls    int // BatchGetBuilds calls
	logCalls int
	clock    time.Time
	noLogs   bool
	late     int // lines that reach the log only after the build ended and a delay
	extra    int
}

func newTrkWorld(polls ...trkPoll) *trkWorld {
	return &trkWorld{polls: polls, pageSize: 1000, clock: bldT0}
}

func (w *trkWorld) clients() Clients { return Clients{CodeBuild: trkCB{w: w}, Logs: trkLogs{w: w}} }

func (w *trkWorld) now() time.Time { return w.clock }

// every advances the fake clock by d and answers at once.
func (w *trkWorld) every(d time.Duration) <-chan time.Time {
	w.clock = w.clock.Add(d)
	if w.calls > 0 && w.poll().status != cbtypes.StatusTypeInProgress && w.poll().err == nil {
		w.extra = w.late
	}
	c := make(chan time.Time)
	close(c)
	return c
}

func (w *trkWorld) poll() trkPoll { return w.polls[min(w.calls-1, len(w.polls)-1)] }

type trkCB struct {
	CodeBuild
	w *trkWorld
}

func (f trkCB) BatchGetBuilds(_ context.Context, in *codebuild.BatchGetBuildsInput, _ ...func(*codebuild.Options)) (*codebuild.BatchGetBuildsOutput, error) {
	f.w.calls++
	p := f.w.poll()
	if p.err != nil {
		return nil, p.err
	}
	b := bldBuild(in.Ids[0], p.status, bldT0)
	b.CurrentPhase = aws.String(p.phase)
	if !f.w.noLogs {
		b.Logs = &cbtypes.LogsLocation{GroupName: aws.String("/aws/codebuild/dmt-prod-install"), StreamName: aws.String("stream")}
	}
	return &codebuild.BatchGetBuildsOutput{Builds: []cbtypes.Build{b}}, nil
}

type trkLogs struct {
	Logs
	w *trkWorld
}

func (f trkLogs) GetLogEvents(_ context.Context, in *cloudwatchlogs.GetLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetLogEventsOutput, error) {
	f.w.logCalls++
	from := 0
	if in.NextToken == nil {
		if in.StartFromHead == nil || !*in.StartFromHead {
			return nil, errors.New("first read not from the head")
		}
	} else if _, err := fmt.Sscanf(*in.NextToken, "t%d", &from); err != nil {
		return nil, err
	}
	visible := min(f.w.poll().lines+f.w.extra, len(f.w.log))
	to := min(visible, from+f.w.pageSize)
	to = max(to, from)
	out := &cloudwatchlogs.GetLogEventsOutput{NextForwardToken: aws.String(fmt.Sprintf("t%d", to))}
	for i := from; i < to; i++ {
		var at time.Duration
		if i < len(f.w.at) {
			at = f.w.at[i]
		}
		out.Events = append(out.Events, cwtypes.OutputLogEvent{Message: aws.String(f.w.log[i]), Timestamp: aws.Int64(bldT0.Add(at).UnixMilli())})
	}
	return out, nil
}

// trkStatus records a terminal's status line.
type trkStatus struct {
	updates []string
	end     string
}

func (s *trkStatus) Update(l string) { s.updates = append(s.updates, l) }
func (s *trkStatus) End(l string)    { s.end = l }

func trkOpts(w *trkWorld, s *trkStatus, out io.Writer) FollowOptions {
	o := FollowOptions{Label: "Plan", Total: 3, Out: out, Every: w.every, Now: w.now}
	if s != nil {
		o.Status = s
	}
	return o
}

func trkPhases() []trkPoll {
	return []trkPoll{
		{cbtypes.StatusTypeInProgress, "PROVISIONING", 0, nil},
		{cbtypes.StatusTypeInProgress, "BUILD", 2, nil},
		{cbtypes.StatusTypeInProgress, "BUILD", 4, nil},
		{cbtypes.StatusTypeInProgress, "BUILD", 6, nil},
		{cbtypes.StatusTypeSucceeded, "COMPLETED", 8, nil},
	}
}

func trkLog() []string {
	return []string{
		"terraform init",
		"aws_a.b: Creating...",
		"aws_a.b: Creation complete after 2s [id=a]",
		"aws_x.y: Still creating... [12s elapsed]",
		"aws_x.y: Creation complete after 14s [id=x]",
		"aws_z.w: Creating...",
		"aws_z.w: Creation complete after 1s [id=z]",
		"Apply complete!",
	}
}

func TestFollowStatusLine(t *testing.T) {
	w := newTrkWorld(trkPhases()...)
	w.log = trkLog()
	w.at = []time.Duration{0, 3 * time.Second}
	s := &trkStatus{}
	res, err := Follow(context.Background(), w.clients(), Names("111122223333", "eu-west-1", "prod"), trkID, trkOpts(w, s, &bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed || res.Status != "SUCCEEDED" || res.Tail != nil {
		t.Errorf("result %+v", res)
	}
	all := strings.Join(s.updates, "\n")
	for _, want := range []string{
		"Plan (build dmt-prod-install:1234…)  provisioning  0s",
		"Plan (build dmt-prod-install:1234…)  0/3  creating aws_a.b (2s)  5s",
		"Plan (build dmt-prod-install:1234…)  1/3  creating aws_x.y (12s)  10s",
		"2/3  creating aws_z.w",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in\n%s", want, all)
		}
	}
	if s.end != "Plan (build dmt-prod-install:1234…)  succeeded  20s" {
		t.Errorf("end %q", s.end)
	}
}

func TestFollowWithoutTotal(t *testing.T) {
	w := newTrkWorld(trkPhases()...)
	w.log = trkLog()
	s := &trkStatus{}
	o := trkOpts(w, s, &bytes.Buffer{})
	o.Total = 0
	if _, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, o); err != nil {
		t.Fatal(err)
	}
	if all := strings.Join(s.updates, "\n"); !strings.Contains(all, "  1  creating aws_x.y (12s)") || strings.Contains(all, "1/") {
		t.Errorf("updates\n%s", all)
	}
}

func TestFollowFailedKeepsTheLastForty(t *testing.T) {
	var log []string
	for i := 1; i <= 60; i++ {
		log = append(log, fmt.Sprintf("line %d", i))
	}
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 30, nil}, trkPoll{cbtypes.StatusTypeFailed, "COMPLETED", 60, nil})
	w.log = log
	s := &trkStatus{}
	res, err := Follow(context.Background(), w.clients(), Names("111122223333", "eu-west-1", "prod"), trkID, trkOpts(w, s, &bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed || res.Status != "FAILED" {
		t.Errorf("result %+v", res)
	}
	if len(res.Tail) != 40 || res.Tail[0] != "line 21" || res.Tail[39] != "line 60" {
		t.Errorf("tail %q", res.Tail)
	}
	want := "https://eu-west-1.console.aws.amazon.com/codesuite/codebuild/111122223333/projects/dmt-prod-install/build/dmt-prod-install:1234abcd-0000-4000-8000-000000000000/?region=eu-west-1"
	if res.ConsoleURL != want {
		t.Errorf("console %q", res.ConsoleURL)
	}
	if !strings.HasSuffix(s.end, "failed  5s") {
		t.Errorf("end %q", s.end)
	}
}

func TestConsoleURLEscapesTheID(t *testing.T) {
	got := ConsoleURL(Names("1", "eu-west-1", "prod"), "dmt-prod-install:a/b c")
	if !strings.Contains(got, "/build/dmt-prod-install:a%2Fb%20c/?region=eu-west-1") {
		t.Errorf("%q", got)
	}
}

func TestFollowPagesEveryLineOnce(t *testing.T) {
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 5, nil}, trkPoll{cbtypes.StatusTypeSucceeded, "COMPLETED", 7, nil})
	w.log = []string{"a", "b", "c", "d", "e", "f", "g"}
	w.pageSize = 2
	var out bytes.Buffer
	if _, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &out)); err != nil {
		t.Fatal(err)
	}
	for _, l := range w.log {
		if c := strings.Count(out.String(), l+"\n"); c != 1 {
			t.Errorf("%q written %d times", l, c)
		}
	}
}

func TestFollowStripsANSI(t *testing.T) {
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 1, nil}, trkPoll{cbtypes.StatusTypeSucceeded, "COMPLETED", 1, nil})
	w.log = []string{"\x1b[1m\x1b[32maws_x.y: Creation complete after 2s\x1b[0m"}
	s := &trkStatus{}
	var out bytes.Buffer
	o := trkOpts(w, s, &out)
	o.Lines = true
	if _, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, o); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "aws_x.y: Creation complete after 2s\n") {
		t.Errorf("out %q", out.String())
	}
}

func TestFollowCountsLinesWithEscapes(t *testing.T) {
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 1, nil}, trkPoll{cbtypes.StatusTypeSucceeded, "COMPLETED", 1, nil})
	w.log = []string{"\x1b[1m\x1b[32maws_x.y: Creation complete after 2s\x1b[0m"}
	s := &trkStatus{}
	if _, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, s, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if all := strings.Join(s.updates, "\n"); !strings.Contains(all, "1/3") {
		t.Errorf("updates\n%s", all)
	}
}

func TestFollowWithoutTerminalWritesLines(t *testing.T) {
	w := newTrkWorld(trkPhases()...)
	w.log = trkLog()
	var out bytes.Buffer
	if _, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &out)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\r") {
		t.Errorf("carriage return in %q", out.String())
	}
	if !strings.Contains(out.String(), "aws_a.b: Creation complete after 2s [id=a]\n") || !strings.Contains(out.String(), "succeeded") {
		t.Errorf("out %q", out.String())
	}
}

func TestFollowLinesInATerminalHasNoStatusLine(t *testing.T) {
	w := newTrkWorld(trkPhases()...)
	w.log = trkLog()
	s := &trkStatus{}
	var out bytes.Buffer
	o := trkOpts(w, s, &out)
	o.Lines = true
	if _, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, o); err != nil {
		t.Fatal(err)
	}
	if len(s.updates) != 0 || s.end != "" {
		t.Errorf("status %+v", s)
	}
	if !strings.Contains(out.String(), "Apply complete!\n") || !strings.Contains(out.String(), "succeeded") {
		t.Errorf("out %q", out.String())
	}
}

func TestFollowDetachesOnCancel(t *testing.T) {
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 0, nil})
	ctx, cancel := context.WithCancel(context.Background())
	o := trkOpts(w, &trkStatus{}, &bytes.Buffer{})
	o.Every = func(d time.Duration) <-chan time.Time {
		cancel()
		return w.every(d)
	}
	res, err := Follow(ctx, w.clients(), Names("1", "eu-west-1", "prod"), trkID, o)
	if !errors.Is(err, context.Canceled) || res.Status != "IN_PROGRESS" || res.Failed {
		t.Errorf("res %+v err %v", res, err)
	}
	if w.calls != 1 {
		t.Errorf("%d polls", w.calls)
	}
}

func trkAPIErr(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code + " message"}
}

func TestFollowCredentialsExpired(t *testing.T) {
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 0, nil}, trkPoll{err: trkAPIErr("ExpiredTokenException")})
	_, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &bytes.Buffer{}))
	var ce *CredentialsError
	if !errors.As(err, &ce) {
		t.Fatalf("err %v", err)
	}
	want := "the AWS credentials expired while following build " + trkID + "; the build goes on: sign in again (aws sso login --profile <your profile>), then run dormeto install status"
	if err.Error() != want {
		t.Errorf("message %q", err)
	}
	if w.calls != 2 {
		t.Errorf("%d polls: not at once", w.calls)
	}
}

func TestFollowCredentialsByMessage(t *testing.T) {
	for _, e := range []error{errors.New("the SSO session has expired or is invalid"), errors.New("failed to refresh cached credentials, x"), trkAPIErr("InvalidClientTokenId")} {
		w := newTrkWorld(trkPoll{err: e})
		_, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &bytes.Buffer{}))
		var ce *CredentialsError
		if !errors.As(err, &ce) {
			t.Errorf("%v: err %v", e, err)
		}
	}
}

func TestFollowRetriesTransientErrors(t *testing.T) {
	boom := errors.New("boom")
	w := newTrkWorld(trkPoll{err: boom}, trkPoll{err: boom}, trkPoll{cbtypes.StatusTypeSucceeded, "COMPLETED", 0, nil})
	res, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &bytes.Buffer{}))
	if err != nil || res.Status != "SUCCEEDED" {
		t.Errorf("res %+v err %v", res, err)
	}
	w = newTrkWorld(trkPoll{err: boom})
	_, err = Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &bytes.Buffer{}))
	if !errors.Is(err, boom) || w.calls != 3 {
		t.Errorf("err %v after %d polls", err, w.calls)
	}
}

func TestFollowTransientCountIsConsecutive(t *testing.T) {
	boom := errors.New("boom")
	w := newTrkWorld(trkPoll{err: boom}, trkPoll{err: boom}, trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 0, nil}, trkPoll{err: boom}, trkPoll{err: boom}, trkPoll{cbtypes.StatusTypeSucceeded, "COMPLETED", 0, nil})
	if res, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &bytes.Buffer{})); err != nil || res.Status != "SUCCEEDED" {
		t.Errorf("res %+v err %v", res, err)
	}
}

func TestDurations(t *testing.T) {
	for d, want := range map[time.Duration]string{45 * time.Second: "45s", 192 * time.Second: "3m12s", 725 * time.Second: "12m05s", 0: "0s"} {
		if got := formatDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

// trkHTTP answers each GET with the next status.
type trkHTTP struct {
	statuses []int
	urls     []string
}

func (h *trkHTTP) Do(r *http.Request) (*http.Response, error) {
	h.urls = append(h.urls, r.URL.String())
	code := h.statuses[min(len(h.urls)-1, len(h.statuses)-1)]
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
}

type trkCognito struct {
	Cognito
	users map[string]string // email → status
}

func (f trkCognito) ListUsers(_ context.Context, in *cognitoidentityprovider.ListUsersInput, _ ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.ListUsersOutput, error) {
	if aws.ToString(in.UserPoolId) != "eu-west-1_abc" {
		return nil, errors.New("wrong pool")
	}
	out := &cognitoidentityprovider.ListUsersOutput{}
	for email, status := range f.users {
		if aws.ToString(in.Filter) == fmt.Sprintf("email = %q", email) {
			out.Users = append(out.Users, cogtypes.UserType{UserStatus: cogtypes.UserStatusType(status)})
		}
	}
	return out, nil
}

func trkVerifyOpts(w *trkWorld) VerifyOptions {
	return VerifyOptions{Wait: 10 * time.Minute, Poll: 15 * time.Second, Every: w.every, Now: w.now}
}

var trkOut = Outputs{DashboardURL: "https://dash.example.com", UserPoolID: "eu-west-1_abc"}

func TestVerifyDashboardAnswers(t *testing.T) {
	w := newTrkWorld()
	h := &trkHTTP{statuses: []int{403, 200}}
	checks := Verify(context.Background(), Clients{}, h, trkOut, nil, trkVerifyOpts(w))
	if len(checks) != 1 || checks[0] != (Check{"dashboard", "https://dash.example.com answers", OK}) {
		t.Errorf("checks %+v", checks)
	}
	if len(h.urls) != 2 || h.urls[0] != "https://dash.example.com" {
		t.Errorf("urls %v", h.urls)
	}
}

func TestVerifyDashboardNeverAnswers(t *testing.T) {
	w := newTrkWorld()
	h := &trkHTTP{statuses: []int{403}}
	checks := Verify(context.Background(), Clients{}, h, trkOut, nil, trkVerifyOpts(w))
	want := Check{"dashboard", "https://dash.example.com does not answer yet (a new CloudFront distribution can take some more minutes)", Warn}
	if len(checks) != 1 || checks[0] != want {
		t.Errorf("checks %+v", checks)
	}
	if got := w.clock.Sub(bldT0); got < 10*time.Minute || got > 10*time.Minute+15*time.Second {
		t.Errorf("waited %v", got)
	}
}

func TestVerifyAdministrators(t *testing.T) {
	w := newTrkWorld()
	cog := trkCognito{users: map[string]string{"a@x.com": "FORCE_CHANGE_PASSWORD", "b@x.com": "CONFIRMED", "d@x.com": "RESET_REQUIRED"}}
	h := &trkHTTP{statuses: []int{200}}
	checks := Verify(context.Background(), Clients{Cognito: cog}, h, trkOut, []string{"a@x.com", "b@x.com", "d@x.com", "c@x.com"}, trkVerifyOpts(w))
	if len(checks) != 2 {
		t.Fatalf("checks %+v", checks)
	}
	want := Check{"administrators", "invitation sent to a@x.com (valid 7 days); b@x.com signed in; RESET_REQUIRED; c@x.com is not in the user pool", Warn}
	if checks[1] != want {
		t.Errorf("got %+v", checks[1])
	}
	checks = Verify(context.Background(), Clients{Cognito: cog}, h, trkOut, []string{"a@x.com", "b@x.com"}, trkVerifyOpts(w))
	if checks[1].Verdict != OK || checks[1].Detail != "invitation sent to a@x.com (valid 7 days); b@x.com signed in" {
		t.Errorf("got %+v", checks[1])
	}
}

func TestFollowEndsTheStatusLineOnDetach(t *testing.T) {
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 0, nil})
	ctx, cancel := context.WithCancel(context.Background())
	s := &trkStatus{}
	o := trkOpts(w, s, &bytes.Buffer{})
	o.Every = func(d time.Duration) <-chan time.Time {
		cancel()
		return w.every(d)
	}
	if _, err := Follow(ctx, w.clients(), Names("1", "eu-west-1", "prod"), trkID, o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.end != "Plan (build dmt-prod-install:1234…)  detached" {
		t.Errorf("end %q", s.end)
	}
}

func TestFollowEndsTheStatusLineOnErrors(t *testing.T) {
	for name, e := range map[string]error{"credentials": trkAPIErr("ExpiredTokenException"), "retries": errors.New("boom")} {
		w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 0, nil}, trkPoll{err: e})
		s := &trkStatus{}
		if _, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, s, &bytes.Buffer{})); err == nil {
			t.Fatal(name, "no error")
		}
		if s.end != "Plan (build dmt-prod-install:1234…)  stopped following" {
			t.Errorf("%s: end %q", name, s.end)
		}
	}
}

func TestFollowReadsTheLogOnceMoreAfterAFailure(t *testing.T) {
	w := newTrkWorld(trkPoll{cbtypes.StatusTypeInProgress, "BUILD", 1, nil}, trkPoll{cbtypes.StatusTypeFailed, "COMPLETED", 2, nil})
	w.log = []string{"one", "two", "late error", "last line"}
	w.late = 2
	res, err := Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, &trkStatus{}, &bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Tail, "|"); got != "one|two|late error|last line" {
		t.Errorf("tail %q", got)
	}
	// a success does not wait
	w = newTrkWorld(trkPoll{cbtypes.StatusTypeSucceeded, "COMPLETED", 0, nil})
	Follow(context.Background(), w.clients(), Names("1", "eu-west-1", "prod"), trkID, trkOpts(w, nil, &bytes.Buffer{}))
	if !w.clock.Equal(bldT0) {
		t.Errorf("waited %v on success", w.clock.Sub(bldT0))
	}
}

func TestVerifyDashboardInterrupted(t *testing.T) {
	want := Check{"dashboard", "not checked (interrupted)", Skip}
	w := newTrkWorld()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &trkHTTP{statuses: []int{403}}
	if checks := Verify(ctx, Clients{}, h, trkOut, nil, trkVerifyOpts(w)); checks[0] != want || len(h.urls) != 0 {
		t.Errorf("checks %+v, %d GETs", checks, len(h.urls))
	}
	// a cancel during the GET
	ctx, cancel = context.WithCancel(context.Background())
	h2 := &trkCancelHTTP{cancel: cancel}
	if checks := Verify(ctx, Clients{}, h2, trkOut, nil, trkVerifyOpts(w)); checks[0] != want || h2.n != 1 {
		t.Errorf("checks %+v, %d GETs", checks, h2.n)
	}
}

type trkCancelHTTP struct {
	cancel func()
	n      int
}

func (h *trkCancelHTTP) Do(*http.Request) (*http.Response, error) {
	h.n++
	h.cancel()
	return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(""))}, nil
}

type trkFailingCognito struct {
	Cognito
	errs  map[string]error
	calls []string
}

func (f *trkFailingCognito) ListUsers(_ context.Context, in *cognitoidentityprovider.ListUsersInput, _ ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.ListUsersOutput, error) {
	f.calls = append(f.calls, aws.ToString(in.Filter))
	for email, err := range f.errs {
		if aws.ToString(in.Filter) == fmt.Sprintf("email = %q", email) {
			return nil, err
		}
	}
	return &cognitoidentityprovider.ListUsersOutput{Users: []cogtypes.UserType{{UserStatus: "CONFIRMED"}}}, nil
}

func TestVerifyAdministratorsCredentialsExpired(t *testing.T) {
	w := newTrkWorld()
	cog := &trkFailingCognito{errs: map[string]error{"a@x.com": trkAPIErr("ExpiredTokenException")}}
	checks := Verify(context.Background(), Clients{Cognito: cog}, &trkHTTP{statuses: []int{200}}, trkOut, []string{"a@x.com", "b@x.com"}, trkVerifyOpts(w))
	want := Check{"administrators", "not checked (the AWS credentials expired: sign in again, then dormeto install status)", Warn}
	if checks[1] != want || len(cog.calls) != 1 {
		t.Errorf("got %+v after %d calls", checks[1], len(cog.calls))
	}
}

func TestVerifyAdministratorsOtherErrorsStayPerAddress(t *testing.T) {
	w := newTrkWorld()
	cog := &trkFailingCognito{errs: map[string]error{"a@x.com": errors.New("boom")}}
	checks := Verify(context.Background(), Clients{Cognito: cog}, &trkHTTP{statuses: []int{200}}, trkOut, []string{"a@x.com", "b@x.com"}, trkVerifyOpts(w))
	want := Check{"administrators", "a@x.com: boom; b@x.com signed in", Warn}
	if checks[1] != want {
		t.Errorf("got %+v", checks[1])
	}
}
