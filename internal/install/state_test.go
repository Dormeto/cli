package install

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// stS3 is a bucket: objects by key, or an absent bucket; its listing pages by two keys.
type stS3 struct {
	S3
	objects  map[string]string
	noBucket bool
	missing  string // the error code of an absent object; NoSuchKey by default
	desc     bool   // the listing is in descending key order
	puts     map[string]string
}

func stErr(code string) error { return chkAPI(code) }

func (f *stS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.noBucket {
		return nil, stErr("NoSuchBucket")
	}
	body, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		if f.missing != "" {
			return nil, stErr(f.missing)
		}
		return nil, stErr("NoSuchKey")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (f *stS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	if f.puts == nil {
		f.puts = map[string]string{}
	}
	f.puts[aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key)] = string(b)
	return &s3.PutObjectOutput{}, nil
}

func (f *stS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.noBucket {
		return nil, stErr("NoSuchBucket")
	}
	var keys []string
	for k := range f.objects {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if f.desc {
		slices.Reverse(keys)
	}
	start := 0
	if in.ContinuationToken != nil {
		for i, k := range keys {
			if k == *in.ContinuationToken {
				start = i
			}
		}
	}
	out := &s3.ListObjectsV2Output{}
	for i := start; i < len(keys); i++ {
		if len(out.Contents) == 2 {
			out.IsTruncated = aws.Bool(true)
			out.NextContinuationToken = aws.String(keys[i])
			break
		}
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(keys[i])})
	}
	return out, nil
}

func stClients(f *stS3) Clients { return Clients{S3: f} }

const stSettings = `{"env":"prod","region":"eu-west-1","version":"0.11.0","mode":"account","adminEmails":["a@b.co"]}`

func TestReadSettings(t *testing.T) {
	ctx := context.Background()
	got, err := ReadSettings(ctx, stClients(&stS3{objects: map[string]string{"prod/install.json": stSettings}}), bootP)
	if err != nil || got == nil || got.Env != "prod" || got.Version != "0.11.0" {
		t.Fatalf("present: %+v, %v", got, err)
	}
	if got, err := ReadSettings(ctx, stClients(&stS3{}), bootP); err != nil || got != nil {
		t.Fatalf("absent: %+v, %v", got, err)
	}
	if got, err := ReadSettings(ctx, stClients(&stS3{noBucket: true}), bootP); err != nil || got != nil {
		t.Fatalf("no bucket: %+v, %v", got, err)
	}
	if _, err := ReadSettings(ctx, stClients(&stS3{objects: map[string]string{"prod/install.json": "{"}}), bootP); err == nil {
		t.Fatal("garbage: no error")
	}
}

func TestOtherErrorsAreReturned(t *testing.T) {
	c := Clients{S3: stDenied{}}
	if _, err := ReadApplied(context.Background(), c, bootP); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v", err)
	}
}

type stDenied struct{ S3 }

func (stDenied) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, stErr("AccessDenied")
}

func TestWriteSettingsRoundTrips(t *testing.T) {
	f := &stS3{}
	s, _ := ParseSettings([]byte(stSettings))
	if err := WriteSettings(context.Background(), stClients(f), bootP, s); err != nil {
		t.Fatal(err)
	}
	body, ok := f.puts["dmt-install-123456789012-eu-west-1/prod/install.json"]
	if !ok {
		t.Fatalf("puts = %v", f.puts)
	}
	back, err := ParseSettings([]byte(body))
	if err != nil || !reflect.DeepEqual(back, s) {
		t.Fatalf("%+v, %v", back, err)
	}
}

func TestReadApplied(t *testing.T) {
	ctx := context.Background()
	got, err := ReadApplied(ctx, stClients(&stS3{objects: map[string]string{"prod/applied.json": `{"version":"0.11.0","build":"dmt-prod-install:abc"}` + "\n"}}), bootP)
	if err != nil || got == nil || *got != (Applied{Version: "0.11.0", Build: "dmt-prod-install:abc"}) {
		t.Fatalf("present: %+v, %v", got, err)
	}
	if got, err := ReadApplied(ctx, stClients(&stS3{}), bootP); err != nil || got != nil {
		t.Fatalf("absent: %+v, %v", got, err)
	}
	if got, err := ReadApplied(ctx, stClients(&stS3{noBucket: true}), bootP); err != nil || got != nil {
		t.Fatalf("no bucket: %+v, %v", got, err)
	}
}

