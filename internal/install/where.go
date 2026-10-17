package install

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Organization is the AWS organization the account belongs to.
type Organization struct {
	ID          string // o-…
	Management  string // the management account's id
	AllFeatures bool   // FeatureSet == "ALL"
}

// Where is where the install runs: the account the profile reaches, and its organization.
type Where struct {
	Account, Alias, Region, Caller string        // Caller: the caller's ARN
	Organization                   *Organization // nil: not in an organization, or unknown
	OrganizationUnknown            bool          // DescribeOrganization refused (access denied)
}

// Locate reads the caller's account, its alias and its organization. Only the caller identity and unexpected
// organization errors are returned; a missing alias or organization is a value.
func Locate(ctx context.Context, c Clients, region string) (Where, error) {
	id, err := c.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Where{}, err
	}
	w := Where{Account: aws.ToString(id.Account), Caller: aws.ToString(id.Arn), Region: region}

	if out, err := c.IAM.ListAccountAliases(ctx, &iam.ListAccountAliasesInput{}); err == nil && len(out.AccountAliases) > 0 {
		w.Alias = out.AccountAliases[0]
	}

	out, err := c.Organizations.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	var notInUse *orgtypes.AWSOrganizationsNotInUseException
	var denied *orgtypes.AccessDeniedException
	switch {
	case errors.As(err, &notInUse):
	case errors.As(err, &denied):
		w.OrganizationUnknown = true
	case err != nil:
		return Where{}, err
	default:
		o := out.Organization
		w.Organization = &Organization{
			ID:          aws.ToString(o.Id),
			Management:  aws.ToString(o.MasterAccountId),
			AllFeatures: o.FeatureSet == orgtypes.OrganizationFeatureSetAll,
		}
	}
	return w, nil
}

// Suggest is the mode to suggest ("organization" or "account") and why.
func Suggest(w Where) (mode, why string) {
	switch o := w.Organization; {
	case o != nil && o.AllFeatures:
		return "organization", fmt.Sprintf("the manager reads every account of %s", o.ID)
	case o != nil:
		return "account", fmt.Sprintf("the organization %s has consolidated billing only: organization mode needs all features (the delegation policy)", o.ID)
	case w.OrganizationUnknown:
		return "account", "the organization could not be read (organizations:DescribeOrganization refused): the manager reads this account only"
	default:
		return "account", "not in an organization: the manager reads this account only"
	}
}

// IsManagement tells whether the account is its organization's management account.
func (w Where) IsManagement() bool {
	return w.Organization != nil && w.Account == w.Organization.Management
}

// Describe is the one line that says where the install runs.
func (w Where) Describe() string {
	s := "account " + w.Account
	if w.Alias != "" {
		s += " (" + w.Alias + ")"
	}
	switch {
	case w.Organization != nil:
		s += fmt.Sprintf(" in the organization %s (management %s)", w.Organization.ID, w.Organization.Management)
		if w.IsManagement() {
			s += "; this is the organization's management account: AWS advises against workloads there, a member account is suggested"
		}
	case w.OrganizationUnknown:
		s += ", organization unknown"
	default:
		s += ", not in an organization"
	}
	return s
}
