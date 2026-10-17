package install

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type fakeSTS struct {
	STS
	account, arn string
	err          error
}

func (f fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sts.GetCallerIdentityOutput{Account: &f.account, Arn: &f.arn}, nil
}

type fakeIAM struct {
	IAM
	aliases []string
	err     error
}

func (f fakeIAM) ListAccountAliases(context.Context, *iam.ListAccountAliasesInput, ...func(*iam.Options)) (*iam.ListAccountAliasesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &iam.ListAccountAliasesOutput{AccountAliases: f.aliases}, nil
}

type fakeOrgs struct {
	Organizations
	org *orgtypes.Organization
	err error
}

func (f fakeOrgs) DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &organizations.DescribeOrganizationOutput{Organization: f.org}, nil
}

func ptr(s string) *string { return &s }

func org(features orgtypes.OrganizationFeatureSet) *orgtypes.Organization {
	return &orgtypes.Organization{Id: ptr("o-vvk0115ixn"), MasterAccountId: ptr("809706483662"), FeatureSet: features}
}

func locate(t *testing.T, s fakeSTS, i fakeIAM, o fakeOrgs) (Where, error) {
	t.Helper()
	return Locate(context.Background(), Clients{STS: s, IAM: i, Organizations: o}, "eu-west-1")
}

var member = fakeSTS{account: "532105290256", arn: "arn:aws:iam::532105290256:user/me"}

func TestLocateOrganizationAllFeatures(t *testing.T) {
	w, err := locate(t, member, fakeIAM{aliases: []string{"sandbox", "other"}}, fakeOrgs{org: org(orgtypes.OrganizationFeatureSetAll)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Account != "532105290256" || w.Alias != "sandbox" || w.Region != "eu-west-1" || w.Caller != member.arn {
		t.Errorf("where = %+v", w)
	}
	if w.Organization == nil || !w.Organization.AllFeatures || w.Organization.Management != "809706483662" {
		t.Errorf("organization = %+v", w.Organization)
	}
	mode, why := Suggest(w)
	if mode != "organization" || why != "the manager reads every account of o-vvk0115ixn" {
		t.Errorf("suggest = %q, %q", mode, why)
	}
	want := "account 532105290256 (sandbox) in the organization o-vvk0115ixn (management 809706483662)"
	if got := w.Describe(); got != want {
		t.Errorf("describe = %q", got)
	}
	if w.IsManagement() {
		t.Error("member is not the management account")
	}
}

func TestLocateConsolidatedBilling(t *testing.T) {
	w, err := locate(t, member, fakeIAM{}, fakeOrgs{org: org(orgtypes.OrganizationFeatureSetConsolidatedBilling)})
	if err != nil {
		t.Fatal(err)
	}
	mode, why := Suggest(w)
	if mode != "account" || !strings.Contains(why, "consolidated billing only") || !strings.Contains(why, "all features") {
		t.Errorf("suggest = %q, %q", mode, why)
	}
	if got := w.Describe(); strings.Contains(got, "(") && strings.Contains(got, "()") {
		t.Errorf("describe = %q", got)
	}
}

func TestLocateNotInOrganization(t *testing.T) {
	w, err := locate(t, fakeSTS{account: "111122223333"}, fakeIAM{}, fakeOrgs{err: &orgtypes.AWSOrganizationsNotInUseException{}})
	if err != nil {
		t.Fatal(err)
	}
	if w.Organization != nil || w.OrganizationUnknown {
		t.Errorf("where = %+v", w)
	}
	mode, why := Suggest(w)
	if mode != "account" || why != "not in an organization: the manager reads this account only" {
		t.Errorf("suggest = %q, %q", mode, why)
	}
	if got := w.Describe(); got != "account 111122223333, not in an organization" {
		t.Errorf("describe = %q", got)
	}
}

func TestLocateOrganizationRefused(t *testing.T) {
	w, err := locate(t, fakeSTS{account: "111122223333"}, fakeIAM{}, fakeOrgs{err: &orgtypes.AccessDeniedException{}})
	if err != nil {
		t.Fatal(err)
	}
	if w.Organization != nil || !w.OrganizationUnknown {
		t.Errorf("where = %+v", w)
	}
	mode, why := Suggest(w)
	if mode != "account" || !strings.Contains(why, "organizations:DescribeOrganization refused") {
		t.Errorf("suggest = %q, %q", mode, why)
	}
	if got := w.Describe(); got != "account 111122223333, organization unknown" {
		t.Errorf("describe = %q", got)
	}
}

func TestLocateManagementAccount(t *testing.T) {
	w, err := locate(t, fakeSTS{account: "809706483662"}, fakeIAM{}, fakeOrgs{org: org(orgtypes.OrganizationFeatureSetAll)})
	if err != nil {
		t.Fatal(err)
	}
	if !w.IsManagement() {
		t.Error("expected the management account")
	}
	want := "account 809706483662 in the organization o-vvk0115ixn (management 809706483662); this is the organization's management account: AWS advises against workloads there, a member account is suggested"
	if got := w.Describe(); got != want {
		t.Errorf("describe = %q", got)
	}
}

func TestLocateAliasesDenied(t *testing.T) {
	w, err := locate(t, member, fakeIAM{err: errors.New("AccessDenied")}, fakeOrgs{org: org(orgtypes.OrganizationFeatureSetAll)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Alias != "" {
		t.Errorf("alias = %q", w.Alias)
	}
}

func TestLocateCallerIdentityFails(t *testing.T) {
	boom := errors.New("ExpiredToken: the security token is expired")
	_, err := locate(t, fakeSTS{err: boom}, fakeIAM{}, fakeOrgs{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestLocateOrganizationOtherError(t *testing.T) {
	boom := errors.New("throttled")
	_, err := locate(t, member, fakeIAM{}, fakeOrgs{err: boom})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}
