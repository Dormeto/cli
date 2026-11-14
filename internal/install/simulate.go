package install

// The simulation of the caller's rights in IAM, which `install check` makes for the install's own actions and
// `dormeto aws check` for every method's.

import (
	"context"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// Simulator is the IAM calls a simulation makes.
type Simulator interface {
	GetRole(ctx context.Context, in *iam.GetRoleInput, optFns ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	SimulatePrincipalPolicy(ctx context.Context, in *iam.SimulatePrincipalPolicyInput, optFns ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error)
}

// RightsActions are the actions the install's own calls need (and the uninstall's): a copy.
func RightsActions() []string { return slices.Clone(rightsActions) }

// Refused tells an AWS refusal for lack of permission (AccessDenied…, UnauthorizedOperation).
func Refused(err error) bool { return isDenied(err) }

// PolicySource is the ARN IAM simulates for the caller (its ARN): an assumed role's role, read with iam:GetRole, else the
// caller itself. Root is true for the account's root, which holds every right and cannot be simulated.
func PolicySource(ctx context.Context, c Simulator, caller string) (source string, root bool, err error) {
	kind, who := principal(caller)
	switch kind {
	case "root":
		return "", true, nil
	case "assumed-role":
		out, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(who)})
		if err != nil {
			return "", false, err
		}
		return aws.ToString(out.Role.Arn), false, nil
	}
	return caller, false, nil
}

// Grant is an action on a resource.
type Grant struct{ Action, Resource string }

// Denied are the grants of actions on resources that IAM's simulation of source does not allow, every page read. A
// result per resource (ResourceSpecificResults) is read as such; a result without a resource is the only one asked.
func Denied(ctx context.Context, c Simulator, source string, actions, resources []string) (map[Grant]bool, error) {
	denied := map[Grant]bool{}
	var marker *string
	for {
		out, err := c.SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
			PolicySourceArn: aws.String(source), ActionNames: actions, ResourceArns: resources, Marker: marker,
		})
		if err != nil {
			return nil, err
		}
		for _, r := range out.EvaluationResults {
			action := aws.ToString(r.EvalActionName)
			if len(r.ResourceSpecificResults) > 0 {
				for _, rr := range r.ResourceSpecificResults {
					if rr.EvalResourceDecision != iamtypes.PolicyEvaluationDecisionTypeAllowed {
						denied[Grant{action, aws.ToString(rr.EvalResourceName)}] = true
					}
				}
				continue
			}
			if r.EvalDecision == iamtypes.PolicyEvaluationDecisionTypeAllowed {
				continue
			}
			resource := aws.ToString(r.EvalResourceName)
			if resource == "" && len(resources) == 1 {
				resource = resources[0]
			}
			denied[Grant{action, resource}] = true
		}
		if !out.IsTruncated || out.Marker == nil {
			return denied, nil
		}
		marker = out.Marker
	}
}
