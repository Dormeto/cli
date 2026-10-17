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
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/sync/errgroup"
)

const (
	organizationProcedure = "https://dormeto.com/organization-access.html"
	// managementLeftOut is why the management account gets no roles (decision 19: the member role is not there, and AWS
	// advises no workloads there), with its procedure.
	managementLeftOut = "left out of the roles (its procedure: " + organizationProcedure + ")"
	delegationSid     = "DormetoReadOrganization"
	orgPolicyName     = "dormeto-organization"
	surveyParallel    = 20
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
	PolicyChange  string            // "new", "updated" (principal added), "actions" (only actions added), "unchanged"
}

// Accounts are the accounts the build runs the script in: Ready and Missing, sorted.
func (a Access) Accounts() []string {
	out := append(slices.Clone(a.Ready), a.Missing...)
	slices.Sort(out)
	return out
}

// Survey reads the organization from the management account, changing nothing: which accounts the member role reaches,
// which of them hold Dormeto's roles (and whose), and the delegation policy that would be put. An error is returned
// only for a failure that makes the rest meaningless. assume is called from up to 20 goroutines at once.
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
	var said []string
	if len(outside) > 0 {
		said = append(said, "not in the organization: "+strings.Join(outside, ", "))
	}
	if slices.Contains(s.Accounts, m) { // left out all the same (decision 19)
		said = append(said, "--accounts names the management account "+m+": "+managementLeftOut)
	}
	if len(said) > 0 {
		cs = append(cs, Check{"organization accounts", strings.Join(said, "; "), Warn})
	}

	mine := managerRoles(manager.Account, s.Env)
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

// memberRoleCheck is the member role's row: a warning when an account is unreachable, left out with its procedure; none
// reachable leaves organization access the delegation policy only (spec §5, §9), the manager installed all the same.
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
		d += ": no roles, the delegation policy only"
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
	roles, principals, boundary, err := readDormetoRoles(ctx, c, s, account)
	if err != nil {
		r.kind, r.note = "unreachable", firstLine(err.Error())
		return r
	}
	var others []string
	trusting := false
	for _, p := range principals {
		switch {
		case slices.Contains(mine, p):
			trusting = true
		case !isUniqueID(p): // a unique id is a deleted role of a manager: stale, the script rewrites the trust
			others = append(others, p)
		}
	}
	found := len(roles) > 0
	trusting = trusting || (found && staleOnly(principals))
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
	return a, append(cs, Check{name, policyDetail(a.PolicyChange), OK}), nil
}

// policyDetail says what the merge does to the delegation policy (MergePolicy's change).
func policyDetail(change string) string {
	return map[string]string{"new": "new", "updated": delegationSid + " gets this manager", "actions": delegationSid + " gets the release's actions", "unchanged": "already names this manager"}[change]
}

// policyDoc is a policy document whose statements stay as they were written.
type policyDoc struct {
	Version   json.RawMessage `json:"Version,omitempty"`
	ID        json.RawMessage `json:"Id,omitempty"`
	Statement json.RawMessage `json:"Statement"` // an object or a list: IAM accepts both
}

