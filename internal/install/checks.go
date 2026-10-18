package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/smithy-go"
)

// Verdict is how a check came out.
type Verdict int

const (
	OK   Verdict = iota
	Warn         // asked about once, by the command
	Fail         // stops the install
	Skip         // not applicable
)

// Check is one prerequisite and what was found.
type Check struct {
	Name    string
	Detail  string
	Verdict Verdict
}

// Line is the check as one terminal line.
func (c Check) Line() string {
	mark := map[Verdict]string{OK: "✓", Warn: "!", Fail: "✗", Skip: "-"}[c.Verdict]
	return fmt.Sprintf("  %s %s: %s", mark, c.Name, c.Detail)
}

// Failed tells whether a check failed.
func Failed(cs []Check) bool {
	return slices.ContainsFunc(cs, func(c Check) bool { return c.Verdict == Fail })
}

// Warnings are the checks that warn.
func Warnings(cs []Check) []Check {
	var out []Check
	for _, c := range cs {
		if c.Verdict == Warn {
			out = append(out, c)
		}
	}
	return out
}

// Found is what the command gathers before the checks: the bucket's install.json of this env (nil: none), the other
// environments' install.json found in the bucket (their env names), and the release resolution's result.
type Found struct {
	Saved      *Settings
	OtherEnvs  []string
	Release    Release
	ReleaseErr error
	Adoption   *Adoption // --adopt-state's; nil: not adopting
}

const (
	buildImage   = "aws/codebuild/amazonlinux-x86_64-standard:6.0"
	rightsDenied = "not checked (iam:SimulatePrincipalPolicy refused)"
)

// rightsActions are what the install's own calls need: the actions simulated for the caller.
var rightsActions = []string{
	"s3:CreateBucket", "s3:PutBucketPolicy", "s3:PutObject", "s3:GetObject",
	"iam:CreateRole", "iam:AttachRolePolicy", "iam:PassRole", "iam:TagRole",
	"codebuild:CreateProject", "codebuild:StartBuild", "codebuild:BatchGetBuilds",
	"logs:CreateLogGroup", "logs:GetLogEvents",
	"iam:PutRolePolicy", "s3:PutBucketNotification", "events:PutRule", "events:PutTargets",
}

// Checks runs every prerequisite of the manager's own account, read-only, and returns the rows in a fixed order. An
// unexpected error in a row fails that row with AWS's message.
func Checks(ctx context.Context, c Clients, s Settings, w Where, f Found) []Check {
	cs := []Check{
		checkCredentials(w),
		checkRights(ctx, c, w),
		checkRegion(ctx, c, s),
		checkOrganization(s, w),
		checkExisting(s, f),
	}
	if f.Adoption != nil {
		cs = append(cs, checkAdoption(f.Adoption))
	}
	return append(cs,
		checkAnotherInstall(ctx, c, s, f),
		checkAnotherEnv(f),
		checkRoles(ctx, c, s, w, f),
		checkRelease(f),
		Check{"administrators", strings.Join(s.AdminEmails, ", "), OK},
		checkDomain(ctx, c, s),
		checkSES(ctx, c, s),
	)
}

// adopting tells an adoption whose state passed its checks: the manager found is the one adopted.
func (f Found) adopting() bool { return f.Adoption != nil && f.Adoption.Err == nil }

// apiCode is the AWS error code of err, or empty.
func apiCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func isDenied(err error) bool {
	code := apiCode(err)
	return strings.HasPrefix(code, "AccessDenied") || code == "UnauthorizedOperation" || code == "UnauthorizedAccess"
}

func isNotFound(err error, codes ...string) bool {
	return err != nil && slices.Contains(codes, apiCode(err))
}

func failure(name string, err error) Check { return Check{name, err.Error(), Fail} }

// principal splits the caller's ARN into its kind ("assumed-role", "user", "root", …) and its name (a role's, without
// its session; a user's, without its path).
func principal(arn string) (kind, name string) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return "", arn
	}
	res := strings.Split(parts[5], "/")
	if len(res) == 1 {
		return res[0], res[0]
	}
	if res[0] == "assumed-role" && len(res) >= 2 {
		return res[0], res[1]
	}
	return res[0], res[len(res)-1]
}

func checkCredentials(w Where) Check {
	_, name := principal(w.Caller)
	return Check{"credentials", name + " in " + w.Account, OK}
}

