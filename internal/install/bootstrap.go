package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Dormeto/cli/internal/install/build"
)

const (
	adminPolicyARN = "arn:aws:iam::aws:policy/AdministratorAccess"
	rolePath       = "/dormeto/"
	logRetention   = 90
)

// The wait for IAM's eventual consistency: a project created right after its role is refused until the role is usable.
// Package variables so that tests run without waiting.
var (
	bootSleep = time.Sleep
	bootNow   = time.Now
)

const (
	consistencyEvery = 5 * time.Second
	consistencyWait  = 60 * time.Second
)

// Bootstrap creates, or updates to what this version expects, the install's own resources in the account: the state
// bucket, the build role (unless buildRole, an ARN, is given: it is used as is), the log group, the scripts and the
// CodeBuild project. Running it again is safe.
func Bootstrap(ctx context.Context, c Clients, p Project, buildRole string) error {
	if err := ensureBucket(ctx, c, p); err != nil {
		return err
	}
	roleARN := buildRole
	if roleARN == "" {
		var err error
		if roleARN, err = ensureBuildRole(ctx, c, p); err != nil {
			return err
		}
	}
	if err := ensureLogGroup(ctx, c, p); err != nil {
		return err
	}
	if err := uploadScripts(ctx, c, p); err != nil {
		return err
	}
	return ensureProject(ctx, c, p, roleARN, "manager.sh")
}

func ensureBucket(ctx context.Context, c Clients, p Project) error {
	_, err := c.S3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &p.Bucket})
	switch code := apiCode(err); {
	case err == nil:
	case code == "NotFound" || code == "NoSuchBucket" || code == "404":
		in := &s3.CreateBucketInput{Bucket: &p.Bucket}
		if p.Region != "us-east-1" {
			in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(p.Region)}
		}
		if _, err := c.S3.CreateBucket(ctx, in); err != nil {
			return fmt.Errorf("create bucket %s: %w", p.Bucket, err)
		}
	case code == "Forbidden" || code == "403" || isDenied(err):
		return fmt.Errorf("bucket %s exists in another account", p.Bucket)
	default:
		return fmt.Errorf("check bucket %s: %w", p.Bucket, err)
	}

	steps := []struct {
		what string
		do   func() error
	}{
		{"versioning", func() error {
			_, err := c.S3.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: &p.Bucket,
				VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled}})
			return err
		}},
		{"encryption", func() error {
			_, err := c.S3.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{Bucket: &p.Bucket,
				ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{Rules: []s3types.ServerSideEncryptionRule{{
					ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{SSEAlgorithm: s3types.ServerSideEncryptionAes256}}}}})
			return err
		}},
		{"public access block", func() error {
			_, err := c.S3.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{Bucket: &p.Bucket,
				PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
					BlockPublicAcls: aws.Bool(true), BlockPublicPolicy: aws.Bool(true), IgnorePublicAcls: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true)}})
			return err
		}},
		{"policy", func() error {
			_, err := c.S3.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: &p.Bucket, Policy: aws.String(bucketPolicy(p.Bucket))})
			return err
		}},
		{"lifecycle", func() error {
			_, err := c.S3.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: &p.Bucket,
				LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{Rules: []s3types.LifecycleRule{{
					ID: aws.String("noncurrent-90-days"), Status: s3types.ExpirationStatusEnabled, Filter: &s3types.LifecycleRuleFilter{Prefix: aws.String("")},
					NoncurrentVersionExpiration: &s3types.NoncurrentVersionExpiration{NoncurrentDays: aws.Int32(90)}}}}})
			return err
		}},
		{"tags", func() error {
			var tags []s3types.Tag
			for _, k := range sortedKeys(p.Tags()) {
				tags = append(tags, s3types.Tag{Key: aws.String(k), Value: aws.String(p.Tags()[k])})
			}
			_, err := c.S3.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: &p.Bucket, Tagging: &s3types.Tagging{TagSet: tags}})
			return err
		}},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			return fmt.Errorf("bucket %s %s: %w", p.Bucket, s.what, err)
		}
	}
	return nil
}

// bucketPolicy refuses every request that is not over TLS, to the bucket and its objects.
func bucketPolicy(bucket string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"DenyInsecureTransport","Effect":"Deny","Principal":"*","Action":"s3:*",`+
		`"Resource":["arn:aws:s3:::%[1]s","arn:aws:s3:::%[1]s/*"],"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`, bucket)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func trustPolicy(p Project) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"codebuild.amazonaws.com"},"Action":"sts:AssumeRole",`+
		`"Condition":{"StringEquals":{"aws:SourceAccount":"%s"},"ArnLike":{"aws:SourceArn":"arn:aws:codebuild:%s:%s:project/%s"}}}]}`,
		p.Account, p.Region, p.Account, p.Name)
}

