package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Applied is <env>/applied.json, written by the apply build: what the last apply installed.
type Applied struct {
	Version string `json:"version"`
	Build   string `json:"build"`
}

// Outputs are the manager's Terraform outputs the install shows, from <env>/outputs.json (terraform output -json).
type Outputs struct{ DashboardURL, UserPoolID, Version string }

var envSettingsRe = regexp.MustCompile(`^([^/]+)/install\.json$`)

// getObject reads one object of the install's bucket; nil when the object or the bucket is absent.
func getObject(ctx context.Context, c Clients, p Project, key string) ([]byte, error) {
	out, err := c.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: &p.Bucket, Key: &key})
	if isNotFound(err, "NoSuchKey", "NotFound", "NoSuchBucket") {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read s3://%s/%s: %w", p.Bucket, key, err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read s3://%s/%s: %w", p.Bucket, key, err)
	}
	return b, nil
}

func putObject(ctx context.Context, c Clients, p Project, key string, body []byte) error {
	if _, err := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: &p.Bucket, Key: &key, Body: bytes.NewReader(body)}); err != nil {
		return fmt.Errorf("write s3://%s/%s: %w", p.Bucket, key, err)
	}
	return nil
}

// ReadSettings reads <env>/install.json; nil when it is absent or the bucket is.
func ReadSettings(ctx context.Context, c Clients, p Project) (*Settings, error) {
	b, err := getObject(ctx, c, p, p.Key("install.json"))
	if err != nil || b == nil {
		return nil, err
	}
	s, err := ParseSettings(b)
	if err != nil {
		return nil, fmt.Errorf("s3://%s/%s: %w", p.Bucket, p.Key("install.json"), err)
	}
	return &s, nil
}

// WriteSettings writes <env>/install.json.
func WriteSettings(ctx context.Context, c Clients, p Project, s Settings) error {
	b, err := s.JSON()
	if err != nil {
		return err
	}
	return putObject(ctx, c, p, p.Key("install.json"), b)
}

// ReadApplied reads <env>/applied.json; nil when absent.
func ReadApplied(ctx context.Context, c Clients, p Project) (*Applied, error) {
	b, err := getObject(ctx, c, p, p.Key("applied.json"))
	if err != nil || b == nil {
		return nil, err
	}
	var a Applied
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("s3://%s/%s: %w", p.Bucket, p.Key("applied.json"), err)
	}
	return &a, nil
}

// StateHasResources tells whether the manager's Terraform state, <env>/manager.tfstate, holds a resource: false when
// it is absent (or the bucket is) or its resources are none. It, not applied.json, says whether anything is applied: an
// apply that failed halfway leaves resources and no applied.json.
func StateHasResources(ctx context.Context, c Clients, p Project) (bool, error) {
	b, err := getObject(ctx, c, p, p.StateKey())
	if err != nil || b == nil {
		return false, err
	}
	var state struct {
		Resources []json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return false, fmt.Errorf("s3://%s/%s: %w", p.Bucket, p.StateKey(), err)
	}
	return len(state.Resources) > 0, nil
}

// ReadOutputs reads <env>/outputs.json, Terraform's `output -json` ({"dashboard_url":{"value":…},…}); nil when absent.
func ReadOutputs(ctx context.Context, c Clients, p Project) (*Outputs, error) {
	b, err := getObject(ctx, c, p, p.Key("outputs.json"))
	if err != nil || b == nil {
		return nil, err
	}
	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("s3://%s/%s: %w", p.Bucket, p.Key("outputs.json"), err)
	}
	str := func(name string) string {
		s, _ := raw[name].Value.(string)
		return s
	}
	return &Outputs{DashboardURL: str("dashboard_url"), UserPoolID: str("user_pool_id"), Version: str("version")}, nil
}

// OtherEnvs is the envs of the bucket with an <env>/install.json, but p.Env, sorted: one listing of the bucket.
func OtherEnvs(ctx context.Context, c Clients, p Project) ([]string, error) {
	var envs []string
	in := &s3.ListObjectsV2Input{Bucket: &p.Bucket}
	for {
		out, err := c.S3.ListObjectsV2(ctx, in)
		if isNotFound(err, "NoSuchBucket") {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list bucket %s: %w", p.Bucket, err)
		}
		for _, o := range out.Contents {
			if m := envSettingsRe.FindStringSubmatch(aws.ToString(o.Key)); m != nil && m[1] != p.Env {
				envs = append(envs, m[1])
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		in = &s3.ListObjectsV2Input{Bucket: &p.Bucket, ContinuationToken: out.NextContinuationToken}
	}
	slices.Sort(envs)
	return envs, nil
}

// ReadPlan reads <env>/plans/<build>/plan.json (the build id as manager.sh writes it); nil when absent.
func ReadPlan(ctx context.Context, c Clients, p Project, build string) ([]byte, error) {
	return getObject(ctx, c, p, p.Key("plans", build, "plan.json"))
}

// PutConfig writes the generated configuration, <env>/config/main.tf.json.
func PutConfig(ctx context.Context, c Clients, p Project, mainTF []byte) error {
	return putObject(ctx, c, p, p.Key("config", "main.tf.json"), mainTF)
}

// WriteCLI writes <env>/cli.json, the version of the CLI that installed: the dashboard reads it to know whether the
// install can be updated from there.
func WriteCLI(ctx context.Context, c Clients, p Project, version string) error {
	b, err := json.Marshal(map[string]string{"version": version})
	if err != nil {
		return err
	}
	if _, err := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: &p.Bucket, Key: aws.String(p.Key("cli.json")), Body: bytes.NewReader(b),
		ContentType: aws.String("application/json")}); err != nil {
		return fmt.Errorf("write s3://%s/%s: %w", p.Bucket, p.Key("cli.json"), err)
	}
	return nil
}

// UploadRelease puts a local release's files under <env>/release/ and returns that base, s3://<bucket>/<env>/release.
func UploadRelease(ctx context.Context, c Clients, p Project, r Release) (string, error) {
	for _, name := range r.Files() {
		b, err := os.ReadFile(filepath.Join(r.Local, name))
		if err != nil {
			return "", fmt.Errorf("local release %s: %w", r.Local, err)
		}
		if err := putObject(ctx, c, p, p.Key("release", name), b); err != nil {
			return "", err
		}
	}
	return "s3://" + p.Bucket + "/" + p.Key("release"), nil
}