func checkRights(ctx context.Context, c Clients, w Where) Check {
	const name = "administrator rights"
	kind, who := principal(w.Caller)
	source := w.Caller
	switch kind {
	case "root":
		return Check{name, "root", OK}
	case "assumed-role":
		out, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(who)})
		if err != nil {
			if isDenied(err) {
				return Check{name, rightsDenied, Warn}
			}
			return failure(name, err)
		}
		source = aws.ToString(out.Role.Arn)
	}
	denied := map[string]bool{}
	var marker *string
	for {
		out, err := c.IAM.SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
			PolicySourceArn: aws.String(source), ActionNames: rightsActions, ResourceArns: []string{"*"}, Marker: marker,
		})
		if err != nil {
			if isDenied(err) {
				return Check{name, rightsDenied, Warn}
			}
			return failure(name, err)
		}
		for _, r := range out.EvaluationResults {
			if r.EvalDecision != iamtypes.PolicyEvaluationDecisionTypeAllowed {
				denied[aws.ToString(r.EvalActionName)] = true
			}
		}
		if !out.IsTruncated || out.Marker == nil {
			break
		}
		marker = out.Marker
	}
	if len(denied) == 0 {
		return Check{name, "administrator rights (simulated)", OK}
	}
	var list []string
	for _, a := range rightsActions {
		if denied[a] {
			list = append(list, a)
		}
	}
	return Check{name, "denied: " + strings.Join(list, ", "), Fail}
}

func checkRegion(ctx context.Context, c Clients, s Settings) Check {
	const name = "region"
	off := Check{name, s.Region + " is not enabled in this account", Fail}
	regions, err := c.EC2.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		AllRegions: aws.Bool(true),
		Filters:    []ec2types.Filter{{Name: aws.String("region-name"), Values: []string{s.Region}}},
	})
	if err != nil {
		return failure(name, err)
	}
	if !slices.ContainsFunc(regions.Regions, func(r ec2types.Region) bool {
		return aws.ToString(r.RegionName) == s.Region && aws.ToString(r.OptInStatus) != "not-opted-in"
	}) {
		return off
	}
	noImage := Check{name, fmt.Sprintf("CodeBuild has no image %s in %s", buildImage, s.Region), Fail}
	imgs, err := c.CodeBuild.ListCuratedEnvironmentImages(ctx, &codebuild.ListCuratedEnvironmentImagesInput{})
	if err != nil {
		return noImage
	}
	for _, p := range imgs.Platforms {
		for _, l := range p.Languages {
			for _, i := range l.Images {
				if aws.ToString(i.Name) == buildImage {
					return Check{name, s.Region + " enabled; CodeBuild available", OK}
				}
			}
		}
	}
	return noImage
}

func checkOrganization(s Settings, w Where) Check {
	const name = "organization"
	o := w.Organization
	if s.Mode == "account" {
		if o != nil {
			return Check{name, "account deployment (in " + o.ID + ")", OK}
		}
		return Check{name, "account deployment", OK}
	}
	switch {
	case o == nil && w.OrganizationUnknown:
		return Check{name, "the organization could not be read (organizations:DescribeOrganization refused); use --mode account", Fail}
	case o == nil:
		return Check{name, "this account is not in an organization; use --mode account", Fail}
	case !o.AllFeatures:
		return Check{name, fmt.Sprintf("the organization %s has consolidated billing only, organization mode needs all features; use --mode account", o.ID), Fail}
	}
	return Check{name, o.ID + ": all features", OK}
}

func checkExisting(s Settings, f Found) Check {
	const name = "existing install"
	if f.Saved == nil {
		return Check{name, "none: a first install of env " + s.Env, OK}
	}
	version := s.Version
	if version == "" {
		version = f.Release.Version
	}
	switch installed := f.Saved.Installed; {
	case installed == "":
		return Check{name, "recorded for env " + s.Env + ", nothing applied yet: " + version, OK}
	case CompareVersions(installed, version) > 0:
		return Check{name, fmt.Sprintf("%s is installed, newer than %s", installed, version), Fail}
	case installed == version:
		return Check{name, "reinstall of " + version, OK}
	default:
		return Check{name, fmt.Sprintf("upgrade: %s → %s", installed, version), OK}
	}
}

