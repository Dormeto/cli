package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
)

// Action is what a build does: the ACTION variable manager.sh and organization.sh read.
type Action string

const (
	ActionPlan         Action = "plan"
	ActionPlanDestroy  Action = "plan-destroy"
	ActionApply        Action = "apply"
	ActionApplyDestroy Action = "apply-destroy"
	ActionOrgApply     Action = "org-apply" // the management account's project: ACTION=apply for organization.sh
)

// RunningError is a build already running in the project: CodeBuild refuses a second one rather than queuing it.
type RunningError struct{ Build string }

func (e *RunningError) Error() string {
	return fmt.Sprintf("build %s is running: dormeto install status follows it", e.Build)
}

// Running is the id of the project's build in progress, or "" when none.
func Running(ctx context.Context, c Clients, project string) (string, error) {
	list, err := c.CodeBuild.ListBuildsForProject(ctx, &codebuild.ListBuildsForProjectInput{ProjectName: &project, SortOrder: cbtypes.SortOrderTypeDescending})
	if err != nil {
		return "", fmt.Errorf("list builds of %s: %w", project, err)
	}
	if len(list.Ids) == 0 {
		return "", nil
	}
	got, err := c.CodeBuild.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: list.Ids[:min(len(list.Ids), 10)]})
	if err != nil {
		return "", fmt.Errorf("get builds of %s: %w", project, err)
	}
	for _, b := range got.Builds {
		if b.BuildStatus == cbtypes.StatusTypeInProgress {
			return aws.ToString(b.Id), nil
		}
	}
	return "", nil
}

// Start starts a build of the project with the action (and env as more variables) and returns its id; a build already
// running is a *RunningError.
func Start(ctx context.Context, c Clients, p Project, a Action, env map[string]string) (string, error) {
	running, err := Running(ctx, c, p.Name)
	if err != nil {
		return "", err
	}
	if running != "" {
		return "", &RunningError{Build: running}
	}
	vars := map[string]string{}
	for k, v := range env {
		vars[k] = v
	}
	vars["ACTION"] = string(a)
	if a == ActionOrgApply {
		vars["ACTION"] = "apply"
	}
	var overrides []cbtypes.EnvironmentVariable
	for _, k := range sortedKeys(vars) {
		overrides = append(overrides, cbtypes.EnvironmentVariable{Name: aws.String(k), Value: aws.String(vars[k]), Type: cbtypes.EnvironmentVariableTypePlaintext})
	}
	var id string
	err = untilUsable(func() error {
		out, err := c.CodeBuild.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: &p.Name, EnvironmentVariablesOverride: overrides})
		if err == nil && out.Build != nil {
			id = aws.ToString(out.Build.Id)
		}
		return err
	}, "start build of "+p.Name)
	return id, err
}

// PlanSummary is what a Terraform plan does, counted.
type PlanSummary struct {
	Add, Change, Destroy int
	ByType               map[string]int // type → resources changed (created, updated, replaced or destroyed)
	Replaced, Destroyed  []string       // addresses, sorted
}

// Summarize reads `terraform show -json` of a plan. A replacement counts as one to add and one to destroy.
func Summarize(planJSON []byte) (PlanSummary, error) {
	var plan struct {
		FormatVersion   *string `json:"format_version"`
		ResourceChanges *[]struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return PlanSummary{}, fmt.Errorf("not a Terraform plan: %w", err)
	}
	if plan.FormatVersion == nil && plan.ResourceChanges == nil {
		return PlanSummary{}, errors.New("not a Terraform plan: no format_version or resource_changes")
	}
	s := PlanSummary{ByType: map[string]int{}}
	if plan.ResourceChanges == nil {
		return s, nil
	}
	for _, rc := range *plan.ResourceChanges {
		switch strings.Join(rc.Change.Actions, ",") {
		case "create":
			s.Add++
		case "update":
			s.Change++
		case "delete":
			s.Destroy++
			s.Destroyed = append(s.Destroyed, rc.Address)
		case "delete,create", "create,delete":
			s.Add++
			s.Destroy++
			s.Replaced = append(s.Replaced, rc.Address)
		default: // no-op, read
			continue
		}
		s.ByType[rc.Type]++
	}
	slices.Sort(s.Replaced)
	slices.Sort(s.Destroyed)
	return s, nil
}