// ensureBuildRole creates the build role (administrator access: Terraform creates the manager's resources with it) or
// brings an existing one to the expected trust, and returns its ARN.
func ensureBuildRole(ctx context.Context, c Clients, p Project) (string, error) {
	got, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: &p.Role})
	var arn string
	switch {
	case err == nil:
		if got.Role == nil || aws.ToString(got.Role.Path) != rolePath {
			return "", fmt.Errorf("role %s exists outside %s: not this install's", p.Role, rolePath)
		}
		arn = aws.ToString(got.Role.Arn)
		if _, err := c.IAM.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{RoleName: &p.Role, PolicyDocument: aws.String(trustPolicy(p))}); err != nil {
			return "", fmt.Errorf("update role %s: %w", p.Role, err)
		}
	case isNotFound(err, "NoSuchEntity"):
		var tags []iamtypes.Tag
		for _, k := range sortedKeys(p.Tags()) {
			tags = append(tags, iamtypes.Tag{Key: aws.String(k), Value: aws.String(p.Tags()[k])})
		}
		created, err := c.IAM.CreateRole(ctx, &iam.CreateRoleInput{RoleName: &p.Role, Path: aws.String(rolePath), AssumeRolePolicyDocument: aws.String(trustPolicy(p)),
			MaxSessionDuration: aws.Int32(3600), Tags: tags, Description: aws.String("Dormeto install: what CodeBuild runs Terraform with")})
		if err != nil {
			return "", fmt.Errorf("create role %s: %w", p.Role, err)
		}
		if created.Role != nil {
			arn = aws.ToString(created.Role.Arn)
		}
	default:
		return "", fmt.Errorf("check role %s: %w", p.Role, err)
	}
	if _, err := c.IAM.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: &p.Role, PolicyArn: aws.String(adminPolicyARN)}); err != nil {
		return "", fmt.Errorf("attach AdministratorAccess to role %s: %w", p.Role, err)
	}
	return arn, nil
}

func ensureLogGroup(ctx context.Context, c Clients, p Project) error {
	out, err := c.Logs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: &p.LogGroup})
	if err != nil {
		return fmt.Errorf("check log group %s: %w", p.LogGroup, err)
	}
	found := false
	for _, g := range out.LogGroups {
		found = found || aws.ToString(g.LogGroupName) == p.LogGroup
	}
	if !found {
		if _, err := c.Logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: &p.LogGroup, Tags: p.Tags()}); err != nil {
			return fmt.Errorf("create log group %s: %w", p.LogGroup, err)
		}
	}
	if _, err := c.Logs.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: &p.LogGroup, RetentionInDays: aws.Int32(logRetention)}); err != nil {
		return fmt.Errorf("log group %s retention: %w", p.LogGroup, err)
	}
	return nil
}

// uploadScripts puts both scripts under <env>/build/, where the buildspec reads them.
func uploadScripts(ctx context.Context, c Clients, p Project) error {
	for _, name := range []string{"manager.sh", "organization.sh"} {
		if _, err := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: &p.Bucket, Key: aws.String(p.Key("build", name)), Body: bytes.NewReader(build.Script(name))}); err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
	}
	return nil
}

// ensureProject creates or updates the CodeBuild project with roleARN, running script (a name under <env>/build/).
func ensureProject(ctx context.Context, c Clients, p Project, roleARN, script string) error {
	out, err := c.CodeBuild.BatchGetProjects(ctx, &codebuild.BatchGetProjectsInput{Names: []string{p.Name}})
	if err != nil {
		return fmt.Errorf("check project %s: %w", p.Name, err)
	}
	source := &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String(build.Buildspec())}
	artifacts := &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts}
	env := &cbtypes.ProjectEnvironment{
		Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String(buildImage), ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		EnvironmentVariables: []cbtypes.EnvironmentVariable{
			{Name: aws.String("BUCKET"), Value: &p.Bucket, Type: cbtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String("ENV"), Value: &p.Env, Type: cbtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String("SCRIPT"), Value: &script, Type: cbtypes.EnvironmentVariableTypePlaintext},
			{Name: aws.String("TF_VERSION"), Value: aws.String(build.TerraformVersion), Type: cbtypes.EnvironmentVariableTypePlaintext},
		},
	}
	logs := &cbtypes.LogsConfig{CloudWatchLogs: &cbtypes.CloudWatchLogsConfig{Status: cbtypes.LogsConfigStatusTypeEnabled, GroupName: &p.LogGroup}}
	var tags []cbtypes.Tag
	for _, k := range sortedKeys(p.Tags()) {
		tags = append(tags, cbtypes.Tag{Key: aws.String(k), Value: aws.String(p.Tags()[k])})
	}
	if len(out.Projects) == 0 {
		return untilUsable(func() error {
			_, err := c.CodeBuild.CreateProject(ctx, &codebuild.CreateProjectInput{Name: &p.Name, Source: source, Artifacts: artifacts, Environment: env,
				ServiceRole: &roleARN, TimeoutInMinutes: aws.Int32(60), ConcurrentBuildLimit: aws.Int32(1), LogsConfig: logs, Tags: tags})
			return err
		}, "create project "+p.Name)
	}
	return untilUsable(func() error {
		_, err := c.CodeBuild.UpdateProject(ctx, &codebuild.UpdateProjectInput{Name: &p.Name, Source: source, Artifacts: artifacts, Environment: env,
			ServiceRole: &roleARN, TimeoutInMinutes: aws.Int32(60), ConcurrentBuildLimit: aws.Int32(1), LogsConfig: logs, Tags: tags})
		return err
	}, "update project "+p.Name)
}

