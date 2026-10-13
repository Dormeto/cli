package aws

// The accounts a profile reaches (accounts.Ports): STS (GetCallerIdentity,
// AssumeRole) and Organizations (DescribeOrganization, the inventory's
// listings, discover.OrganizationsPort), in us-east-1 with the profile's
// configuration. Ports manager packages/estimate/src/bin.ts (awsOf's
// accounts) and packages/discovery/src/aws.ts (createAssumeRole,
// organizationsPort).

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/discover"
)

// sessionName names the estimate's role sessions (in the member accounts'
// CloudTrail); a session lasts an hour.
const (
	sessionName      = "dormeto-estimate"
	sessionSeconds   = 3600
	defaultPartition = "aws"
)

type stsAPI interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
	AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

type organizationsAPI interface {
	DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error)
	ListRoots(context.Context, *organizations.ListRootsInput, ...func(*organizations.Options)) (*organizations.ListRootsOutput, error)
	ListOrganizationalUnitsForParent(context.Context, *organizations.ListOrganizationalUnitsForParentInput, ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error)
	ListAccountsForParent(context.Context, *organizations.ListAccountsForParentInput, ...func(*organizations.Options)) (*organizations.ListAccountsForParentOutput, error)
	ListTagsForResource(context.Context, *organizations.ListTagsForResourceInput, ...func(*organizations.Options)) (*organizations.ListTagsForResourceOutput, error)
}

type accountsPort struct {
	sts           stsAPI
	organizations organizationsPort
}

// NewAccounts is a profile's accounts.Ports: STS and Organizations in
// us-east-1, the SDK's standard retries.
func NewAccounts(cfg awssdk.Config) accounts.Ports {
	return &accountsPort{
		sts:           sts.NewFromConfig(cfg, func(o *sts.Options) { o.Region = global }),
		organizations: organizationsPort{organizations.NewFromConfig(cfg, func(o *organizations.Options) { o.Region = global })},
	}
}

// Whoami is the profile's account.
func (a *accountsPort) Whoami(ctx context.Context) (string, error) {
	out, err := a.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", err
	}
	return awssdk.ToString(out.Account), nil
}

// Organization is the profile's organization's id.
func (a *accountsPort) Organization(ctx context.Context) (string, error) {
	out, err := a.organizations.client.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	if err != nil {
		return "", err
	}
	if out.Organization == nil {
		return "", nil
	}
	return awssdk.ToString(out.Organization.Id), nil
}

// Inventory is the organization's active accounts and OU tree, without tags.
func (a *accountsPort) Inventory(ctx context.Context) (discover.OrgInventory, error) {
	return discover.LoadInventory(ctx, a.organizations, false)
}

// Assume assumes the role of that name in the account for an hour.
func (a *accountsPort) Assume(ctx context.Context, account, role string) (awssdk.Credentials, error) {
	out, err := a.sts.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         awssdk.String(fmt.Sprintf("arn:%s:iam::%s:role/%s", defaultPartition, account, role)),
		RoleSessionName: awssdk.String(sessionName), DurationSeconds: awssdk.Int32(sessionSeconds),
	})
	if err != nil {
		return awssdk.Credentials{}, err
	}
	c := out.Credentials
	if c == nil {
		return awssdk.Credentials{}, fmt.Errorf("AssumeRole %s in %s gave no credentials", role, account)
	}
	creds := awssdk.Credentials{AccessKeyID: awssdk.ToString(c.AccessKeyId), SecretAccessKey: awssdk.ToString(c.SecretAccessKey), SessionToken: awssdk.ToString(c.SessionToken)}
	if c.Expiration != nil {
		creds.CanExpire, creds.Expires = true, *c.Expiration
	}
	return creds, nil
}

// organizationsPort is discover.OrganizationsPort.
type organizationsPort struct{ client organizationsAPI }

func (p organizationsPort) ListRoots(ctx context.Context, in discover.ListRootsInput) (discover.ListRootsOutput, error) {
	out, err := p.client.ListRoots(ctx, &organizations.ListRootsInput{NextToken: optional(in.NextToken)})
	if err != nil {
		return discover.ListRootsOutput{}, err
	}
	res := discover.ListRootsOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, r := range out.Roots {
		res.Roots = append(res.Roots, discover.Root{ID: awssdk.ToString(r.Id), Name: awssdk.ToString(r.Name)})
	}
	return res, nil
}

func (p organizationsPort) ListOrganizationalUnitsForParent(ctx context.Context, in discover.ListOrganizationalUnitsForParentInput) (discover.ListOrganizationalUnitsForParentOutput, error) {
	out, err := p.client.ListOrganizationalUnitsForParent(ctx, &organizations.ListOrganizationalUnitsForParentInput{ParentId: optional(in.ParentID), NextToken: optional(in.NextToken)})
	if err != nil {
		return discover.ListOrganizationalUnitsForParentOutput{}, err
	}
	res := discover.ListOrganizationalUnitsForParentOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, u := range out.OrganizationalUnits {
		res.OrganizationalUnits = append(res.OrganizationalUnits, discover.OrganizationalUnit{ID: awssdk.ToString(u.Id), Name: awssdk.ToString(u.Name)})
	}
	return res, nil
}

func (p organizationsPort) ListAccountsForParent(ctx context.Context, in discover.ListAccountsForParentInput) (discover.ListAccountsForParentOutput, error) {
	out, err := p.client.ListAccountsForParent(ctx, &organizations.ListAccountsForParentInput{ParentId: optional(in.ParentID), NextToken: optional(in.NextToken)})
	if err != nil {
		return discover.ListAccountsForParentOutput{}, err
	}
	res := discover.ListAccountsForParentOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, a := range out.Accounts {
		res.Accounts = append(res.Accounts, discover.OrgAccount{ID: awssdk.ToString(a.Id), Name: awssdk.ToString(a.Name), State: string(a.State), Status: string(a.Status)})
	}
	return res, nil
}

func (p organizationsPort) ListTagsForResource(ctx context.Context, in discover.ListTagsForResourceInput) (discover.ListTagsForResourceOutput, error) {
	out, err := p.client.ListTagsForResource(ctx, &organizations.ListTagsForResourceInput{ResourceId: optional(in.ResourceID), NextToken: optional(in.NextToken)})
	if err != nil {
		return discover.ListTagsForResourceOutput{}, err
	}
	res := discover.ListTagsForResourceOutput{NextToken: awssdk.ToString(out.NextToken)}
	for _, t := range out.Tags {
		res.Tags = append(res.Tags, discover.Tag{Key: awssdk.ToString(t.Key), Value: awssdk.ToString(t.Value)})
	}
	return res, nil
}
