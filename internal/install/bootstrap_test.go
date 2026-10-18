package install

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/install/build"
)

// bootWorld records the calls of Bootstrap and Teardown, in order, with the last input of each.

type bootWorld struct {
	calls []string
	in    map[string]any
	all   map[string][]any // every input of each call, in order (in keeps the last)

	bucket      error // HeadBucket's answer
	role        bool  // the role exists
	rolePath    string
	rolePathFor map[string]string // a path per role name, over rolePath
	untagged    bool              // the existing role lacks managed-by=dormeto-cli
	logGroup    bool
	project     bool
	cbErr       func(n int) error // the nth CreateProject/UpdateProject
	cbCalls     int
	versions    []*s3.ListObjectVersionsOutput // the pages
	deleted     [][]string                     // each DeleteObjects' keys
	noProject   bool                           // DeleteProject answers not found
	noRule      bool                           // RemoveTargets and DeleteRule answer not found
	targetErr   func(n int) error              // the nth PutTargets error
	targets     int
	failed      bool // PutTargets answers a failed entry
}

func (w *bootWorld) rec(name string, in any) {
	if w.in == nil {
		w.in = map[string]any{}
	}
	w.calls = append(w.calls, name)
	w.in[name] = in
	if w.all == nil {
		w.all = map[string][]any{}
	}
	w.all[name] = append(w.all[name], in)
}

func (w *bootWorld) clients() Clients {
	return Clients{S3: bootS3{w: w}, IAM: bootIAM{w: w}, Logs: bootLogs{w: w}, CodeBuild: bootCB{w: w}, EventBridge: bootEvents{w: w}}
}

type bootS3 struct {
	S3
	w *bootWorld
}

func (f bootS3) HeadBucket(_ context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	f.w.rec("s3.HeadBucket", in)
	return &s3.HeadBucketOutput{}, f.w.bucket
}
func (f bootS3) CreateBucket(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.w.rec("s3.CreateBucket", in)
	return &s3.CreateBucketOutput{}, nil
}
func (f bootS3) PutBucketVersioning(_ context.Context, in *s3.PutBucketVersioningInput, _ ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error) {
	f.w.rec("s3.PutBucketVersioning", in)
	return &s3.PutBucketVersioningOutput{}, nil
}
func (f bootS3) PutBucketEncryption(_ context.Context, in *s3.PutBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error) {
	f.w.rec("s3.PutBucketEncryption", in)
	return &s3.PutBucketEncryptionOutput{}, nil
}
func (f bootS3) PutPublicAccessBlock(_ context.Context, in *s3.PutPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error) {
	f.w.rec("s3.PutPublicAccessBlock", in)
	return &s3.PutPublicAccessBlockOutput{}, nil
}
func (f bootS3) PutBucketPolicy(_ context.Context, in *s3.PutBucketPolicyInput, _ ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error) {
	f.w.rec("s3.PutBucketPolicy", in)
	return &s3.PutBucketPolicyOutput{}, nil
}
func (f bootS3) PutBucketLifecycleConfiguration(_ context.Context, in *s3.PutBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	f.w.rec("s3.PutBucketLifecycleConfiguration", in)
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}
func (f bootS3) PutBucketTagging(_ context.Context, in *s3.PutBucketTaggingInput, _ ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error) {
	f.w.rec("s3.PutBucketTagging", in)
	return &s3.PutBucketTaggingOutput{}, nil
}
func (f bootS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.w.rec("s3.PutObject "+aws.ToString(in.Key), in)
	return &s3.PutObjectOutput{}, nil
}
func (f bootS3) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	f.w.rec("s3.ListObjectVersions", in)
	i := 0
	if in.KeyMarker != nil {
		i = 1
	}
	return f.w.versions[i], nil
}
func (f bootS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.w.rec("s3.DeleteObjects", in)
	var keys []string
	for _, o := range in.Delete.Objects {
		keys = append(keys, aws.ToString(o.Key)+"@"+aws.ToString(o.VersionId))
	}
	f.w.deleted = append(f.w.deleted, keys)
	return &s3.DeleteObjectsOutput{}, nil
}
func (f bootS3) DeleteBucket(_ context.Context, in *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	f.w.rec("s3.DeleteBucket", in)
	return &s3.DeleteBucketOutput{}, nil
}

func (f bootS3) PutBucketNotificationConfiguration(_ context.Context, in *s3.PutBucketNotificationConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketNotificationConfigurationOutput, error) {
	f.w.rec("s3.PutBucketNotificationConfiguration", in)
	return &s3.PutBucketNotificationConfigurationOutput{}, nil
}

type bootEvents struct {
	EventBridge
	w *bootWorld
}

func (f bootEvents) PutRule(_ context.Context, in *eventbridge.PutRuleInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutRuleOutput, error) {
	f.w.rec("events.PutRule", in)
	return &eventbridge.PutRuleOutput{}, nil
}
func (f bootEvents) PutTargets(_ context.Context, in *eventbridge.PutTargetsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutTargetsOutput, error) {
	f.w.rec("events.PutTargets", in)
	f.w.targets++
	if f.w.targetErr != nil {
		if err := f.w.targetErr(f.w.targets); err != nil {
			return nil, err
		}
	}
	if f.w.failed {
		return &eventbridge.PutTargetsOutput{FailedEntryCount: 1, FailedEntries: []ebtypes.PutTargetsResultEntry{
			{TargetId: aws.String("install-build"), ErrorCode: aws.String("ValidationException"), ErrorMessage: aws.String("bad role")}}}, nil
	}
	return &eventbridge.PutTargetsOutput{}, nil
}
func (f bootEvents) RemoveTargets(_ context.Context, in *eventbridge.RemoveTargetsInput, _ ...func(*eventbridge.Options)) (*eventbridge.RemoveTargetsOutput, error) {
	f.w.rec("events.RemoveTargets", in)
	if f.w.noRule {
		return nil, &ebtypes.ResourceNotFoundException{Message: aws.String("no rule")}
	}
	return &eventbridge.RemoveTargetsOutput{}, nil
}
func (f bootEvents) DeleteRule(_ context.Context, in *eventbridge.DeleteRuleInput, _ ...func(*eventbridge.Options)) (*eventbridge.DeleteRuleOutput, error) {
	f.w.rec("events.DeleteRule", in)
	if f.w.noRule {
		return nil, &ebtypes.ResourceNotFoundException{Message: aws.String("no rule")}
	}
	return &eventbridge.DeleteRuleOutput{}, nil
}

