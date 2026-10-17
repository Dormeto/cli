// Package installaws builds the AWS SDK for Go v2 clients of the install's ports (install.Clients) and the
// configuration of an assumed role. Apart from internal/aws (the estimate's read-only adapters), which the estimate
// alone uses: the estimate never imports the install (importguard_test.go).
package installaws

import (
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Dormeto/cli/internal/install"
)

// attempts is how many times a client tries a call.
const attempts = 5

// certificates is the region of CloudFront's certificates (ACM).
const certificates = "us-east-1"

// standard is the SDK's standard retryer trying a call n times.
func standard(n int) awssdk.Retryer {
	return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = n })
}

// Clients is every service's client from cfg in region (ACM in us-east-1, where CloudFront reads its certificates; IAM,
// STS and Organizations keep region, the SDK resolves their global endpoints), with the standard retryer, 5 attempts.
func Clients(cfg awssdk.Config, region string) install.Clients {
	return install.Clients{
		STS: sts.NewFromConfig(cfg, func(o *sts.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		IAM: iam.NewFromConfig(cfg, func(o *iam.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		S3: s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		CodeBuild: codebuild.NewFromConfig(cfg, func(o *codebuild.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		Logs: cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		Organizations: organizations.NewFromConfig(cfg, func(o *organizations.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		EC2: ec2.NewFromConfig(cfg, func(o *ec2.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		ACM: acm.NewFromConfig(cfg, func(o *acm.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = certificates, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		Route53: route53.NewFromConfig(cfg, func(o *route53.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		SESv2: sesv2.NewFromConfig(cfg, func(o *sesv2.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		Lambda: lambda.NewFromConfig(cfg, func(o *lambda.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		Cognito: cognitoidentityprovider.NewFromConfig(cfg, func(o *cognitoidentityprovider.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
		DynamoDB: dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
			o.Region, o.Retryer, o.RetryMode, o.RetryMaxAttempts = region, standard(attempts), awssdk.RetryModeStandard, 0
		}),
	}
}

// Assumed is a copy of cfg in region whose credentials are the role's, assumed with cfg's credentials through STS in
// region (a profile may have none: the run's comes from --region or the question; session names the session) and
// cached, refreshed before they expire.
func Assumed(cfg awssdk.Config, region, roleARN, session string) awssdk.Config {
	cfg = cfg.Copy()
	cfg.Region = region
	provider := stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), roleARN, func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = session })
	cfg.Credentials = awssdk.NewCredentialsCache(provider)
	return cfg
}
