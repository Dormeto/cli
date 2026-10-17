package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// bldWorld is a CodeBuild project's builds: the list answers with every id, the details by id.
type bldWorld struct {
	builds  []cbtypes.Build // newest first
	started []*codebuild.StartBuildInput
	errs    []error // StartBuild's answers, in order; then success
	batches [][]string
}

func (w *bldWorld) clients() Clients { return Clients{CodeBuild: bldCB{w: w}} }

type bldCB struct {
	CodeBuild
	w *bldWorld
}

func (f bldCB) ListBuildsForProject(_ context.Context, in *codebuild.ListBuildsForProjectInput, _ ...func(*codebuild.Options)) (*codebuild.ListBuildsForProjectOutput, error) {
	if in.SortOrder != cbtypes.SortOrderTypeDescending {
		return nil, errors.New("not descending")
	}
	var ids []string
	for _, b := range f.w.builds {
		ids = append(ids, aws.ToString(b.Id))
	}
	return &codebuild.ListBuildsForProjectOutput{Ids: ids}, nil
}

func (f bldCB) BatchGetBuilds(_ context.Context, in *codebuild.BatchGetBuildsInput, _ ...func(*codebuild.Options)) (*codebuild.BatchGetBuildsOutput, error) {
	f.w.batches = append(f.w.batches, in.Ids)
	out := &codebuild.BatchGetBuildsOutput{}
	for _, id := range in.Ids {
		found := false
		for _, b := range f.w.builds {
			if aws.ToString(b.Id) == id {
				out.Builds = append(out.Builds, b)
				found = true
			}
		}
		if !found {
			out.BuildsNotFound = append(out.BuildsNotFound, id)
		}
	}
	return out, nil
}

func (f bldCB) StartBuild(_ context.Context, in *codebuild.StartBuildInput, _ ...func(*codebuild.Options)) (*codebuild.StartBuildOutput, error) {
	f.w.started = append(f.w.started, in)
	if n := len(f.w.started) - 1; n < len(f.w.errs) && f.w.errs[n] != nil {
		return nil, f.w.errs[n]
	}
	return &codebuild.StartBuildOutput{Build: &cbtypes.Build{Id: aws.String("dmt-prod-install:new")}}, nil
}

func bldBuild(id string, status cbtypes.StatusType, start time.Time) cbtypes.Build {
	return cbtypes.Build{Id: aws.String(id), BuildStatus: status, StartTime: aws.Time(start)}
}

var bldT0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func TestRunning(t *testing.T) {
	w := &bldWorld{builds: []cbtypes.Build{
		bldBuild("b3", cbtypes.StatusTypeSucceeded, bldT0), bldBuild("b2", cbtypes.StatusTypeInProgress, bldT0), bldBuild("b1", cbtypes.StatusTypeFailed, bldT0)}}
	got, err := Running(context.Background(), w.clients(), "dmt-prod-install")
	if err != nil || got != "b2" {
		t.Fatalf("got %q, %v", got, err)
	}
	w = &bldWorld{builds: []cbtypes.Build{bldBuild("b1", cbtypes.StatusTypeSucceeded, bldT0)}}
	if got, err := Running(context.Background(), w.clients(), "p"); err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := Running(context.Background(), (&bldWorld{}).clients(), "p"); err != nil || got != "" {
		t.Fatalf("none: got %q, %v", got, err)
	}
}

func TestRunningLooksAtTheFirstTen(t *testing.T) {
	w := &bldWorld{}
	for i := 0; i < 15; i++ {
		w.builds = append(w.builds, bldBuild(fmt.Sprintf("b%d", i), cbtypes.StatusTypeSucceeded, bldT0))
	}
	if _, err := Running(context.Background(), w.clients(), "p"); err != nil {
		t.Fatal(err)
	}
	if len(w.batches) != 1 || len(w.batches[0]) != 10 || w.batches[0][0] != "b0" {
		t.Fatalf("batches = %v", w.batches)
	}
}

func TestStartRefusesWhileRunning(t *testing.T) {
	w := &bldWorld{builds: []cbtypes.Build{bldBuild("b9", cbtypes.StatusTypeInProgress, bldT0)}}
	_, err := Start(context.Background(), w.clients(), bootP, ActionApply, nil)
	var re *RunningError
	if !errors.As(err, &re) || re.Build != "b9" {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "build b9 is running: dormeto install status follows it" {
		t.Fatalf("message = %q", err)
	}
	if len(w.started) != 0 {
		t.Fatal("StartBuild called")
	}
}

func bldVars(in *codebuild.StartBuildInput) []string {
	var out []string
	for _, v := range in.EnvironmentVariablesOverride {
		if v.Type != cbtypes.EnvironmentVariableTypePlaintext {
			out = append(out, "not plaintext: "+aws.ToString(v.Name))
		}
		out = append(out, aws.ToString(v.Name)+"="+aws.ToString(v.Value))
	}
	return out
}

func TestStartOverrides(t *testing.T) {
	for _, tc := range []struct {
		action Action
		env    map[string]string
		want   []string
	}{
		{ActionPlan, nil, []string{"ACTION=plan"}},
		{ActionApply, map[string]string{"PLAN": "p:1", "A": "z"}, []string{"A=z", "ACTION=apply", "PLAN=p:1"}},
		{ActionApplyDestroy, nil, []string{"ACTION=apply-destroy"}},
		{ActionPlanDestroy, nil, []string{"ACTION=plan-destroy"}},
		{ActionOrgApply, nil, []string{"ACTION=apply"}},
	} {
		w := &bldWorld{}
		id, err := Start(context.Background(), w.clients(), bootP, tc.action, tc.env)
		if err != nil || id != "dmt-prod-install:new" {
			t.Fatalf("%s: %q, %v", tc.action, id, err)
		}
		if aws.ToString(w.started[0].ProjectName) != "dmt-prod-install" {
			t.Fatalf("project = %q", aws.ToString(w.started[0].ProjectName))
		}
		if got := bldVars(w.started[0]); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: vars = %v, want %v", tc.action, got, tc.want)
		}
	}
}

