package install

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/sync/errgroup"
)

const (
	organizationProcedure = "https://dormeto.com/organization-access.html"
	delegationSid         = "DormetoReadOrganization"
	orgPolicyName         = "dormeto-organization"
	surveyParallel        = 20
)

// Access is what the survey of the organization found, and the plan the build applies.
type Access struct {
	Management    string            // the management account (the management profile's)
	Ready         []string          // reachable, roles present and trusting this manager (the script updates them)
	Missing       []string          // reachable, no Dormeto role (the script creates them)
	DiscoveryOnly []string          // of Ready+Missing, the --discovery-only ones (no command role)
	Unreachable   []string          // the member role could not be assumed
	Foreign       []string          // another manager's roles or boundary (decision 15): left out
	Notes         map[string]string // account → why it is unreachable or foreign
	Policy        []byte            // the merged delegation policy to put
	PolicyBase    []byte            // the policy as read (empty: none), which the build checks is unchanged
	PolicyChange  string            // "new", "updated", "unchanged"
}

// Accounts are the accounts the build runs the script in: Ready and Missing, sorted.
func (a Access) Accounts() []string {
	out := append(slices.Clone(a.Ready), a.Missing...)
	slices.Sort(out)
	return out
}

// Survey reads the organization from the management account, changing nothing: which accounts the member role reaches,
// which of them hold Dormeto's roles (and whose), and the delegation policy that would be put. An error is returned
// only for a failure that makes the rest meaningless.
func Survey(ctx context.Context, mgmt Clients, assume func(account string) (Clients, error), s Settings, manager Where, template []byte) (Access, []Check, error) {
	const name = "management account"
	m := ""
	if manager.Organization != nil {
		m = manager.Organization.Management
	}
	id, err := mgmt.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Access{}, nil, err
	}
	if got := aws.ToString(id.Account); m == "" || got != m {
		if m == "" {
			return Access{}, []Check{{name, "the manager's account is not in an organization", Fail}}, nil
		}
		return Access{}, []Check{{name, fmt.Sprintf("the profile reaches %s, not the management account %s", got, m), Fail}}, nil
	}
	cs := []Check{{name, m, OK}}
	a := Access{Management: m, Notes: map[string]string{}}

	ids, outside, err := surveyAccounts(ctx, mgmt, s, manager.Account, m)
	if err != nil {
		return Access{}, cs, fmt.Errorf("list the organization's accounts: %w", err)
	}
	if len(outside) > 0 {
		cs = append(cs, Check{"organization accounts", "not in the organization: " + strings.Join(outside, ", "), Warn})
	}

	mine := []string{
		"arn:aws:iam::" + manager.Account + ":role/dmt-" + s.Env + "-manager-discovery",
		"arn:aws:iam::" + manager.Account + ":role/dmt-" + s.Env + "-manager-operations",
	}
	results := make([]accountResult, len(ids))
	var g errgroup.Group
	g.SetLimit(surveyParallel)
	for i, acct := range ids {
		g.Go(func() error {
			results[i] = surveyAccount(ctx, assume, s, acct, mine)
			return nil
		})
	}
	_ = g.Wait()
	for _, r := range results { // ids are sorted, so the results are
		switch r.kind {
		case "ready":
			a.Ready = append(a.Ready, r.account)
		case "missing":
			a.Missing = append(a.Missing, r.account)
		case "unreachable":
			a.Unreachable = append(a.Unreachable, r.account)
		case "foreign":
			a.Foreign = append(a.Foreign, r.account)
		}
		if r.note != "" {
			a.Notes[r.account] = r.note
		}
	}
	for _, acct := range a.Accounts() {
		if slices.Contains(s.DiscoveryOnly, acct) {
			a.DiscoveryOnly = append(a.DiscoveryOnly, acct)
		}
	}

	cs = append(cs, memberRoleCheck(s, len(ids), a))
	if len(a.Unreachable) == len(ids) && len(ids) > 0 {
		return a, cs, nil
	}
	cs = append(cs, Check{"member accounts", memberAccountsDetail(a), memberAccountsVerdict(a)})
	return surveyPolicy(ctx, mgmt, a, cs, template, manager.Account, s.Env)
}

func memberAccountsVerdict(a Access) Verdict {
	if len(a.Foreign) > 0 {
		return Warn
	}
	return OK
}

func memberAccountsDetail(a Access) string {
	d := fmt.Sprintf("%d ready, %d to create, %d discovery only", len(a.Ready), len(a.Missing), len(a.DiscoveryOnly))
	if len(a.Foreign) > 0 {
		d += "; another manager's roles, left out: " + strings.Join(a.Foreign, ", ")
	}
	return d
}