// untilUsable runs do, again every 5 s for up to 60 s while CodeBuild refuses the role that IAM has not yet spread.
func untilUsable(do func() error, what string) error {
	start := bootNow()
	for {
		err := do()
		var invalid *cbtypes.InvalidInputException
		if err == nil || !errors.As(err, &invalid) || !strings.Contains(invalid.ErrorMessage(), "not authorized to perform: sts:AssumeRole") {
			if err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			return nil
		}
		if bootNow().Sub(start) >= consistencyWait {
			return fmt.Errorf("%s: %w", what, err)
		}
		bootSleep(consistencyEvery)
	}
}

// Teardown deletes the install's project, its role (only the one this install created, under /dormeto/), its log group
// and, with bucket, the state bucket with every version in it. What is already gone is not an error.
func Teardown(ctx context.Context, c Clients, p Project, bucket bool) error {
	if _, err := c.CodeBuild.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: &p.Name}); err != nil && !isNotFound(err, "ResourceNotFoundException") {
		return fmt.Errorf("delete project %s: %w", p.Name, err)
	}
	if err := deleteBuildRole(ctx, c, p); err != nil {
		return err
	}
	if _, err := c.Logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: &p.LogGroup}); err != nil && !isNotFound(err, "ResourceNotFoundException") {
		return fmt.Errorf("delete log group %s: %w", p.LogGroup, err)
	}
	if bucket {
		return deleteBucket(ctx, c, p)
	}
	return nil
}

// deleteBuildRole deletes p.Role when it is under /dormeto/ and tagged managed-by=dormeto-cli (GetRole returns the
// tags): a role of that name elsewhere, or not made by the CLI, is not the install's.
func deleteBuildRole(ctx context.Context, c Clients, p Project) error {
	got, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: &p.Role})
	if isNotFound(err, "NoSuchEntity") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check role %s: %w", p.Role, err)
	}
	if got.Role == nil || aws.ToString(got.Role.Path) != rolePath || !slices.ContainsFunc(got.Role.Tags, func(t iamtypes.Tag) bool {
		return aws.ToString(t.Key) == "managed-by" && aws.ToString(t.Value) == "dormeto-cli"
	}) {
		return nil
	}
	if _, err := c.IAM.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: &p.Role, PolicyArn: aws.String(adminPolicyARN)}); err != nil && !isNotFound(err, "NoSuchEntity") {
		return fmt.Errorf("detach AdministratorAccess from role %s: %w", p.Role, err)
	}
	if _, err := c.IAM.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: &p.Role}); err != nil && !isNotFound(err, "NoSuchEntity") {
		return fmt.Errorf("delete role %s: %w", p.Role, err)
	}
	return nil
}

// deleteBucket removes every object version and delete marker, a page of the listing at a time (at most 1000, what
// DeleteObjects takes), then the bucket.
func deleteBucket(ctx context.Context, c Clients, p Project) error {
	in := &s3.ListObjectVersionsInput{Bucket: &p.Bucket}
	for {
		page, err := c.S3.ListObjectVersions(ctx, in)
		if isNotFound(err, "NoSuchBucket") {
			return nil
		}
		if err != nil {
			return fmt.Errorf("list bucket %s: %w", p.Bucket, err)
		}
		var ids []s3types.ObjectIdentifier
		for _, v := range page.Versions {
			ids = append(ids, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range page.DeleteMarkers {
			ids = append(ids, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		for len(ids) > 0 {
			n := min(len(ids), 1000)
			res, err := c.S3.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &p.Bucket, Delete: &s3types.Delete{Objects: ids[:n], Quiet: aws.Bool(true)}})
			if err != nil {
				return fmt.Errorf("empty bucket %s: %w", p.Bucket, err)
			}
			if len(res.Errors) > 0 {
				return fmt.Errorf("empty bucket %s: %s: %s", p.Bucket, aws.ToString(res.Errors[0].Key), aws.ToString(res.Errors[0].Message))
			}
			ids = ids[n:]
		}
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		in = &s3.ListObjectVersionsInput{Bucket: &p.Bucket, KeyMarker: page.NextKeyMarker, VersionIdMarker: page.NextVersionIdMarker}
	}
	if _, err := c.S3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &p.Bucket}); err != nil && !isNotFound(err, "NoSuchBucket") {
		return fmt.Errorf("delete bucket %s: %w", p.Bucket, err)
	}
	return nil
}
