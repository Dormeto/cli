// Package fleet is the CLI's client of the manager's IAM-authorized API: the lookup of the API by name, the SigV4-signed
// calls, and what the routers and workloads commands build on them. The estimate never imports it (importguard_test.go).
package fleet

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
)

// ErrNoAPI is the lookup's answer when no API has the manager's name in the region.
var ErrNoAPI = errors.New("no manager API found")

// Locator finds the manager's API endpoint of an environment.
type Locator interface {
	APIURL(ctx context.Context, env, region string) (string, error)
}

// apisAPI is the apigatewayv2 client's call the lookup makes.
type apisAPI interface {
	GetApis(ctx context.Context, in *apigatewayv2.GetApisInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.GetApisOutput, error)
}

type apiLocator struct{ api apisAPI }

// NewLocator is the Locator over API Gateway in cfg's region, with the standard retryer (5 attempts).
func NewLocator(cfg aws.Config) Locator {
	return &apiLocator{api: apigatewayv2.NewFromConfig(cfg, func(o *apigatewayv2.Options) {
		o.RetryMode, o.RetryMaxAttempts = aws.RetryModeStandard, 0
		o.Retryer = retry.NewStandard(func(so *retry.StandardOptions) { so.MaxAttempts = 5 })
	})}
}

// APIURL is the endpoint of the API named dmt-<env>-manager-api (Terraform's name), looking through every page.
func (l *apiLocator) APIURL(ctx context.Context, env, region string) (string, error) {
	name := "dmt-" + env + "-manager-api"
	var token *string
	for {
		out, err := l.api.GetApis(ctx, &apigatewayv2.GetApisInput{NextToken: token})
		if err != nil {
			return "", fmt.Errorf("list the APIs of %s: %w", region, err)
		}
		for _, a := range out.Items {
			if aws.ToString(a.Name) == name && aws.ToString(a.ApiEndpoint) != "" {
				return aws.ToString(a.ApiEndpoint), nil
			}
		}
		if out.NextToken == nil {
			return "", fmt.Errorf("%w: no API named %s for env %s in region %s", ErrNoAPI, name, env, region)
		}
		token = out.NextToken
	}
}