// checkAdoption is the adopted state's row: what moves to the install's module and what stays in the source, or why it
// cannot be adopted.
func checkAdoption(a *Adoption) Check {
	const name = "adopted state"
	if a.Err != nil {
		return Check{name, a.File + ": " + a.Err.Error(), Fail}
	}
	d := fmt.Sprintf("module.%s of %s (Terraform %s): %d resources to module.%s", a.Module, a.File, a.Terraform, a.Resources, installModule)
	if len(a.Left) > 0 {
		d += "; left in the source: " + strings.Join(a.Left, ", ")
	}
	return Check{name, d, OK}
}

func checkAnotherInstall(ctx context.Context, c Clients, s Settings, f Found) Check {
	const name = "another install"
	if f.Saved != nil { // the manager found is this install's
		return Check{name, "none", OK}
	}
	prefix := "dmt-" + s.Env + "-manager"
	if f.adopting() {
		return Check{name, prefix + ": the manager adopted", OK}
	}
	found := false
	if _, err := c.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(prefix + "-api")}); err == nil {
		found = true
	} else if !isNotFound(err, "ResourceNotFoundException") {
		return failure(name, err)
	}
	for token := (*string)(nil); !found; {
		out, err := c.Cognito.ListUserPools(ctx, &cognitoidentityprovider.ListUserPoolsInput{MaxResults: aws.Int32(60), NextToken: token})
		if err != nil {
			return failure(name, err)
		}
		for _, p := range out.UserPools {
			found = found || aws.ToString(p.Name) == prefix
		}
		if token = out.NextToken; token == nil {
			break
		}
	}
	if found {
		return Check{name, prefix + " resources exist without dormeto install's record: installed by other means (keep it in its Terraform)", Fail}
	}
	return Check{name, "none", OK}
}

func checkAnotherEnv(f Found) Check {
	if len(f.OtherEnvs) > 0 {
		return Check{"another environment", "one environment per account: " + strings.Join(f.OtherEnvs, ", ") + " installed here", Fail}
	}
	return Check{"another environment", "none", OK}
}

// roleVar is the role a variable names (a JSON string, a path may precede the name, kept), or def.
func roleVar(s Settings, key, def string) string {
	var v string
	if json.Unmarshal(s.Vars[key], &v) != nil || v == "" {
		return def
	}
	return v
}

// roleName is the name a variable overrides (its path left out), or def.
func roleName(s Settings, key, def string) string {
	v := roleVar(s, key, def)
	return v[strings.LastIndex(v, "/")+1:]
}

func checkRoles(ctx context.Context, c Clients, s Settings, w Where, f Found) Check {
	const name = "Dormeto roles"
	roles, trusted, boundary, err := readDormetoRoles(ctx, c, s, w.Account)
	if err != nil {
		return failure(name, err)
	}
	found := roles
	if boundary {
		found = append(found, dormetoBoundary)
	}
	switch {
	case len(found) == 0:
		return Check{name, "none", OK}
	case f.Saved != nil:
		return Check{name, "this install's", OK}
	case len(roles) > 0 && staleOnly(trusted):
		return Check{name, strings.Join(found, ", ") + " exist, trusting a deleted role: left by an earlier manager; delete them before installing", Fail}
	}
	d := strings.Join(found, ", ") + " exist"
	if len(trusted) > 0 {
		d += ", trusting " + strings.Join(trusted, ", ")
	}
	mine := managerRoles(w.Account, s.Env)
	if len(trusted) > 0 && !slices.ContainsFunc(trusted, func(p string) bool { return !slices.Contains(mine, p) }) {
		// Adopted: each one found is a role or a policy the adopted state moves.
		if f.adopting() && !slices.ContainsFunc(found, func(n string) bool { return !slices.Contains(f.Adoption.IAMNames, n) }) {
			return Check{name, strings.Join(found, ", ") + ": the adopted manager's", OK}
		}
		return Check{name, d + ": env " + s.Env + "'s manager without dormeto install's record (installed by other means, or left by an earlier one); " +
			"delete them before installing (decision 15)", Fail}
	}
	return Check{name, d + ": another manager's (decision 15)", Fail}
}

