package aws

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/discover"
)

type fakeSTS struct{ assumed []*sts.AssumeRoleInput }

func (f *fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: awssdk.String("111111111111")}, nil
}

func (f *fakeSTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	f.assumed = append(f.assumed, in)
	if awssdk.ToString(in.RoleArn) == "arn:aws:iam::333333333333:role/OrganizationAccountAccessRole" {
		return nil, &smithy.OperationError{ServiceID: "STS", OperationName: "AssumeRole", Err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: sts:AssumeRole"}}
	}
	expires := time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC)
	return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
		AccessKeyId: awssdk.String("ASIA"), SecretAccessKey: awssdk.String("s"), SessionToken: awssdk.String("t"), Expiration: &expires,
	}}, nil
}

// fakeOrg is an organization: a root with the management account and an OU holding a member.
type fakeOrg struct {
	describeErr error
	asked       []any
}

func (f *fakeOrg) DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return &organizations.DescribeOrganizationOutput{Organization: &orgtypes.Organization{Id: awssdk.String("o-abc123")}}, nil
}

func (f *fakeOrg) ListRoots(_ context.Context, in *organizations.ListRootsInput, _ ...func(*organizations.Options)) (*organizations.ListRootsOutput, error) {
	f.asked = append(f.asked, in)
	return &organizations.ListRootsOutput{Roots: []orgtypes.Root{{Id: awssdk.String("r-root"), Name: awssdk.String("Root")}}}, nil
}

func (f *fakeOrg) ListOrganizationalUnitsForParent(_ context.Context, in *organizations.ListOrganizationalUnitsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error) {
	f.asked = append(f.asked, in)
	if awssdk.ToString(in.ParentId) == "r-root" {
		return &organizations.ListOrganizationalUnitsForParentOutput{OrganizationalUnits: []orgtypes.OrganizationalUnit{{Id: awssdk.String("ou-prod"), Name: awssdk.String("Prod")}}}, nil
	}
	return &organizations.ListOrganizationalUnitsForParentOutput{}, nil
}

func (f *fakeOrg) ListAccountsForParent(_ context.Context, in *organizations.ListAccountsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsForParentOutput, error) {
	f.asked = append(f.asked, in)
	switch awssdk.ToString(in.ParentId) {
	case "r-root":
		if in.NextToken == nil {
			return &organizations.ListAccountsForParentOutput{NextToken: awssdk.String("p2"), Accounts: []orgtypes.Account{{Id: awssdk.String("111111111111"), Name: awssdk.String("management"), State: orgtypes.AccountStateActive}}}, nil
		}
		return &organizations.ListAccountsForParentOutput{Accounts: []orgtypes.Account{{Id: awssdk.String("444444444444"), Name: awssdk.String("closed"), State: orgtypes.AccountStateClosed}}}, nil
	case "ou-prod":
		return &organizations.ListAccountsForParentOutput{Accounts: []orgtypes.Account{{Id: awssdk.String("222222222222"), Name: awssdk.String("prod"), Status: orgtypes.AccountStatusActive}}}, nil
	}
	return &organizations.ListAccountsForParentOutput{}, nil
}

func (f *fakeOrg) ListTagsForResource(_ context.Context, in *organizations.ListTagsForResourceInput, _ ...func(*organizations.Options)) (*organizations.ListTagsForResourceOutput, error) {
	f.asked = append(f.asked, in)
	return &organizations.ListTagsForResourceOutput{Tags: []orgtypes.Tag{{Key: awssdk.String("team"), Value: awssdk.String("web")}}}, nil
}

func TestTheAccountsAdapterWhoamiTheOrganizationAndItsActiveAccounts(t *testing.T) {
	org := &fakeOrg{}
	a := &accountsPort{sts: &fakeSTS{}, organizations: organizationsPort{org}}
	ctx := context.Background()
	if self, err := a.Whoami(ctx); err != nil || self != "111111111111" {
		t.Errorf("whoami %q %v", self, err)
	}
	if id, err := a.Organization(ctx); err != nil || id != "o-abc123" {
		t.Errorf("organization %q %v", id, err)
	}
	inv, err := a.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]string
	for _, acc := range inv.Accounts {
		got = append(got, [2]string{acc.ID, acc.Name})
	}
	if !reflect.DeepEqual(got, [][2]string{{"111111111111", "management"}, {"222222222222", "prod"}}) {
		t.Errorf("the active accounts %v", got)
	}
	// The inventory without tags (the estimate's): no ListTagsForResource.
	for _, in := range org.asked {
		if _, ok := in.(*organizations.ListTagsForResourceInput); ok {
			t.Error("tags read")
		}
	}
	tags, err := organizationsPort{org}.ListTagsForResource(ctx, discover.ListTagsForResourceInput{ResourceID: "222222222222", NextToken: "t"})
	if err != nil || !reflect.DeepEqual(tags.Tags, []discover.Tag{{Key: "team", Value: "web"}}) {
		t.Errorf("%+v %v", tags, err)
	}
	if last := org.asked[len(org.asked)-1]; !reflect.DeepEqual(last, &organizations.ListTagsForResourceInput{ResourceId: awssdk.String("222222222222"), NextToken: awssdk.String("t")}) {
		t.Errorf("asked %+v", last)
	}
}

func TestTheAccountsAdapterAssumesARoleAsTheNodeEstimate(t *testing.T) {
	s := &fakeSTS{}
	a := &accountsPort{sts: s, organizations: organizationsPort{&fakeOrg{}}}
	c, err := a.Assume(context.Background(), "222222222222", "OrganizationAccountAccessRole")
	if err != nil {
		t.Fatal(err)
	}
	want := awssdk.Credentials{AccessKeyID: "ASIA", SecretAccessKey: "s", SessionToken: "t", CanExpire: true, Expires: time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC)}
	if c != want {
		t.Errorf("%+v", c)
	}
	asked := &sts.AssumeRoleInput{RoleArn: awssdk.String("arn:aws:iam::222222222222:role/OrganizationAccountAccessRole"), RoleSessionName: awssdk.String("dormeto-estimate"), DurationSeconds: awssdk.Int32(3600)}
	if !reflect.DeepEqual(s.assumed, []*sts.AssumeRoleInput{asked}) {
		t.Errorf("asked %+v", s.assumed[0])
	}
	// A refusal is the SDK's error, its code kept.
	_, err = a.Assume(context.Background(), "333333333333", "OrganizationAccountAccessRole")
	var api smithy.APIError
	if !errors.As(err, &api) || api.ErrorCode() != "AccessDenied" {
		t.Errorf("%v", err)
	}
}

func TestTheAccountsAdapterKeepsTheOrganizationsErrorCode(t *testing.T) {
	notInUse := &smithy.GenericAPIError{Code: "AWSOrganizationsNotInUseException", Message: "Your account is not a member of an organization."}
	a := &accountsPort{sts: &fakeSTS{}, organizations: organizationsPort{&fakeOrg{describeErr: notInUse}}}
	if _, err := a.Organization(context.Background()); !errors.Is(err, notInUse) {
		t.Errorf("%v", err)
	}
}