func TestAbsentObjectCodes(t *testing.T) {
	for _, code := range []string{"NoSuchKey", "NotFound"} {
		got, err := ReadApplied(context.Background(), stClients(&stS3{missing: code}), bootP)
		if err != nil || got != nil {
			t.Fatalf("%s: %+v, %v", code, got, err)
		}
	}
}

func TestReadOutputs(t *testing.T) {
	ctx := context.Background()
	body := `{"dashboard_url":{"sensitive":false,"type":"string","value":"https://d.example.com"},"user_pool_id":{"value":"eu-west-1_abc"},"version":{"value":"0.11.0"},"other":{"value":{"a":1}}}`
	got, err := ReadOutputs(ctx, stClients(&stS3{objects: map[string]string{"prod/outputs.json": body}}), bootP)
	if err != nil || got == nil || *got != (Outputs{DashboardURL: "https://d.example.com", UserPoolID: "eu-west-1_abc", Version: "0.11.0"}) {
		t.Fatalf("present: %+v, %v", got, err)
	}
	if got, err := ReadOutputs(ctx, stClients(&stS3{}), bootP); err != nil || got != nil {
		t.Fatalf("absent: %+v, %v", got, err)
	}
	if got, err := ReadOutputs(ctx, stClients(&stS3{noBucket: true}), bootP); err != nil || got != nil {
		t.Fatalf("no bucket: %+v, %v", got, err)
	}
}

func TestReadPlan(t *testing.T) {
	ctx := context.Background()
	f := &stS3{objects: map[string]string{"prod/plans/dmt-prod-install:abc/plan.json": "{}"}}
	got, err := ReadPlan(ctx, stClients(f), bootP, "dmt-prod-install:abc")
	if err != nil || string(got) != "{}" {
		t.Fatalf("present: %q, %v", got, err)
	}
	if got, err := ReadPlan(ctx, stClients(f), bootP, "other"); err != nil || got != nil {
		t.Fatalf("absent: %q, %v", got, err)
	}
	if got, err := ReadPlan(ctx, stClients(&stS3{noBucket: true}), bootP, "x"); err != nil || got != nil {
		t.Fatalf("no bucket: %q, %v", got, err)
	}
}

func TestOtherEnvs(t *testing.T) {
	ctx := context.Background()
	f := &stS3{objects: map[string]string{
		"prod/install.json": "{}", "staging/install.json": "{}", "dev/install.json": "{}", "dev/plans/x/install.json": "{}",
		"prod/plans/a/plan.json": "{}", "qa/applied.json": "{}", "install.json": "{}",
	}}
	got, err := OtherEnvs(ctx, stClients(f), bootP)
	if err != nil || !reflect.DeepEqual(got, []string{"dev", "staging"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	f.desc = true
	if got, err := OtherEnvs(ctx, stClients(f), bootP); err != nil || !reflect.DeepEqual(got, []string{"dev", "staging"}) {
		t.Fatalf("listed descending: %v, %v", got, err)
	}
	if got, err := OtherEnvs(ctx, stClients(&stS3{noBucket: true}), bootP); err != nil || len(got) != 0 {
		t.Fatalf("no bucket: %v, %v", got, err)
	}
	if got, err := OtherEnvs(ctx, stClients(&stS3{}), bootP); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v, %v", got, err)
	}
}

func TestPutConfig(t *testing.T) {
	f := &stS3{}
	if err := PutConfig(context.Background(), stClients(f), bootP, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if f.puts["dmt-install-123456789012-eu-west-1/prod/config/main.tf.json"] != "{}" {
		t.Fatalf("puts = %v", f.puts)
	}
}

func TestUploadRelease(t *testing.T) {
	dir := writeLocal(t, "0.11.0-dev", nil)
	r, err := Resolve(context.Background(), nil, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &stS3{}
	base, err := UploadRelease(context.Background(), stClients(f), bootP, r)
	if err != nil || base != "s3://dmt-install-123456789012-eu-west-1/prod/release" {
		t.Fatalf("%q, %v", base, err)
	}
	if len(f.puts) != len(r.Files()) {
		t.Fatalf("%d puts for %d files", len(f.puts), len(r.Files()))
	}
	for _, name := range r.Files() {
		want, _ := os.ReadFile(filepath.Join(dir, name))
		if got := f.puts["dmt-install-123456789012-eu-west-1/prod/release/"+name]; got != string(want) {
			t.Fatalf("%s = %q", name, got)
		}
	}
	r.Local = filepath.Join(dir, "missing")
	if _, err := UploadRelease(context.Background(), stClients(f), bootP, r); err == nil {
		t.Fatal("missing dir: no error")
	}
}