func memberRoleCheck(s Settings, total int, a Access) Check {
	const name = "member role"
	if total == 0 {
		return Check{name, "no other account in the organization to give access to", Skip}
	}
	d := fmt.Sprintf("%s: assumed in %d of %d accounts", s.MemberRole, total-len(a.Unreachable), total)
	switch {
	case len(a.Unreachable) == 0:
		return Check{name, d, OK}
	case len(a.Unreachable) == total:
		return Check{name, d + unreachableDetail(a), Fail}
	}
	return Check{name, d + unreachableDetail(a), Warn}
}

func unreachableDetail(a Access) string {
	return fmt.Sprintf("; unreachable: %s (the procedure for them: %s)", strings.Join(a.Unreachable, ", "), organizationProcedure)
}

// surveyAccounts are the active accounts to survey, sorted, and the ids of --accounts that are not in the organization.
func surveyAccounts(ctx context.Context, mgmt Clients, s Settings, manager, management string) (ids, outside []string, err error) {
	in := &organizations.ListAccountsInput{}
	known := map[string]bool{}
	for {
		out, err := mgmt.Organizations.ListAccounts(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		for _, a := range out.Accounts {
			id := aws.ToString(a.Id)
			known[id] = true
			if string(a.Status) == "ACTIVE" && id != manager && id != management {
				ids = append(ids, id)
			}
		}
		if out.NextToken == nil {
			break
		}
		in = &organizations.ListAccountsInput{NextToken: out.NextToken}
	}
	if len(s.Accounts) > 0 {
		ids = slices.DeleteFunc(ids, func(id string) bool { return !slices.Contains(s.Accounts, id) })
		for _, id := range s.Accounts {
			if !known[id] && !slices.Contains(outside, id) {
				outside = append(outside, id)
			}
		}
		slices.Sort(outside)
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { return slices.Contains(s.ExcludeAccounts, id) })
	slices.Sort(ids)
	return slices.Compact(ids), outside, nil
}

type accountResult struct {
	account, kind, note string
}

// surveyAccount reads one account's Dormeto roles and boundary through the member role.
func surveyAccount(ctx context.Context, assume func(string) (Clients, error), s Settings, account string, mine []string) accountResult {
	r := accountResult{account: account}
	c, err := assume(account)
	if err != nil {
		r.kind, r.note = "unreachable", firstLine(err.Error())
		return r
	}
	var others []string
	trusting, found := false, false
	for _, role := range []string{roleName(s, "discovery_role_name", "DmtDiscoveryRole"), roleName(s, "command_role_name", "DmtCommandRole")} {
		out, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(role)})
		if isNotFound(err, "NoSuchEntity") {
			continue
		}
		if err != nil {
			r.kind, r.note = "unreachable", "read "+role+": "+firstLine(err.Error())
			return r
		}
		found = true
		for _, p := range trustedPrincipals(aws.ToString(out.Role.AssumeRolePolicyDocument)) {
			switch {
			case slices.Contains(mine, p):
				trusting = true
			case !slices.Contains(others, p):
				others = append(others, p)
			}
		}
	}
	_, err = c.IAM.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String("arn:aws:iam::" + account + ":policy/DmtRouterBoundary")})
	boundary := err == nil
	if err != nil && !isNotFound(err, "NoSuchEntity") {
		r.kind, r.note = "unreachable", "read DmtRouterBoundary: "+firstLine(err.Error())
		return r
	}
	switch {
	case len(others) > 0:
		r.kind, r.note = "foreign", "roles trust "+strings.Join(others, ", ")
	case trusting:
		r.kind = "ready"
	case found || boundary:
		r.kind, r.note = "foreign", "DmtRouterBoundary or Dormeto roles exist, none trusting this manager"
	default:
		r.kind = "missing"
	}
	return r
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	line = strings.TrimSpace(line)
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}

// surveyPolicy reads the delegation policy, merges this manager into it and adds the policy's check.
func surveyPolicy(ctx context.Context, mgmt Clients, a Access, cs []Check, template []byte, managerAccount, env string) (Access, []Check, error) {
	const name = "delegation policy"
	out, err := mgmt.Organizations.DescribeResourcePolicy(ctx, &organizations.DescribeResourcePolicyInput{})
	switch {
	case isNotFound(err, "ResourcePolicyNotFoundException"):
	case err != nil:
		return a, append(cs, failure(name, err)), nil
	case out.ResourcePolicy != nil:
		a.PolicyBase = []byte(aws.ToString(out.ResourcePolicy.Content))
	}
	a.Policy, a.PolicyChange, err = MergePolicy(a.PolicyBase, template, managerAccount, env)
	if err != nil {
		return a, append(cs, failure(name, err)), nil
	}
	detail := map[string]string{"new": "new", "updated": delegationSid + " gets this manager", "unchanged": "already names this manager"}[a.PolicyChange]
	return a, append(cs, Check{name, detail, OK}), nil
}