// managerRoles are the ARNs of the manager's roles that Dormeto's roles trust (discovery, operations), of env in account.
func managerRoles(account, env string) []string {
	return []string{
		"arn:aws:iam::" + account + ":role/dmt-" + env + "-manager-discovery",
		"arn:aws:iam::" + account + ":role/dmt-" + env + "-manager-operations",
	}
}

const dormetoBoundary = "DmtRouterBoundary"

// readDormetoRoles reads the account's Dormeto roles (the names from the Vars, the defaults otherwise): the ones that
// exist, the AWS principals their trusts name (no duplicate), and whether the DmtRouterBoundary policy exists.
func readDormetoRoles(ctx context.Context, c Clients, s Settings, account string) (roles, principals []string, boundary bool, err error) {
	for _, r := range []string{roleName(s, "discovery_role_name", "DmtDiscoveryRole"), roleName(s, "command_role_name", "DmtCommandRole")} {
		out, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(r)})
		if isNotFound(err, "NoSuchEntity") {
			continue
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("read role %s: %w", r, err)
		}
		roles = append(roles, r)
		for _, p := range trustedPrincipals(aws.ToString(out.Role.AssumeRolePolicyDocument)) {
			if !slices.Contains(principals, p) {
				principals = append(principals, p)
			}
		}
	}
	_, err = c.IAM.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String("arn:aws:iam::" + account + ":policy/" + dormetoBoundary)})
	switch {
	case err == nil:
		boundary = true
	case !isNotFound(err, "NoSuchEntity"):
		return nil, nil, false, fmt.Errorf("read policy %s: %w", dormetoBoundary, err)
	}
	return roles, principals, boundary, nil
}

// isUniqueID tells a principal that is a unique id (AROA…): AWS shows that for a role deleted since the trust was written.
func isUniqueID(p string) bool { return !strings.HasPrefix(p, "arn:") }

// staleOnly: the trust names principals, all of them deleted roles (unique ids), so it is stale, not another manager's.
func staleOnly(principals []string) bool {
	return len(principals) > 0 && !slices.ContainsFunc(principals, func(p string) bool { return !isUniqueID(p) })
}

// principalArnOperators are the condition operators whose aws:PrincipalArn values narrow an account's root to roles.
var principalArnOperators = []string{"ArnLike", "ArnEquals", "StringLike", "StringEquals"}

// trustedPrincipals are the AWS principals a role's trust policy (a URL-encoded JSON document) names: an account's root
// narrowed by its statement's aws:PrincipalArn condition to the role ARNs of that account it names (how Dormeto's access
// module and release script write the trust), else the principals as they are (a role ARN, a root without such a
// condition, a deleted role's unique id).
func trustedPrincipals(doc string) []string {
	if d, err := url.QueryUnescape(doc); err == nil {
		doc = d
	}
	var p struct{ Statement json.RawMessage }
	if json.Unmarshal([]byte(doc), &p) != nil {
		return nil
	}
	stmts, err := statements(p.Statement)
	if err != nil {
		return nil
	}
	var out []string
	for _, raw := range stmts {
		var st struct{ Principal, Condition json.RawMessage }
		var pr struct{ AWS json.RawMessage }
		if json.Unmarshal(raw, &st) != nil || json.Unmarshal(st.Principal, &pr) != nil {
			continue
		}
		arns := conditionArns(st.Condition)
		for _, principal := range stringList(pr.AWS) {
			out = append(out, narrowed(principal, arns)...)
		}
	}
	return out
}

// conditionArns are the aws:PrincipalArn values of a statement's condition (the key's case does not matter).
func conditionArns(raw json.RawMessage) []string {
	var cond map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &cond) != nil {
		return nil
	}
	var out []string
	for _, op := range principalArnOperators {
		for key, v := range cond[op] {
			if strings.EqualFold(key, "aws:PrincipalArn") {
				out = append(out, stringList(v)...)
			}
		}
	}
	return out
}

// narrowed is what a principal lets assume a role under a condition naming arns: an account's root (its ARN, or its id)
// stands for the role ARNs of that account among arns; for an ARN of another account, the root itself.
func narrowed(principal string, arns []string) []string {
	account, root := rootAccount(principal)
	if !root || len(arns) == 0 {
		return []string{principal}
	}
	var out []string
	for _, a := range arns {
		if arnAccount(a) == account {
			out = append(out, a)
		} else if !slices.Contains(out, principal) {
			out = append(out, principal)
		}
	}
	return out
}