func TestStartRetriesWhileIAMCatchesUp(t *testing.T) {
	slept := bootClock(t)
	w := &bldWorld{errs: []error{bootAssumeRoleRefused()}}
	id, err := Start(context.Background(), w.clients(), bootP, ActionPlan, nil)
	if err != nil || id == "" {
		t.Fatalf("%q, %v", id, err)
	}
	if len(w.started) != 2 || len(*slept) != 1 {
		t.Fatalf("started %d, slept %v", len(w.started), *slept)
	}
	w = &bldWorld{errs: []error{&cbtypes.InvalidInputException{Message: aws.String("bad")}}}
	if _, err := Start(context.Background(), w.clients(), bootP, ActionPlan, nil); err == nil || len(w.started) != 1 {
		t.Fatalf("err = %v, started %d", err, len(w.started))
	}
}

func bldFixture(t *testing.T) PlanSummary {
	t.Helper()
	b, err := os.ReadFile("testdata/plan/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Summarize(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSummarize(t *testing.T) {
	s := bldFixture(t)
	want := PlanSummary{
		Add: 3, Change: 1, Destroy: 3,
		ByType:    map[string]int{"aws_lambda_function": 2, "aws_iam_role": 1, "aws_dynamodb_table": 1, "aws_cloudfront_distribution": 1},
		Replaced:  []string{"aws_cloudfront_distribution.dashboard", "aws_dynamodb_table.events"},
		Destroyed: []string{"aws_iam_role.old"},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v\nwant %+v", s, want)
	}
	if s.Empty() {
		t.Fatal("Empty")
	}
}

func TestSummarizeLines(t *testing.T) {
	got := bldFixture(t).Lines()
	want := []string{
		"3 to add, 1 to change, 3 to destroy",
		"  replaced: aws_cloudfront_distribution.dashboard, aws_dynamodb_table.events",
		"  destroyed: aws_iam_role.old",
		"    2 aws_lambda_function",
		"    1 aws_cloudfront_distribution",
		"    1 aws_dynamodb_table",
		"    1 aws_iam_role",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSummarizeCuts(t *testing.T) {
	var rc []string
	for i := 0; i < 15; i++ {
		rc = append(rc, fmt.Sprintf(`{"address":"t%02d.a","type":"t%02d","change":{"actions":["create"]}}`, i, i))
	}
	for i := 0; i < 12; i++ {
		rc = append(rc, fmt.Sprintf(`{"address":"d%02d.a","type":"d","change":{"actions":["delete"]}}`, i))
	}
	s, err := Summarize([]byte(`{"format_version":"1.2","resource_changes":[` + strings.Join(rc, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := s.Lines()
	if lines[0] != "15 to add, 0 to change, 12 to destroy" {
		t.Fatalf("first = %q", lines[0])
	}
	if want := "  destroyed: d00.a, d01.a, d02.a, d03.a, d04.a, d05.a, d06.a, d07.a, d08.a, d09.a, and 2 more"; lines[1] != want {
		t.Fatalf("destroyed = %q", lines[1])
	}
	if lines[2] != "    12 d" || lines[3] != "    1 t00" || lines[13] != "    1 t10" {
		t.Fatalf("types = %q", lines[2:])
	}
	if len(lines) != 15 || lines[14] != "    … and 4 more types" {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestSummarizeEmptyAndGarbage(t *testing.T) {
	s, err := Summarize([]byte(`{"format_version":"1.2"}`))
	if err != nil || !s.Empty() || s.Lines()[0] != "0 to add, 0 to change, 0 to destroy" || len(s.Lines()) != 1 {
		t.Fatalf("%+v, %v", s, err)
	}
	for _, body := range []string{"garbage", `{"a":1}`, ``, `[]`} {
		if _, err := Summarize([]byte(body)); err == nil {
			t.Fatalf("%q: no error", body)
		}
	}
}

func TestCheckPlan(t *testing.T) {
	apply := bldBuild("a1", cbtypes.StatusTypeSucceeded, bldT0)
	newer := bldBuild("p2", cbtypes.StatusTypeSucceeded, bldT0.Add(time.Hour))
	older := bldBuild("p0", cbtypes.StatusTypeSucceeded, bldT0.Add(-time.Hour))
	failed := bldBuild("pf", cbtypes.StatusTypeFailed, bldT0.Add(time.Hour))
	for _, tc := range []struct {
		name    string
		plan    string
		applied *Applied
		want    string
	}{
		{"newer", "p2", &Applied{Build: "a1"}, ""},
		{"no apply yet", "p2", nil, ""},
		{"apply gone", "p2", &Applied{Build: "gone"}, ""},
		{"failed", "pf", &Applied{Build: "a1"}, "the plan pf did not succeed (FAILED): plan again"},
		{"older", "p0", &Applied{Build: "a1"}, "the plan p0 predates the last apply a1: plan again"},
		{"unknown", "nope", &Applied{Build: "a1"}, "no plan nope: plan again"},
	} {
		w := &bldWorld{builds: []cbtypes.Build{apply, newer, older, failed}}
		err := CheckPlan(context.Background(), w.clients(), tc.plan, tc.applied)
		if got := fmt.Sprint(err); (tc.want == "" && err != nil) || (tc.want != "" && got != tc.want) {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
	}
}