type bootIAM struct {
	IAM
	w *bootWorld
}

func (f bootIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	f.w.rec("iam.GetRole", in)
	if !f.w.role {
		return nil, chkNoEntity()
	}
	path := f.w.rolePath
	if p, ok := f.w.rolePathFor[aws.ToString(in.RoleName)]; ok {
		path = p
	}
	if path == "" {
		path = "/dormeto/"
	}
	r := &iamtypes.Role{RoleName: in.RoleName, Path: &path, Arn: aws.String("arn:aws:iam::123456789012:role" + path + aws.ToString(in.RoleName))}
	if !f.w.untagged {
		r.Tags = []iamtypes.Tag{{Key: aws.String("managed-by"), Value: aws.String("dormeto-cli")}}
	}
	return &iam.GetRoleOutput{Role: r}, nil
}
func (f bootIAM) CreateRole(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	f.w.rec("iam.CreateRole", in)
	return &iam.CreateRoleOutput{Role: &iamtypes.Role{Arn: aws.String("arn:aws:iam::123456789012:role" + aws.ToString(in.Path) + aws.ToString(in.RoleName))}}, nil
}
func (f bootIAM) UpdateAssumeRolePolicy(_ context.Context, in *iam.UpdateAssumeRolePolicyInput, _ ...func(*iam.Options)) (*iam.UpdateAssumeRolePolicyOutput, error) {
	f.w.rec("iam.UpdateAssumeRolePolicy", in)
	return &iam.UpdateAssumeRolePolicyOutput{}, nil
}
func (f bootIAM) AttachRolePolicy(_ context.Context, in *iam.AttachRolePolicyInput, _ ...func(*iam.Options)) (*iam.AttachRolePolicyOutput, error) {
	f.w.rec("iam.AttachRolePolicy", in)
	return &iam.AttachRolePolicyOutput{}, nil
}
func (f bootIAM) DetachRolePolicy(_ context.Context, in *iam.DetachRolePolicyInput, _ ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error) {
	f.w.rec("iam.DetachRolePolicy", in)
	return &iam.DetachRolePolicyOutput{}, nil
}
func (f bootIAM) PutRolePolicy(_ context.Context, in *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	f.w.rec("iam.PutRolePolicy", in)
	return &iam.PutRolePolicyOutput{}, nil
}
func (f bootIAM) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	f.w.rec("iam.DeleteRolePolicy", in)
	return &iam.DeleteRolePolicyOutput{}, nil
}
func (f bootIAM) DeleteRole(_ context.Context, in *iam.DeleteRoleInput, _ ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	f.w.rec("iam.DeleteRole", in)
	return &iam.DeleteRoleOutput{}, nil
}

type bootLogs struct {
	Logs
	w *bootWorld
}

func (f bootLogs) DescribeLogGroups(_ context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	f.w.rec("logs.DescribeLogGroups", in)
	out := &cloudwatchlogs.DescribeLogGroupsOutput{}
	if f.w.logGroup {
		out.LogGroups = []cwltypes.LogGroup{{LogGroupName: in.LogGroupNamePrefix}}
	}
	return out, nil
}
func (f bootLogs) CreateLogGroup(_ context.Context, in *cloudwatchlogs.CreateLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	f.w.rec("logs.CreateLogGroup", in)
	return &cloudwatchlogs.CreateLogGroupOutput{}, nil
}
func (f bootLogs) PutRetentionPolicy(_ context.Context, in *cloudwatchlogs.PutRetentionPolicyInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	f.w.rec("logs.PutRetentionPolicy", in)
	return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
}
func (f bootLogs) DeleteLogGroup(_ context.Context, in *cloudwatchlogs.DeleteLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	f.w.rec("logs.DeleteLogGroup", in)
	return &cloudwatchlogs.DeleteLogGroupOutput{}, nil
}

type bootCB struct {
	CodeBuild
	w *bootWorld
}

func (f bootCB) BatchGetProjects(_ context.Context, in *codebuild.BatchGetProjectsInput, _ ...func(*codebuild.Options)) (*codebuild.BatchGetProjectsOutput, error) {
	f.w.rec("codebuild.BatchGetProjects", in)
	out := &codebuild.BatchGetProjectsOutput{}
	if f.w.project {
		out.Projects = []cbtypes.Project{{Name: aws.String(in.Names[0])}}
	} else {
		out.ProjectsNotFound = in.Names
	}
	return out, nil
}
func (f bootCB) cbResult() error {
	f.w.cbCalls++
	if f.w.cbErr != nil {
		return f.w.cbErr(f.w.cbCalls)
	}
	return nil
}
func (f bootCB) CreateProject(_ context.Context, in *codebuild.CreateProjectInput, _ ...func(*codebuild.Options)) (*codebuild.CreateProjectOutput, error) {
	f.w.rec("codebuild.CreateProject", in)
	return &codebuild.CreateProjectOutput{}, f.cbResult()
}
func (f bootCB) UpdateProject(_ context.Context, in *codebuild.UpdateProjectInput, _ ...func(*codebuild.Options)) (*codebuild.UpdateProjectOutput, error) {
	f.w.rec("codebuild.UpdateProject", in)
	return &codebuild.UpdateProjectOutput{}, f.cbResult()
}
func (f bootCB) DeleteProject(_ context.Context, in *codebuild.DeleteProjectInput, _ ...func(*codebuild.Options)) (*codebuild.DeleteProjectOutput, error) {
	f.w.rec("codebuild.DeleteProject", in)
	if f.w.noProject {
		return nil, &cbtypes.ResourceNotFoundException{Message: aws.String("no project")}
	}
	return &codebuild.DeleteProjectOutput{}, nil
}

