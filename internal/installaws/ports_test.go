package installaws

import (
	"github.com/Dormeto/cli/internal/install"
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
)

// Each SDK client satisfies its port.
var (
	_ install.STS           = (*sts.Client)(nil)
	_ install.IAM           = (*iam.Client)(nil)
	_ install.S3            = (*s3.Client)(nil)
	_ install.CodeBuild     = (*codebuild.Client)(nil)
	_ install.Logs          = (*cloudwatchlogs.Client)(nil)
	_ install.Organizations = (*organizations.Client)(nil)
	_ install.EC2           = (*ec2.Client)(nil)
	_ install.ACM           = (*acm.Client)(nil)
	_ install.Route53       = (*route53.Client)(nil)
	_ install.SESv2         = (*sesv2.Client)(nil)
	_ install.Lambda        = (*lambda.Client)(nil)
	_ install.Cognito       = (*cognitoidentityprovider.Client)(nil)
	_ install.DynamoDB      = (*dynamodb.Client)(nil)
)