// Empty is a plan with nothing to add, change or destroy.
func (s PlanSummary) Empty() bool { return s.Add == 0 && s.Change == 0 && s.Destroy == 0 }

const (
	maxAddresses = 10
	maxTypes     = 12
)

// Lines is the summary to show: the counts, the replaced and destroyed addresses, then the resources by type.
func (s PlanSummary) Lines() []string {
	lines := []string{fmt.Sprintf("%d to add, %d to change, %d to destroy", s.Add, s.Change, s.Destroy)}
	for _, l := range []struct {
		label     string
		addresses []string
	}{{"replaced", s.Replaced}, {"destroyed", s.Destroyed}} {
		if len(l.addresses) == 0 {
			continue
		}
		shown := l.addresses[:min(len(l.addresses), maxAddresses)]
		line := "  " + l.label + ": " + strings.Join(shown, ", ")
		if more := len(l.addresses) - len(shown); more > 0 {
			line += fmt.Sprintf(", and %d more", more)
		}
		lines = append(lines, line)
	}
	types := make([]string, 0, len(s.ByType))
	for t := range s.ByType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		if s.ByType[types[i]] != s.ByType[types[j]] {
			return s.ByType[types[i]] > s.ByType[types[j]]
		}
		return types[i] < types[j]
	})
	for _, t := range types[:min(len(types), maxTypes)] {
		lines = append(lines, fmt.Sprintf("    %d %s", s.ByType[t], t))
	}
	if more := len(types) - maxTypes; more > 0 {
		lines = append(lines, fmt.Sprintf("    … and %d more types", more))
	}
	return lines
}

// StalePlanError is a plan made before the last apply: applied already, or replaced; there is no plan to apply.
type StalePlanError struct{ Plan, Apply string }

func (e *StalePlanError) Error() string {
	return fmt.Sprintf("the plan %s predates the last apply %s: plan again", e.Plan, e.Apply)
}

// UnusablePlanError is a plan that cannot be applied: unknown (Status empty), or it did not succeed.
type UnusablePlanError struct{ Plan, Status string }

func (e *UnusablePlanError) Error() string {
	if e.Status == "" {
		return fmt.Sprintf("no plan %s: plan again", e.Plan)
	}
	return fmt.Sprintf("the plan %s did not succeed (%s): plan again", e.Plan, e.Status)
}

// CheckPlan refuses a plan that is unknown or did not succeed (an *UnusablePlanError), or was made before the last
// apply (applied, when not nil: a *StalePlanError); its other errors are AWS's.
func CheckPlan(ctx context.Context, c Clients, planBuild string, applied *Applied) error {
	ids := []string{planBuild}
	if applied != nil && applied.Build != "" && applied.Build != planBuild {
		ids = append(ids, applied.Build)
	}
	got, err := c.CodeBuild.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: ids})
	if err != nil {
		return fmt.Errorf("get build %s: %w", planBuild, err)
	}
	var plan, apply *cbtypes.Build
	for i, b := range got.Builds {
		switch aws.ToString(b.Id) {
		case planBuild:
			plan = &got.Builds[i]
		case ids[len(ids)-1]:
			apply = &got.Builds[i]
		}
	}
	if plan == nil {
		return &UnusablePlanError{Plan: planBuild}
	}
	if plan.BuildStatus != cbtypes.StatusTypeSucceeded {
		return &UnusablePlanError{Plan: planBuild, Status: string(plan.BuildStatus)}
	}
	if apply != nil && apply.StartTime != nil && plan.StartTime != nil && plan.StartTime.Before(*apply.StartTime) {
		return &StalePlanError{Plan: planBuild, Apply: aws.ToString(apply.Id)}
	}
	return nil
}