// bootClock replaces the retry's sleep and clock with a fake one that only moves when slept.
func bootClock(t *testing.T) (slept *[]time.Duration) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var sl []time.Duration
	oldSleep, oldNow := bootSleep, bootNow
	bootSleep = func(d time.Duration) { sl = append(sl, d); now = now.Add(d) }
	bootNow = func() time.Time { return now }
	t.Cleanup(func() { bootSleep, bootNow = oldSleep, oldNow })
	return &sl
}

var bootP = Names("123456789012", "eu-west-1", "prod")

func bootAssumeRoleRefused() error {
	return &cbtypes.InvalidInputException{Message: aws.String("CodeBuild is not authorized to perform: sts:AssumeRole on arn:aws:iam::123456789012:role/dormeto/dmt-prod-install")}
}

func TestBootstrapFreshAccount(t *testing.T) {
	bootClock(t)
	w := &bootWorld{bucket: &s3types.NotFound{}}
	if err := Bootstrap(context.Background(), w.clients(), bootP, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"s3.HeadBucket", "s3.CreateBucket", "s3.PutBucketVersioning", "s3.PutBucketEncryption", "s3.PutPublicAccessBlock",
		"s3.PutBucketPolicy", "s3.PutBucketLifecycleConfiguration", "s3.PutBucketTagging",
		"iam.GetRole", "iam.CreateRole", "iam.AttachRolePolicy",
		"logs.DescribeLogGroups", "logs.CreateLogGroup", "logs.PutRetentionPolicy",
		"s3.PutObject prod/build/manager.sh", "s3.PutObject prod/build/organization.sh",
		"codebuild.BatchGetProjects", "codebuild.CreateProject",
		"iam.GetRole", "iam.CreateRole", "iam.PutRolePolicy", "s3.PutBucketNotificationConfiguration", "events.PutRule", "events.PutTargets",
	}
	if !slices.Equal(w.calls, want) {
		t.Fatalf("calls:\n%s\nwant\n%s", strings.Join(w.calls, "\n"), strings.Join(want, "\n"))
	}
	if cb := w.in["s3.CreateBucket"].(*s3.CreateBucketInput); aws.ToString(cb.Bucket) != "dmt-install-123456789012-eu-west-1" ||
		cb.CreateBucketConfiguration == nil || cb.CreateBucketConfiguration.LocationConstraint != s3types.BucketLocationConstraintEuWest1 {
		t.Errorf("CreateBucket = %+v", cb)
	}
	checkBucketSettings(t, w)
	checkRole(t, w)
	checkEvents(t, w)

	lg := w.in["logs.CreateLogGroup"].(*cloudwatchlogs.CreateLogGroupInput)
	if aws.ToString(lg.LogGroupName) != "/aws/codebuild/dmt-prod-install" || lg.Tags["managed-by"] != "dormeto-cli" || lg.Tags["dormeto:env"] != "prod" || lg.Tags["project"] != "dormeto" {
		t.Errorf("CreateLogGroup = %+v", lg)
	}
	if r := w.in["logs.PutRetentionPolicy"].(*cloudwatchlogs.PutRetentionPolicyInput); aws.ToInt32(r.RetentionInDays) != 90 {
		t.Errorf("retention = %v", r.RetentionInDays)
	}
	if o := w.in["s3.PutObject prod/build/manager.sh"].(*s3.PutObjectInput); aws.ToString(o.Bucket) != bootP.Bucket {
		t.Errorf("PutObject bucket = %v", o.Bucket)
	}

	cp := w.in["codebuild.CreateProject"].(*codebuild.CreateProjectInput)
	checkProject(t, cp.Name, cp.Source, cp.Artifacts, cp.Environment, cp.ServiceRole, cp.TimeoutInMinutes, cp.ConcurrentBuildLimit, cp.LogsConfig, cp.Tags,
		"arn:aws:iam::123456789012:role/dormeto/dmt-prod-install")
}