// rootAccount is the account of a principal that is an account's root (arn:<partition>:iam::<account>:root, or the bare
// account id).
func rootAccount(principal string) (string, bool) {
	if accountIDRe.MatchString(principal) {
		return principal, true
	}
	parts := strings.SplitN(principal, ":", 6)
	if len(parts) == 6 && parts[0] == "arn" && parts[2] == "iam" && parts[5] == "root" {
		return parts[4], true
	}
	return "", false
}

// arnAccount is the account field of an ARN, or empty.
func arnAccount(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return ""
	}
	return parts[4]
}

func checkRelease(f Found) Check {
	if f.ReleaseErr != nil {
		return failure("release", f.ReleaseErr)
	}
	d := "manager " + f.Release.Version
	if f.Release.Local != "" {
		d += " (local: " + f.Release.Local + ")"
	}
	return Check{"release", d, OK}
}

// covers tells whether a certificate name (exact, or a wildcard covering exactly one more label) covers host.
func covers(name, host string) bool {
	if rest, ok := strings.CutPrefix(name, "*."); ok {
		label, tail, ok := strings.Cut(host, ".")
		return ok && label != "" && strings.EqualFold(tail, rest)
	}
	return strings.EqualFold(name, host)
}

func checkDomain(ctx context.Context, c Clients, s Settings) Check {
	const name = "domain"
	d := s.Domain
	if d == nil {
		return Check{name, "not used (a cloudfront.net host)", Skip}
	}
	hosts := []string{d.Dashboard, d.AuthHost()}
	var bad []string
	cert, err := c.ACM.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(d.CertificateARN)})
	if err != nil {
		bad = append(bad, "certificate: "+err.Error())
	} else {
		cd := cert.Certificate
		if string(cd.Status) != "ISSUED" {
			bad = append(bad, fmt.Sprintf("the certificate is not issued (%s)", cd.Status))
		}
		names := append([]string{aws.ToString(cd.DomainName)}, cd.SubjectAlternativeNames...)
		for _, h := range hosts {
			if !slices.ContainsFunc(names, func(n string) bool { return covers(n, h) }) {
				bad = append(bad, "the certificate does not cover "+h)
			}
		}
	}
	zone, err := c.Route53.GetHostedZone(ctx, &route53.GetHostedZoneInput{Id: aws.String(d.ZoneID)})
	switch {
	case isNotFound(err, "NoSuchHostedZone"):
		bad = append(bad, fmt.Sprintf("zone %s not found", d.ZoneID))
	case err != nil:
		bad = append(bad, "zone: "+err.Error())
	default:
		zn := strings.TrimSuffix(aws.ToString(zone.HostedZone.Name), ".")
		for _, h := range hosts {
			if !strings.EqualFold(h, zn) && !strings.HasSuffix(strings.ToLower(h), "."+strings.ToLower(zn)) {
				bad = append(bad, fmt.Sprintf("%s is outside the zone %s", h, zn))
			}
		}
	}
	if len(bad) > 0 {
		return Check{name, strings.Join(bad, "; "), Fail}
	}
	return Check{name, strings.Join(hosts, ", ") + ": certificate and zone", OK}
}

func checkSES(ctx context.Context, c Clients, s Settings) Check {
	const name = "SES"
	if s.SESIdentity == "" {
		return Check{name, "not used (Cognito sends the invitations, about 50 a day)", Skip}
	}
	unverified := Check{name, fmt.Sprintf("%s is not verified for sending in %s", s.SESIdentity, s.Region), Fail}
	id, err := c.SESv2.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(s.SESIdentity)})
	switch {
	case isNotFound(err, "NotFoundException"):
		return unverified
	case err != nil:
		return failure(name, err)
	case !id.VerifiedForSendingStatus:
		return unverified
	}
	acct, err := c.SESv2.GetAccount(ctx, &sesv2.GetAccountInput{})
	switch {
	case isDenied(err):
		return Check{name, "not checked (sesv2:GetAccount refused): the SES sandbox allows verified addresses only", Warn}
	case err != nil:
		return failure(name, err)
	case !acct.ProductionAccessEnabled:
		return Check{name, "SES sandbox: invitations reach verified addresses only", Warn}
	}
	return Check{name, s.SESIdentity + " verified for sending", OK}
}