// statements are a policy's statements, whether it wrote one object or a list.
func statements(raw json.RawMessage) ([]json.RawMessage, error) {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return list, nil
	}
	var one map[string]json.RawMessage
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, errors.New("Statement is neither an object nor a list")
	}
	return []json.RawMessage{raw}, nil
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
// is "new", "updated" (the principal was added), "actions" (only the release's actions were added) or "unchanged"
// (current returned as it is).
func MergePolicy(current, template []byte, managerAccount, env string) (merged []byte, change string, err error) {
	filled := strings.NewReplacer("MANAGER_ACCOUNT", managerAccount, "ENV", env).Replace(string(template))
	var tpl policyDoc
	if err := json.Unmarshal([]byte(filled), &tpl); err != nil {
		return nil, "", fmt.Errorf("the release's delegation policy: %w", err)
	}
	var tplStmt map[string]json.RawMessage
	tplStmts, err := statements(tpl.Statement)
	if err != nil || len(tplStmts) != 1 || json.Unmarshal(tplStmts[0], &tplStmt) != nil {
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
	stmts, err := statements(doc.Statement)
	if err != nil {
		return nil, "", fmt.Errorf("the delegation policy in the organization: %w", err)
	}
	for i, raw := range stmts {
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
		added := !slices.Contains(have, principal)
		union := slices.Clone(haveActions)
		union = append(union, actions...)
		slices.Sort(union)
		union = slices.Compact(union)
		if !added && len(union) == len(haveActions) {
			return current, "unchanged", nil
		}
		have = append(have, principal)
		slices.Sort(have)
		have = slices.Compact(have)
		pr["AWS"], _ = json.Marshal(have)
		st["Principal"] = orderedObject(pr)
		st["Action"], _ = json.Marshal(union)
		stmts[i] = orderedObject(st)
		change = "actions"
		if added {
			change = "updated"
		}
		return marshalPolicy(doc, stmts, change)
	}
	return marshalPolicy(doc, append(stmts, tplStmts[0]), "updated")
}

func marshalPolicy(doc policyDoc, stmts []json.RawMessage, change string) ([]byte, string, error) {
	doc.Statement, _ = json.Marshal(stmts)
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return nil, "", err
	}
	return out.Bytes(), change, nil
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

// orgRolePolicy is what the organization build may do: assume the member role in the organization's accounts only, read
// and put the delegation policy, keep its plan and results in the bucket, and write its log.
func orgRolePolicy(p Project, memberRole, organization string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[`+
		`{"Sid":"AssumeMemberRole","Effect":"Allow","Action":"sts:AssumeRole","Resource":"arn:aws:iam::*:role/%[1]s",`+
		`"Condition":{"StringEquals":{"aws:ResourceOrgID":"%[6]s"}}},`+
		`{"Sid":"DelegationPolicy","Effect":"Allow","Action":["organizations:DescribeResourcePolicy","organizations:PutResourcePolicy","organizations:ListAccounts"],"Resource":"*"},`+
		`{"Sid":"State","Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:ListBucket"],"Resource":["arn:aws:s3:::%[2]s","arn:aws:s3:::%[2]s/*"]},`+
		`{"Sid":"Logs","Effect":"Allow","Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"arn:aws:logs:%[3]s:%[4]s:log-group:%[5]s:*"}]}`,
		strings.TrimPrefix(memberRole, "/"), p.Bucket, p.Region, p.Account, p.LogGroup, organization)
}

// OrgBootstrap creates, or updates, the management account's project that runs organization.sh: the bucket, the role
// (no managed policy, one scoped inline policy: the member role of the organization's accounts), the log group, the
// scripts and the project.
func OrgBootstrap(ctx context.Context, mgmt Clients, p Project, memberRole, organization string) error {
	if err := ensureBucket(ctx, mgmt, p); err != nil {
		return err
	}
	roleARN, err := ensureRole(ctx, mgmt, p, "Dormeto install: what CodeBuild gives the organization's accounts their roles with")
	if err != nil {
		return err
	}
	if _, err := mgmt.IAM.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: &p.Role, PolicyName: aws.String(orgPolicyName),
		PolicyDocument: aws.String(orgRolePolicy(p, memberRole, organization))}); err != nil {
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
	case got.Role != nil && aws.ToString(got.Role.Path) == rolePath && hasCLITag(got.Role.Tags):
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

// PutOrgPlan writes the plan the build applies: policy.json is the policy to put; policy-base.json is the policy it was
// merged from, an empty object (zero bytes) when there was none (ruling R23), which is what the build compares with an
// absent policy; accounts.txt and discovery-only.txt are the accounts that get the roles and those of them without the
// command role, one id per line (an organization's hundreds of ids do not fit the build's environment).
func PutOrgPlan(ctx context.Context, mgmt Clients, p Project, a Access) error {
	for _, f := range []struct {
		name string
		body []byte
	}{{"policy.json", a.Policy}, {"policy-base.json", a.PolicyBase}, {"accounts.txt", idLines(a.Accounts())}, {"discovery-only.txt", idLines(a.DiscoveryOnly)}} {
		if err := putObject(ctx, mgmt, p, p.Key("organization", f.name), f.body); err != nil {
			return err
		}
	}
	return nil
}

// idLines are ids one per line, each ended by a newline (none: empty).
func idLines(ids []string) []byte {
	var b bytes.Buffer
	for _, id := range ids {
		b.WriteString(id + "\n")
	}
	return b.Bytes()
}

// ReadOrgAccounts reads the accounts of the plan the organization build applies (accounts.txt; none when absent).
func ReadOrgAccounts(ctx context.Context, mgmt Clients, p Project) ([]string, error) {
	b, err := getObject(ctx, mgmt, p, p.Key("organization", "accounts.txt"))
	return strings.Fields(string(b)), err
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