// policyDoc is a policy document whose statements stay as they were written.
type policyDoc struct {
	Version   json.RawMessage   `json:"Version,omitempty"`
	ID        json.RawMessage   `json:"Id,omitempty"`
	Statement []json.RawMessage `json:"Statement"`
}

// statementOrder is the order a statement's keys are written in.
var statementOrder = []string{"Sid", "Effect", "Principal", "NotPrincipal", "Action", "NotAction", "Resource", "NotResource", "Condition"}

// orderedObject writes an object with the usual policy keys first, then the others sorted.
func orderedObject(m map[string]json.RawMessage) json.RawMessage {
	var keys []string
	for _, k := range statementOrder {
		if _, ok := m[k]; ok {
			keys = append(keys, k)
		}
	}
	var rest []string
	for k := range m {
		if !slices.Contains(statementOrder, k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range append(keys, rest...) {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(m[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// stringList reads a JSON string or list of strings.
func stringList(raw json.RawMessage) []string {
	var list []string
	var one string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	return nil
}

// MergePolicy puts this manager into the organization's delegation policy: the template (the release's
// dormeto-organization-policy.json, MANAGER_ACCOUNT and ENV replaced) when there is none; else the statement of
// the template's Sid gets the manager's principal and the template's actions, the other statements kept in place. change
// is "new", "updated", or "unchanged" (current returned as it is).
func MergePolicy(current, template []byte, managerAccount, env string) (merged []byte, change string, err error) {
	filled := strings.NewReplacer("MANAGER_ACCOUNT", managerAccount, "ENV", env).Replace(string(template))
	var tpl policyDoc
	if err := json.Unmarshal([]byte(filled), &tpl); err != nil {
		return nil, "", fmt.Errorf("the release's delegation policy: %w", err)
	}
	var tplStmt map[string]json.RawMessage
	if len(tpl.Statement) != 1 || json.Unmarshal(tpl.Statement[0], &tplStmt) != nil {
		return nil, "", errors.New("the release's delegation policy must have one statement")
	}
	var tplPrincipal struct{ AWS json.RawMessage }
	_ = json.Unmarshal(tplStmt["Principal"], &tplPrincipal)
	principals := stringList(tplPrincipal.AWS)
	if len(principals) != 1 {
		return nil, "", errors.New("the release's delegation policy must name one principal")
	}
	principal := principals[0]
	actions := stringList(tplStmt["Action"])
	slices.Sort(actions)

	if len(bytes.TrimSpace(current)) == 0 {
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(filled)); err != nil {
			return nil, "", err
		}
		return b.Bytes(), "new", nil
	}

	var doc policyDoc
	if err := json.Unmarshal(current, &doc); err != nil {
		return nil, "", fmt.Errorf("the delegation policy in the organization: %w", err)
	}
	for i, raw := range doc.Statement {
		var st map[string]json.RawMessage
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		var sid string
		if json.Unmarshal(st["Sid"], &sid) != nil || sid != delegationSid {
			continue
		}
		var pr map[string]json.RawMessage
		if json.Unmarshal(st["Principal"], &pr) != nil || pr == nil {
			pr = map[string]json.RawMessage{}
		}
		have := stringList(pr["AWS"])
		haveActions := stringList(st["Action"])
		slices.Sort(haveActions)
		if slices.Contains(have, principal) && slices.Equal(haveActions, actions) {
			return current, "unchanged", nil
		}
		have = append(have, principal)
		slices.Sort(have)
		have = slices.Compact(have)
		pr["AWS"], _ = json.Marshal(have)
		st["Principal"] = orderedObject(pr)
		st["Action"] = tplStmt["Action"]
		doc.Statement[i] = orderedObject(st)
		return marshalPolicy(doc)
	}
	doc.Statement = append(doc.Statement, tpl.Statement[0])
	return marshalPolicy(doc)
}

func marshalPolicy(doc policyDoc) ([]byte, string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return nil, "", err
	}
	return out.Bytes(), "updated", nil
}

// OrgNames is the management account's own project: its bucket, CodeBuild project, role and log group.
func OrgNames(managementAccount, region, env string) Project {
	name := "dmt-" + env + "-organization"
	return Project{
		Account: managementAccount, Region: region, Env: env,
		Bucket:   "dmt-install-" + managementAccount + "-" + region,
		Name:     name,
		Role:     name,
		LogGroup: "/aws/codebuild/" + name,
	}
}

// orgRolePolicy is what the organization build may do: assume the member role, read and put the delegation policy,
// keep its plan and results in the bucket, and write its log.
func orgRolePolicy(p Project, memberRole string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[`+
		`{"Sid":"AssumeMemberRole","Effect":"Allow","Action":"sts:AssumeRole","Resource":"arn:aws:iam::*:role/%[1]s"},`+
		`{"Sid":"DelegationPolicy","Effect":"Allow","Action":["organizations:DescribeResourcePolicy","organizations:PutResourcePolicy","organizations:ListAccounts"],"Resource":"*"},`+
		`{"Sid":"State","Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:ListBucket"],"Resource":["arn:aws:s3:::%[2]s","arn:aws:s3:::%[2]s/*"]},`+
		`{"Sid":"Logs","Effect":"Allow","Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"arn:aws:logs:%[3]s:%[4]s:log-group:%[5]s:*"}]}`,
		strings.TrimPrefix(memberRole, "/"), p.Bucket, p.Region, p.Account, p.LogGroup)
}

// OrgBootstrap creates, or updates, the management account's project that runs organization.sh: the bucket, the role
// (no managed policy, one scoped inline policy), the log group, the scripts and the project.
func OrgBootstrap(ctx context.Context, mgmt Clients, p Project, memberRole string) error {
	if err := ensureBucket(ctx, mgmt, p); err != nil {
		return err
	}
	roleARN, err := ensureRole(ctx, mgmt, p, "Dormeto install: what CodeBuild gives the organization's accounts their roles with")
	if err != nil {
		return err
	}
	if _, err := mgmt.IAM.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: &p.Role, PolicyName: aws.String(orgPolicyName),
		PolicyDocument: aws.String(orgRolePolicy(p, memberRole))}); err != nil {
		return fmt.Errorf("put the policy of role %s: %w", p.Role, err)
	}
	if err := ensureLogGroup(ctx, mgmt, p); err != nil {
		return err
	}
	if err := uploadScripts(ctx, mgmt, p); err != nil {
		return err
	}
	return ensureProject(ctx, mgmt, p, roleARN, "organization.sh")
}

// OrgTeardown deletes the organization project, its role and its log group; the bucket stays (its reports stay).
// What is already gone is not an error.
func OrgTeardown(ctx context.Context, mgmt Clients, p Project) error {
	if _, err := mgmt.CodeBuild.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: &p.Name}); err != nil && !isNotFound(err, "ResourceNotFoundException") {
		return fmt.Errorf("delete project %s: %w", p.Name, err)
	}
	got, err := mgmt.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: &p.Role})
	switch {
	case isNotFound(err, "NoSuchEntity"):
	case err != nil:
		return fmt.Errorf("check role %s: %w", p.Role, err)
	case got.Role != nil && aws.ToString(got.Role.Path) == rolePath && slices.ContainsFunc(got.Role.Tags, func(t iamtypes.Tag) bool {
		return aws.ToString(t.Key) == "managed-by" && aws.ToString(t.Value) == "dormeto-cli"
	}):
		if _, err := mgmt.IAM.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: &p.Role, PolicyName: aws.String(orgPolicyName)}); err != nil && !isNotFound(err, "NoSuchEntity") {
			return fmt.Errorf("delete the policy of role %s: %w", p.Role, err)
		}
		if _, err := mgmt.IAM.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: &p.Role}); err != nil && !isNotFound(err, "NoSuchEntity") {
			return fmt.Errorf("delete role %s: %w", p.Role, err)
		}
	}
	if _, err := mgmt.Logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: &p.LogGroup}); err != nil && !isNotFound(err, "ResourceNotFoundException") {
		return fmt.Errorf("delete log group %s: %w", p.LogGroup, err)
	}
	return nil
}