func checkBucketSettings(t *testing.T, w *bootWorld) {
	t.Helper()
	if v := w.in["s3.PutBucketVersioning"].(*s3.PutBucketVersioningInput); v.VersioningConfiguration.Status != s3types.BucketVersioningStatusEnabled {
		t.Errorf("versioning = %+v", v)
	}
	e := w.in["s3.PutBucketEncryption"].(*s3.PutBucketEncryptionInput).ServerSideEncryptionConfiguration.Rules
	if len(e) != 1 || e[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm != s3types.ServerSideEncryptionAes256 {
		t.Errorf("encryption = %+v", e)
	}
	b := w.in["s3.PutPublicAccessBlock"].(*s3.PutPublicAccessBlockInput).PublicAccessBlockConfiguration
	if !aws.ToBool(b.BlockPublicAcls) || !aws.ToBool(b.BlockPublicPolicy) || !aws.ToBool(b.IgnorePublicAcls) || !aws.ToBool(b.RestrictPublicBuckets) {
		t.Errorf("public access = %+v", b)
	}
	var pol struct {
		Statement []struct {
			Effect    string
			Principal string
			Action    string
			Resource  []string
			Condition map[string]map[string]string
		}
	}
	if err := json.Unmarshal([]byte(aws.ToString(w.in["s3.PutBucketPolicy"].(*s3.PutBucketPolicyInput).Policy)), &pol); err != nil {
		t.Fatal(err)
	}
	if len(pol.Statement) != 1 || pol.Statement[0].Effect != "Deny" || pol.Statement[0].Principal != "*" || pol.Statement[0].Action != "s3:*" ||
		pol.Statement[0].Condition["Bool"]["aws:SecureTransport"] != "false" ||
		!slices.Equal(pol.Statement[0].Resource, []string{"arn:aws:s3:::dmt-install-123456789012-eu-west-1", "arn:aws:s3:::dmt-install-123456789012-eu-west-1/*"}) {
		t.Errorf("policy = %+v", pol)
	}
	rules := w.in["s3.PutBucketLifecycleConfiguration"].(*s3.PutBucketLifecycleConfigurationInput).LifecycleConfiguration.Rules
	if len(rules) != 1 || aws.ToString(rules[0].ID) != "noncurrent-90-days" || rules[0].Status != s3types.ExpirationStatusEnabled ||
		aws.ToInt32(rules[0].NoncurrentVersionExpiration.NoncurrentDays) != 90 || rules[0].Filter == nil {
		t.Errorf("lifecycle = %+v", rules)
	}
	tags := w.in["s3.PutBucketTagging"].(*s3.PutBucketTaggingInput).Tagging.TagSet
	got := map[string]string{}
	for _, tg := range tags {
		got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	if len(got) != 3 || got["project"] != "dormeto" || got["dormeto:env"] != "prod" || got["managed-by"] != "dormeto-cli" {
		t.Errorf("bucket tags = %v", got)
	}
}

const wantTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"codebuild.amazonaws.com"},"Action":"sts:AssumeRole","Condition":{"StringEquals":{"aws:SourceAccount":"123456789012"},"ArnLike":{"aws:SourceArn":"arn:aws:codebuild:eu-west-1:123456789012:project/dmt-prod-install"}}}]}`

// first is the first input of a call (the build role's: it is ensured before the events role).
func first[T any](w *bootWorld, call string) (T, bool) {
	var zero T
	if len(w.all[call]) == 0 {
		return zero, false
	}
	v, ok := w.all[call][0].(T)
	return v, ok
}

func checkRole(t *testing.T, w *bootWorld) {
	t.Helper()
	if c, ok := first[*iam.CreateRoleInput](w, "iam.CreateRole"); ok {
		if aws.ToString(c.RoleName) != "dmt-prod-install" || aws.ToString(c.Path) != "/dormeto/" || aws.ToString(c.AssumeRolePolicyDocument) != wantTrust || aws.ToInt32(c.MaxSessionDuration) != 3600 {
			t.Errorf("CreateRole = %+v", c)
		}
		got := map[string]string{}
		for _, tg := range c.Tags {
			got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		if len(got) != 3 || got["managed-by"] != "dormeto-cli" {
			t.Errorf("role tags = %v", got)
		}
	}
	if u, ok := first[*iam.UpdateAssumeRolePolicyInput](w, "iam.UpdateAssumeRolePolicy"); ok && aws.ToString(u.PolicyDocument) != wantTrust {
		t.Errorf("UpdateAssumeRolePolicy = %+v", u)
	}
	a := w.in["iam.AttachRolePolicy"].(*iam.AttachRolePolicyInput)
	if aws.ToString(a.RoleName) != "dmt-prod-install" || aws.ToString(a.PolicyArn) != "arn:aws:iam::aws:policy/AdministratorAccess" {
		t.Errorf("AttachRolePolicy = %+v", a)
	}
}

func checkProject(t *testing.T, name *string, src *cbtypes.ProjectSource, art *cbtypes.ProjectArtifacts, env *cbtypes.ProjectEnvironment, role *string,
	timeout, limit *int32, logs *cbtypes.LogsConfig, tags []cbtypes.Tag, wantRole string) {
	t.Helper()
	if aws.ToString(name) != "dmt-prod-install" {
		t.Errorf("project name = %v", aws.ToString(name))
	}
	if src.Type != cbtypes.SourceTypeNoSource || aws.ToString(src.Buildspec) != build.Buildspec() {
		t.Errorf("source = %+v", src)
	}
	if art.Type != cbtypes.ArtifactsTypeNoArtifacts {
		t.Errorf("artifacts = %+v", art)
	}
	if env.Type != cbtypes.EnvironmentTypeLinuxContainer || aws.ToString(env.Image) != "aws/codebuild/amazonlinux-x86_64-standard:6.0" || env.ComputeType != cbtypes.ComputeTypeBuildGeneral1Small {
		t.Errorf("environment = %+v", env)
	}
	vars := map[string]string{}
	for _, v := range env.EnvironmentVariables {
		vars[aws.ToString(v.Name)] = aws.ToString(v.Value)
		if v.Type != cbtypes.EnvironmentVariableTypePlaintext {
			t.Errorf("variable %s type %s", aws.ToString(v.Name), v.Type)
		}
	}
	wantVars := map[string]string{"BUCKET": "dmt-install-123456789012-eu-west-1", "ENV": "prod", "SCRIPT": "manager.sh", "TF_VERSION": build.TerraformVersion}
	if len(vars) != 4 || vars["BUCKET"] != wantVars["BUCKET"] || vars["ENV"] != "prod" || vars["SCRIPT"] != "manager.sh" || vars["TF_VERSION"] != "1.16.5" {
		t.Errorf("variables = %v", vars)
	}
	if aws.ToString(role) != wantRole {
		t.Errorf("service role = %v, want %s", aws.ToString(role), wantRole)
	}
	if aws.ToInt32(timeout) != 60 || aws.ToInt32(limit) != 1 {
		t.Errorf("timeout %v limit %v", timeout, limit)
	}
	if logs == nil || logs.CloudWatchLogs == nil || logs.CloudWatchLogs.Status != cbtypes.LogsConfigStatusTypeEnabled || aws.ToString(logs.CloudWatchLogs.GroupName) != "/aws/codebuild/dmt-prod-install" {
		t.Errorf("logs = %+v", logs)
	}
	got := map[string]string{}
	for _, tg := range tags {
		got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	if len(got) != 3 || got["project"] != "dormeto" || got["dormeto:env"] != "prod" || got["managed-by"] != "dormeto-cli" {
		t.Errorf("project tags = %v", got)
	}
}

func TestBootstrapEverythingPresent(t *testing.T) {
	bootClock(t)
	w := &bootWorld{role: true, logGroup: true, project: true}
	if err := Bootstrap(context.Background(), w.clients(), bootP, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.calls {
		if strings.Contains(c, "Create") {
			t.Errorf("created something that exists: %s", c)
		}
	}
	for _, c := range []string{"s3.PutBucketVersioning", "s3.PutBucketEncryption", "s3.PutPublicAccessBlock", "s3.PutBucketPolicy", "s3.PutBucketLifecycleConfiguration", "s3.PutBucketTagging",
		"iam.UpdateAssumeRolePolicy", "iam.AttachRolePolicy", "logs.PutRetentionPolicy", "codebuild.UpdateProject", "s3.PutObject prod/build/manager.sh"} {
		if !slices.Contains(w.calls, c) {
			t.Errorf("%s not called: %v", c, w.calls)
		}
	}
	checkBucketSettings(t, w)
	checkRole(t, w)
	up := w.in["codebuild.UpdateProject"].(*codebuild.UpdateProjectInput)
	checkProject(t, up.Name, up.Source, up.Artifacts, up.Environment, up.ServiceRole, up.TimeoutInMinutes, up.ConcurrentBuildLimit, up.LogsConfig, up.Tags,
		"arn:aws:iam::123456789012:role/dormeto/dmt-prod-install")
}

func TestBootstrapBuildRole(t *testing.T) {
	bootClock(t)
	w := &bootWorld{}
	const arn = "arn:aws:iam::123456789012:role/team/our-build-role"
	if err := Bootstrap(context.Background(), w.clients(), bootP, arn); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.calls {
		if strings.HasPrefix(c, "iam.") {
			t.Errorf("IAM touched with --build-role: %s", c)
		}
	}
	if cp := w.in["codebuild.CreateProject"].(*codebuild.CreateProjectInput); aws.ToString(cp.ServiceRole) != arn {
		t.Errorf("service role = %v", cp.ServiceRole)
	}
	for _, c := range w.calls {
		if strings.HasPrefix(c, "events.") || c == "s3.PutBucketNotificationConfiguration" {
			t.Errorf("the update was set up with --build-role: %s", c)
		}
	}
}

func TestBootstrapBucketOfAnotherAccount(t *testing.T) {
	bootClock(t)
	w := &bootWorld{bucket: &smithy.GenericAPIError{Code: "Forbidden", Message: "403"}}
	err := Bootstrap(context.Background(), w.clients(), bootP, "")
	if err == nil || err.Error() != "bucket dmt-install-123456789012-eu-west-1 exists in another account" {
		t.Fatalf("err = %v", err)
	}
	if len(w.calls) != 1 {
		t.Errorf("went on: %v", w.calls)
	}
}

func TestBootstrapBucketInUsEast1HasNoLocationConstraint(t *testing.T) {
	bootClock(t)
	w := &bootWorld{bucket: &s3types.NotFound{}}
	if err := ensureBucket(context.Background(), w.clients(), Names("123456789012", "us-east-1", "prod")); err != nil {
		t.Fatal(err)
	}
	if cb := w.in["s3.CreateBucket"].(*s3.CreateBucketInput); cb.CreateBucketConfiguration != nil {
		t.Errorf("CreateBucketConfiguration = %+v", cb.CreateBucketConfiguration)
	}
}

func TestBootstrapWaitsForTheRoleToBeUsable(t *testing.T) {
	slept := bootClock(t)
	w := &bootWorld{cbErr: func(n int) error {
		if n <= 2 {
			return bootAssumeRoleRefused()
		}
		return nil
	}}
	if err := Bootstrap(context.Background(), w.clients(), bootP, ""); err != nil {
		t.Fatal(err)
	}
	if w.cbCalls != 3 || len(*slept) != 2 || (*slept)[0] != 5*time.Second {
		t.Errorf("calls %d, sleeps %v", w.cbCalls, *slept)
	}
}

func TestBootstrapGivesUpAfterAMinute(t *testing.T) {
	slept := bootClock(t)
	w := &bootWorld{cbErr: func(int) error { return bootAssumeRoleRefused() }}
	err := Bootstrap(context.Background(), w.clients(), bootP, "")
	var ie *cbtypes.InvalidInputException
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v", err)
	}
	var total time.Duration
	for _, d := range *slept {
		total += d
	}
	if total != 60*time.Second {
		t.Errorf("waited %v, want 60s", total)
	}
}

func TestBootstrapOtherProjectErrorsAreNotRetried(t *testing.T) {
	slept := bootClock(t)
	w := &bootWorld{cbErr: func(int) error {
		return &cbtypes.InvalidInputException{Message: aws.String("Project name is invalid")}
	}}
	if err := Bootstrap(context.Background(), w.clients(), bootP, ""); err == nil {
		t.Fatal("no error")
	}
	if w.cbCalls != 1 || len(*slept) != 0 {
		t.Errorf("calls %d, sleeps %v", w.cbCalls, *slept)
	}
}

func TestTeardownKeepingTheBucket(t *testing.T) {
	w := &bootWorld{role: true}
	if err := Teardown(context.Background(), w.clients(), bootP, "", false); err != nil {
		t.Fatal(err)
	}
	want := []string{"events.RemoveTargets", "events.DeleteRule", "iam.GetRole", "iam.DeleteRolePolicy", "iam.DeleteRole",
		"codebuild.DeleteProject", "iam.GetRole", "iam.DetachRolePolicy", "iam.DeleteRole", "logs.DeleteLogGroup"}
	if !slices.Equal(w.calls, want) {
		t.Errorf("calls %v, want %v", w.calls, want)
	}
	if d := w.in["iam.DetachRolePolicy"].(*iam.DetachRolePolicyInput); aws.ToString(d.PolicyArn) != "arn:aws:iam::aws:policy/AdministratorAccess" {
		t.Errorf("detached %v", d.PolicyArn)
	}
	rt := w.in["events.RemoveTargets"].(*eventbridge.RemoveTargetsInput)
	if aws.ToString(rt.Rule) != "dmt-prod-install-requests" || !slices.Equal(rt.Ids, []string{"install-build"}) {
		t.Errorf("RemoveTargets = %+v", rt)
	}
	if dr := w.in["events.DeleteRule"].(*eventbridge.DeleteRuleInput); aws.ToString(dr.Name) != "dmt-prod-install-requests" {
		t.Errorf("DeleteRule = %+v", dr)
	}
	if dp := w.in["iam.DeleteRolePolicy"].(*iam.DeleteRolePolicyInput); aws.ToString(dp.RoleName) != "dmt-prod-install-events" || aws.ToString(dp.PolicyName) != "StartInstallBuild" {
		t.Errorf("DeleteRolePolicy = %+v", dp)
	}
	if dr := w.all["iam.DeleteRole"][0].(*iam.DeleteRoleInput); aws.ToString(dr.RoleName) != "dmt-prod-install-events" {
		t.Errorf("first DeleteRole = %+v", dr)
	}
}

func TestTeardownToleratesAMissingRule(t *testing.T) {
	w := &bootWorld{noRule: true}
	if err := Teardown(context.Background(), w.clients(), bootP, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestTeardownWithABuildRoleMakesNoEventsCall(t *testing.T) {
	w := &bootWorld{role: true}
	if err := Teardown(context.Background(), w.clients(), bootP, "arn:aws:iam::123456789012:role/team/our-build-role", false); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.calls {
		if strings.HasPrefix(c, "events.") {
			t.Errorf("events call with --build-role: %s", c)
		}
	}
	if len(w.all["iam.GetRole"]) != 1 {
		t.Errorf("the events role was looked up: %v", w.calls)
	}
}

func TestTeardownKeepsAnEventsRoleOutsideDormetoOrUntagged(t *testing.T) {
	for name, w := range map[string]*bootWorld{
		"path":     {role: true, rolePath: "/team/"},
		"untagged": {role: true, untagged: true},
	} {
		if err := Teardown(context.Background(), w.clients(), bootP, "", false); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if slices.Contains(w.calls, "iam.DeleteRole") || slices.Contains(w.calls, "iam.DeleteRolePolicy") {
			t.Errorf("%s: deleted a role that is not the install's: %v", name, w.calls)
		}
		if !slices.Contains(w.calls, "events.DeleteRule") {
			t.Errorf("%s: the rule stays: %v", name, w.calls)
		}
	}
}

func TestTeardownToleratesWhatIsGone(t *testing.T) {
	w := &bootWorld{noProject: true} // no role either
	if err := Teardown(context.Background(), w.clients(), bootP, "", false); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.calls, "iam.DeleteRole") {
		t.Errorf("deleted a role that is gone: %v", w.calls)
	}
}

func TestTeardownNeverDeletesARoleOutsideDormetoPath(t *testing.T) {
	w := &bootWorld{role: true, rolePath: "/team/"}
	if err := Teardown(context.Background(), w.clients(), bootP, "", false); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.calls, "iam.DeleteRole") || slices.Contains(w.calls, "iam.DetachRolePolicy") {
		t.Errorf("touched a role of another path: %v", w.calls)
	}
}

func TestTeardownEmptiesTheBucket(t *testing.T) {
	w := &bootWorld{versions: []*s3.ListObjectVersionsOutput{
		{
			IsTruncated: aws.Bool(true), NextKeyMarker: aws.String("b"), NextVersionIdMarker: aws.String("v2"),
			Versions:      []s3types.ObjectVersion{{Key: aws.String("a"), VersionId: aws.String("v1")}, {Key: aws.String("b"), VersionId: aws.String("v2")}},
			DeleteMarkers: []s3types.DeleteMarkerEntry{{Key: aws.String("a"), VersionId: aws.String("d1")}},
		},
		{Versions: []s3types.ObjectVersion{{Key: aws.String("c"), VersionId: aws.String("v3")}}},
	}}
	if err := Teardown(context.Background(), w.clients(), bootP, "", true); err != nil {
		t.Fatal(err)
	}
	if len(w.deleted) != 2 || !slices.Equal(w.deleted[0], []string{"a@v1", "b@v2", "a@d1"}) || !slices.Equal(w.deleted[1], []string{"c@v3"}) {
		t.Errorf("deleted %v", w.deleted)
	}
	if n := len(w.calls); w.calls[n-1] != "s3.DeleteBucket" {
		t.Errorf("the bucket must go last: %v", w.calls)
	}
	if l := w.in["s3.ListObjectVersions"].(*s3.ListObjectVersionsInput); aws.ToString(l.KeyMarker) != "b" || aws.ToString(l.VersionIdMarker) != "v2" {
		t.Errorf("second page markers = %v %v", l.KeyMarker, l.VersionIdMarker)
	}
	order := []string{"events.DeleteRule", "codebuild.DeleteProject", "logs.DeleteLogGroup", "s3.ListObjectVersions"}
	last := -1
	for _, c := range order {
		i := slices.Index(w.calls, c)
		if i < last {
			t.Errorf("order: %v", w.calls)
		}
		last = i
	}
}

func TestTeardownDeletesByThousand(t *testing.T) {
	var vs []s3types.ObjectVersion
	for i := 0; i < 2500; i++ {
		vs = append(vs, s3types.ObjectVersion{Key: aws.String("k"), VersionId: aws.String(string(rune('a' + i%26)))})
	}
	w := &bootWorld{versions: []*s3.ListObjectVersionsOutput{{Versions: vs}}}
	if err := Teardown(context.Background(), w.clients(), bootP, "", true); err != nil {
		t.Fatal(err)
	}
	if len(w.deleted) != 3 || len(w.deleted[0]) != 1000 || len(w.deleted[2]) != 500 {
		var n []int
		for _, d := range w.deleted {
			n = append(n, len(d))
		}
		t.Errorf("batches %v", n)
	}
}

func TestBootstrapRefusesARoleOutsideDormetoPath(t *testing.T) {
	bootClock(t)
	w := &bootWorld{role: true, rolePath: "/team/"}
	err := Bootstrap(context.Background(), w.clients(), bootP, "")
	if err == nil || err.Error() != "role dmt-prod-install exists outside /dormeto/: not this install's" {
		t.Fatalf("err = %v", err)
	}
	for _, c := range []string{"iam.UpdateAssumeRolePolicy", "iam.AttachRolePolicy", "iam.CreateRole", "codebuild.CreateProject"} {
		if slices.Contains(w.calls, c) {
			t.Errorf("%s called", c)
		}
	}
}

func TestBootstrapUsesTheARNTheAPIReturns(t *testing.T) {
	bootClock(t)
	w := &bootWorld{role: true}
	if err := Bootstrap(context.Background(), w.clients(), bootP, ""); err != nil {
		t.Fatal(err)
	}
	if cp := w.in["codebuild.CreateProject"].(*codebuild.CreateProjectInput); aws.ToString(cp.ServiceRole) != "arn:aws:iam::123456789012:role/dormeto/dmt-prod-install" {
		t.Errorf("service role = %v", cp.ServiceRole)
	}
}

func TestTeardownKeepsARoleNotTaggedAsTheCLIs(t *testing.T) {
	w := &bootWorld{role: true, untagged: true}
	if err := Teardown(context.Background(), w.clients(), bootP, "", false); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.calls, "iam.DeleteRole") || slices.Contains(w.calls, "iam.DetachRolePolicy") {
		t.Errorf("deleted a role the CLI did not tag: %v", w.calls)
	}
}

func TestBootstrapRefusesUntaggedRole(t *testing.T) {
	w := &bootWorld{role: true, untagged: true}
	err := Bootstrap(context.Background(), w.clients(), bootP, "")
	if err == nil || !strings.Contains(err.Error(), "role dmt-prod-install exists without the managed-by=dormeto-cli tag: not this install's") {
		t.Errorf("err = %v", err)
	}
	if slices.Contains(w.calls, "iam.UpdateAssumeRolePolicy") || slices.Contains(w.calls, "iam.AttachRolePolicy") {
		t.Errorf("calls = %v", w.calls)
	}
}

const (
	wantEventsTrust  = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sts:AssumeRole","Condition":{"StringEquals":{"aws:SourceAccount":"123456789012"},"ArnLike":{"aws:SourceArn":"arn:aws:events:eu-west-1:123456789012:rule/dmt-prod-install-requests"}}}]}`
	wantEventsPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"codebuild:StartBuild","Resource":"arn:aws:codebuild:eu-west-1:123456789012:project/dmt-prod-install"}]}`
	wantPattern      = `{"source":["aws.s3"],"detail-type":["Object Created"],"detail":{"bucket":{"name":["dmt-install-123456789012-eu-west-1"]},"object":{"key":[{"wildcard":"prod/requests/*.json"}]}}}`
)

// sameJSON compares two JSON documents by value.
func sameJSON(t *testing.T, what, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Errorf("%s is not JSON: %v\n%s", what, err, got)
		return
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %s\nwant %s", what, got, want)
	}
}

func checkEvents(t *testing.T, w *bootWorld) {
	t.Helper()
	cr := w.all["iam.CreateRole"][1].(*iam.CreateRoleInput)
	if aws.ToString(cr.RoleName) != "dmt-prod-install-events" || aws.ToString(cr.Path) != "/dormeto/" || aws.ToString(cr.AssumeRolePolicyDocument) != wantEventsTrust {
		t.Errorf("events CreateRole = %+v", cr)
	}
	tags := map[string]string{}
	for _, tg := range cr.Tags {
		tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	if len(tags) != 3 || tags["managed-by"] != "dormeto-cli" || tags["dormeto:env"] != "prod" || tags["project"] != "dormeto" {
		t.Errorf("events role tags = %v", tags)
	}
	pr := w.in["iam.PutRolePolicy"].(*iam.PutRolePolicyInput)
	if aws.ToString(pr.RoleName) != "dmt-prod-install-events" || aws.ToString(pr.PolicyName) != "StartInstallBuild" || aws.ToString(pr.PolicyDocument) != wantEventsPolicy {
		t.Errorf("PutRolePolicy = %+v", pr)
	}
	nc := w.in["s3.PutBucketNotificationConfiguration"].(*s3.PutBucketNotificationConfigurationInput)
	if aws.ToString(nc.Bucket) != bootP.Bucket || nc.NotificationConfiguration == nil || nc.NotificationConfiguration.EventBridgeConfiguration == nil {
		t.Errorf("PutBucketNotificationConfiguration = %+v", nc)
	}

	rule := w.in["events.PutRule"].(*eventbridge.PutRuleInput)
	if aws.ToString(rule.Name) != "dmt-prod-install-requests" || rule.State != ebtypes.RuleStateEnabled {
		t.Errorf("PutRule = %+v", rule)
	}
	sameJSON(t, "the rule's pattern", aws.ToString(rule.EventPattern), wantPattern)
	rt := map[string]string{}
	for _, tg := range rule.Tags {
		rt[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	if len(rt) != 3 || rt["managed-by"] != "dormeto-cli" || rt["dormeto:env"] != "prod" || rt["project"] != "dormeto" {
		t.Errorf("rule tags = %v", rt)
	}

	pt := w.in["events.PutTargets"].(*eventbridge.PutTargetsInput)
	if aws.ToString(pt.Rule) != "dmt-prod-install-requests" || len(pt.Targets) != 1 {
		t.Fatalf("PutTargets = %+v", pt)
	}
	tg := pt.Targets[0]
	if aws.ToString(tg.Id) != "install-build" || aws.ToString(tg.Arn) != "arn:aws:codebuild:eu-west-1:123456789012:project/dmt-prod-install" ||
		aws.ToString(tg.RoleArn) != "arn:aws:iam::123456789012:role/dormeto/dmt-prod-install-events" {
		t.Errorf("target = %+v", tg)
	}
	if tg.RetryPolicy == nil || aws.ToInt32(tg.RetryPolicy.MaximumRetryAttempts) != 0 || aws.ToInt32(tg.RetryPolicy.MaximumEventAgeInSeconds) != 60 {
		t.Errorf("retry policy = %+v", tg.RetryPolicy)
	}
	it := tg.InputTransformer
	if it == nil || !maps.Equal(it.InputPathsMap, map[string]string{"key": "$.detail.object.key"}) {
		t.Fatalf("input transformer = %+v", it)
	}
	// EventBridge fills <key> from InputPathsMap; the result must be the StartBuild overrides, as JSON.
	filled := strings.ReplaceAll(aws.ToString(it.InputTemplate), "<key>", "prod/requests/x.json")
	sameJSON(t, "the input", filled, `{"environmentVariablesOverride":[{"name":"ACTION","value":"request","type":"PLAINTEXT"},{"name":"REQUEST_KEY","value":"prod/requests/x.json","type":"PLAINTEXT"}]}`)
	if aws.ToString(it.InputTemplate) != `{"environmentVariablesOverride":[{"name":"ACTION","value":"request","type":"PLAINTEXT"},{"name":"REQUEST_KEY","value":"<key>","type":"PLAINTEXT"}]}` {
		t.Errorf("template = %s", aws.ToString(it.InputTemplate))
	}
}

func TestBootstrapFailedTargetIsAnError(t *testing.T) {
	bootClock(t)
	w := &bootWorld{failed: true}
	err := Bootstrap(context.Background(), w.clients(), bootP, "")
	if err == nil || !strings.Contains(err.Error(), "ValidationException") || !strings.Contains(err.Error(), "bad role") {
		t.Fatalf("err = %v", err)
	}
}

func TestBootstrapEventsRoleOutsideDormetoIsRefused(t *testing.T) {
	bootClock(t)
	// the build role is fine, the events role of that name is another's
	w := &bootWorld{role: true}
	w.rolePathFor = map[string]string{"dmt-prod-install-events": "/team/"}
	err := Bootstrap(context.Background(), w.clients(), bootP, "")
	if err == nil || err.Error() != "role dmt-prod-install-events exists outside /dormeto/: not this install's" {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(w.calls, "events.PutRule") || slices.Contains(w.calls, "s3.PutBucketNotificationConfiguration") {
		t.Errorf("went on: %v", w.calls)
	}
}

func TestBootstrapWaitsForTheEventsRoleToBeUsable(t *testing.T) {
	slept := bootClock(t)
	w := &bootWorld{targetErr: func(n int) error {
		if n == 1 {
			return &smithy.GenericAPIError{Code: "ValidationException", Message: "Provided role arn:aws:iam::123456789012:role/dormeto/dmt-prod-install-events cannot be assumed by principal 'events.amazonaws.com'."}
		}
		return nil
	}}
	if err := Bootstrap(context.Background(), w.clients(), bootP, ""); err != nil {
		t.Fatal(err)
	}
	if w.targets != 2 || len(*slept) != 1 || (*slept)[0] != 5*time.Second {
		t.Errorf("PutTargets %d, sleeps %v", w.targets, *slept)
	}
}

func TestBootstrapOtherTargetErrorsAreNotRetried(t *testing.T) {
	slept := bootClock(t)
	w := &bootWorld{targetErr: func(int) error {
		return &smithy.GenericAPIError{Code: "ValidationException", Message: "Target Arn is invalid"}
	}}
	if err := Bootstrap(context.Background(), w.clients(), bootP, ""); err == nil {
		t.Fatal("no error")
	}
	if w.targets != 1 || len(*slept) != 0 {
		t.Errorf("PutTargets %d, sleeps %v", w.targets, *slept)
	}
}