// PutOrgPlan writes the plan the build applies: the policy to put and the policy it was merged from // object when there was none, which is what the build compares with an absent policy).
func PutOrgPlan(ctx context.Context, mgmt Clients, p Project, a Access) error {
	if err := putObject(ctx, mgmt, p, p.Key("organization", "policy.json"), a.Policy); err != nil {
		return err
	}
	return putObject(ctx, mgmt, p, p.Key("organization", "policy-base.json"), a.PolicyBase)
}

// RoleResult is one account's line of roles.json.
type RoleResult struct {
	Account string `json:"account"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// ReadRoles reads <env>/organization/roles.json (one JSON object per line); nil when it is absent.
func ReadRoles(ctx context.Context, mgmt Clients, p Project) ([]RoleResult, error) {
	b, err := getObject(ctx, mgmt, p, p.Key("organization", "roles.json"))
	if err != nil || b == nil {
		return nil, err
	}
	var out []RoleResult
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r RoleResult
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("roles.json: %w", err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Disabled explains organization access without the management account's profile.
func Disabled(managementAccount string) string {
	return "Organization access needs the management account " + managementAccount + "; without its profile (--management-profile) it is not deployed.\n" +
		"Without it, the manager is installed in organization mode but cannot list the accounts until the delegation policy exists, nor read an account without its roles.\n" +
		"The procedure: " + organizationProcedure + "\n" +
		"Check it later: dormeto install check --management-profile <profile>; the dashboard's Organization access panel too."
}
