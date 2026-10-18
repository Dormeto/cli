package build

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func managerEnv(action string) map[string]string {
	return map[string]string{
		"ACTION": action, "BUCKET": "dmt-install-123456789012-eu-west-1", "ENV": "prod", "TF_VERSION": "1.16.5",
		"RELEASE_BASE": "https://releases.dormeto.com/manager/0.11.0", "RELEASE_VERSION": "0.11.0",
		"PLAN_BUILD": "dmt-prod-install:plan-1", "CODEBUILD_BUILD_ID": "dmt-prod-install:build-2",
	}
}

const mb = "dmt-install-123456789012-eu-west-1/prod/"

func TestManagerPlanUploads(t *testing.T) {
	s := newSandbox(t)
	out, err := s.run("manager.sh", managerEnv("plan"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, k := range []string{"plans/dmt-prod-install:build-2/tfplan", "plans/dmt-prod-install:build-2/plan.json", "plans/dmt-prod-install:build-2/.terraform.lock.hcl", "plans/dmt-prod-install:build-2/main.tf.json", "config/.terraform.lock.hcl"} {
		if _, ok := s.uploaded(mb + k); !ok {
			t.Errorf("%s not uploaded\n%s", k, strings.Join(s.log(), "\n"))
		}
	}
	if _, ok := s.uploaded(mb + "plans/dmt-prod-install:build-2/release.json"); ok {
		t.Error("a CLI plan has a release.json")
	}
	if j, _ := s.uploaded(mb + "plans/dmt-prod-install:build-2/plan.json"); !strings.Contains(j, `"plan"`) {
		t.Errorf("plan.json = %q", j)
	}
	log := s.log()
	for _, want := range []string{
		"curl -fsSL -o terraform_1.16.5_linux_amd64.zip https://releases.hashicorp.com/terraform/1.16.5/terraform_1.16.5_linux_amd64.zip",
		"curl -fsSL -o terraform-aws-dormeto-0.11.0.zip https://releases.dormeto.com/manager/0.11.0/terraform-aws-dormeto-0.11.0.zip",
		"env CHECKPOINT_DISABLE=1 TF_IN_AUTOMATION=1 TF_CLI_ARGS=-no-color", "terraform init -input=false", "terraform plan -input=false -out=tfplan", "terraform show -json tfplan", "terraform-from-bin",
	} {
		if !has(log, want) {
			t.Errorf("no %q in\n%s", want, strings.Join(log, "\n"))
		}
	}
	if has(log, "terraform apply") {
		t.Error("plan applied")
	}
	if !strings.Contains(out, "dormeto: ") {
		t.Errorf("no progress line: %s", out)
	}
}

func TestManagerPlanUsesTheKeptLockFile(t *testing.T) {
	s := newSandbox(t)
	// no lock yet: it is not fetched
	if out, err := s.run("manager.sh", managerEnv("plan")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if has(s.log(), "aws s3 cp s3://"+mb+"config/.terraform.lock.hcl") {
		t.Error("a missing lock file was fetched")
	}
}

func TestManagerWrongTerraformSumStops(t *testing.T) {
	s := newSandbox(t)
	env := managerEnv("plan")
	env["FAKE_BAD"] = "terraform_1.16.5_linux_amd64.zip"
	out, err := s.run("manager.sh", env)
	if err == nil {
		t.Fatalf("no failure: %s", out)
	}
	if has(s.log(), "terraform init") || has(s.log(), "unzip") {
		t.Errorf("went on after the mismatch:\n%s", strings.Join(s.log(), "\n"))
	}
	if !strings.Contains(out, "dormeto:") {
		t.Errorf("no message: %s", out)
	}
}

func TestManagerWrongReleaseSumStops(t *testing.T) {
	s := newSandbox(t)
	env := managerEnv("plan")
	env["FAKE_BAD"] = "terraform-aws-dormeto-0.11.0.zip"
	if out, err := s.run("manager.sh", env); err == nil {
		t.Fatalf("no failure: %s", out)
	}
	if has(s.log(), "terraform init") {
		t.Error("init after the mismatch")
	}
}

func TestManagerApply(t *testing.T) {
	s := newSandbox(t)
	if out, err := s.run("manager.sh", managerEnv("apply")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	plan := index(log, "aws s3 cp --only-show-errors s3://"+mb+"plans/dmt-prod-install:plan-1/tfplan")
	lock := index(log, "aws s3 cp --only-show-errors s3://"+mb+"plans/dmt-prod-install:plan-1/.terraform.lock.hcl")
	init := index(log, "terraform init")
	if lock < 0 || init < 0 || lock > init {
		t.Errorf("the plan's lock must come before init:\n%s", strings.Join(log, "\n"))
	}
	if plan < 0 || index(log, "terraform apply -input=false tfplan") < plan {
		t.Errorf("apply must use the downloaded plan:\n%s", strings.Join(log, "\n"))
	}
	for _, l := range log {
		if strings.Contains(l, "auto-approve") {
			t.Errorf("auto-approve: %s", l)
		}
	}
	if has(log, "terraform plan") {
		t.Error("apply planned again")
	}
	if o, ok := s.uploaded(mb + "outputs.json"); !ok || !strings.Contains(o, `"o"`) {
		t.Errorf("outputs.json = %q %v", o, ok)
	}
	a, ok := s.uploaded(mb + "applied.json")
	if want := `{"version":"0.11.0","build":"dmt-prod-install:build-2"}`; !ok || strings.TrimSpace(a) != want {
		t.Errorf("applied.json = %q, want %s", a, want)
	}
}

func TestManagerPlanDestroy(t *testing.T) {
	s := newSandbox(t)
	if out, err := s.run("manager.sh", managerEnv("plan-destroy")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !has(s.log(), "terraform plan -input=false -destroy -out=tfplan") {
		t.Errorf("no destroy plan:\n%s", strings.Join(s.log(), "\n"))
	}
}

func TestManagerApplyDestroy(t *testing.T) {
	s := newSandbox(t)
	s.put("applied/main.tf.json", "applied-config")
	s.put("applied/.terraform.lock.hcl", "applied-lock")
	if out, err := s.run("manager.sh", managerEnv("apply-destroy")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	if !has(log, "terraform apply -input=false tfplan") {
		t.Error("no apply")
	}
	for _, k := range []string{"applied.json", "outputs.json"} {
		if !has(log, "aws s3 rm --only-show-errors s3://"+mb+k) {
			t.Errorf("%s not removed:\n%s", k, strings.Join(log, "\n"))
		}
	}
	if _, ok := s.uploaded(mb + "applied.json"); ok {
		t.Error("applied.json written by a destroy")
	}
	for _, k := range []string{"applied/main.tf.json", "applied/.terraform.lock.hcl"} {
		if _, ok := s.uploaded(mb + k); ok {
			t.Errorf("%s kept by a destroy:\n%s", k, strings.Join(log, "\n"))
		}
	}
}

func TestManagerS3ReleaseBase(t *testing.T) {
	s := newSandbox(t)
	env := managerEnv("plan")
	env["RELEASE_BASE"] = "s3://dmt-install-123456789012-eu-west-1/prod/release"
	if out, err := s.run("manager.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	for _, f := range []string{"SHA256SUMS", "terraform-aws-dormeto-0.11.0.zip"} {
		if !has(log, "aws s3 cp --only-show-errors s3://dmt-install-123456789012-eu-west-1/prod/release/"+f+" "+f) {
			t.Errorf("%s not fetched from s3:\n%s", f, strings.Join(log, "\n"))
		}
	}
	for _, l := range log {
		if strings.HasPrefix(l, "curl") && !strings.Contains(l, "releases.hashicorp.com") {
			t.Errorf("curl outside hashicorp: %s", l)
		}
	}
}

func TestManagerRefusesAnUnknownAction(t *testing.T) {
	s := newSandbox(t)
	if out, err := s.run("manager.sh", managerEnv("destroy")); err == nil {
		t.Fatalf("accepted: %s", out)
	}
}

func TestManagerPlanFetchesTheKeptLockFile(t *testing.T) {
	s := newSandbox(t)
	s.write2(s.fake+"/s3/"+mb+"config/.terraform.lock.hcl", "kept")
	if out, err := s.run("manager.sh", managerEnv("plan")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	if c, i := index(log, "aws s3 cp --only-show-errors s3://"+mb+"config/.terraform.lock.hcl"), index(log, "terraform init"); c < 0 || c > i {
		t.Errorf("the kept lock must be fetched before init:\n%s", strings.Join(log, "\n"))
	}
}

func TestManagerLockFileHeadObject(t *testing.T) {
	lock := "aws s3 cp --only-show-errors s3://" + mb + "config/.terraform.lock.hcl"
	t.Run("absent: no lock, the run goes on", func(t *testing.T) {
		s := newSandbox(t)
		if out, err := s.run("manager.sh", managerEnv("plan")); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !has(s.log(), "aws s3api head-object --bucket dmt-install-123456789012-eu-west-1 --key prod/config/.terraform.lock.hcl") || has(s.log(), lock) {
			t.Errorf("log:\n%s", strings.Join(s.log(), "\n"))
		}
	})
	t.Run("an error that is not a 404 stops before init", func(t *testing.T) {
		s := newSandbox(t)
		env := managerEnv("plan")
		env["FAKE_HEAD_ERR"] = "1"
		out, err := s.run("manager.sh", env)
		if err == nil || !strings.Contains(out, "dormeto: cannot check the lock file") {
			t.Fatalf("err %v\n%s", err, out)
		}
		if has(s.log(), "terraform init") {
			t.Error("init after an unreadable lock check")
		}
	})
}

func TestManagerApplyRecordsItsConfiguration(t *testing.T) {
	plan := "plans/dmt-prod-install:plan-1/"
	t.Run("the plan's configuration and lock, copied to applied/ before applied.json", func(t *testing.T) {
		s := newSandbox(t)
		s.put("config/main.tf.json", "cli-config") // a later CLI run's, not applied
		s.put(plan+"main.tf.json", "plan-config")
		s.put(plan+".terraform.lock.hcl", "plan-lock")
		if out, err := s.run("manager.sh", managerEnv("apply")); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		log := s.log()
		if !has(log, "config apply plan-config") || has(log, "aws s3 cp --only-show-errors s3://"+mb+"config/main.tf.json") {
			t.Errorf("the apply must run the plan's configuration:\n%s", strings.Join(log, "\n"))
		}
		for k, want := range map[string]string{"applied/main.tf.json": "plan-config", "applied/.terraform.lock.hcl": "plan-lock"} {
			if got, _ := s.uploaded(mb + k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		config := index(log, "aws s3 cp --only-show-errors main.tf.json s3://"+mb+"applied/main.tf.json")
		lock := index(log, "aws s3 cp --only-show-errors .terraform.lock.hcl s3://"+mb+"applied/.terraform.lock.hcl")
		applied := index(log, "aws s3 cp --only-show-errors applied.json s3://"+mb+"applied.json")
		if config < 0 || lock < 0 || applied < config || applied < lock {
			t.Errorf("applied/ must be written before applied.json:\n%s", strings.Join(log, "\n"))
		}
	})
	t.Run("a plan made before plans kept their configuration: config/'s", func(t *testing.T) {
		s := newSandbox(t)
		s.put("config/main.tf.json", "cli-config")
		if out, err := s.run("manager.sh", managerEnv("apply")); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !has(s.log(), "config apply cli-config") {
			t.Errorf("log:\n%s", strings.Join(s.log(), "\n"))
		}
		if got, _ := s.uploaded(mb + "applied/main.tf.json"); got != "cli-config" {
			t.Errorf("applied/main.tf.json = %q", got)
		}
	})
}

// A dashboard request: the rule starts the build with ACTION=request and REQUEST_ETAG besides the project's variables.

const (
	requestID  = "20261018T100000Z-0123abcd"
	requestKey = "prod/requests/" + requestID + ".json"
	olderID    = "20261018T090000Z-89abcdef"
	build      = "dmt-prod-install:build-3"
	planBuild  = "dmt-prod-install:6f1e2d3c-4b5a-4978-8a9b-0c1d2e3f4a5b"
	releases   = "https://releases.dormeto.com/manager/"
	stagedBase = "s3://dmt-install-123456789012-eu-west-1/prod/release"
)

// requestEnv is the environment of the build the rule starts for the request at requestKey: the project's variables,
// ACTION and REQUEST_ETAG (that object's ETag); published are the releases the fake curl serves.
func (s *sandbox) requestEnv(published ...string) map[string]string {
	body, _ := s.uploaded(mb + "requests/" + requestID + ".json")
	return map[string]string{
		"ACTION": "request", "REQUEST_ETAG": etagOf(body), "BUCKET": "dmt-install-123456789012-eu-west-1", "ENV": "prod",
		"TF_VERSION": "1.16.5", "SCRIPT": "manager.sh", "CODEBUILD_BUILD_ID": build,
		"FAKE_PUBLISHED": strings.Join(published, " "),
	}
}

// etagOf is the ETag S3 gives an object of that body (one part): its MD5.
func etagOf(body string) string { return fmt.Sprintf("%x", md5.Sum([]byte(body))) }

func (s *sandbox) put(key, body string) { s.write2(filepath.Join(s.fake, "s3", mb+key), body) }

func (s *sandbox) request(body string) { s.put("requests/"+requestID+".json", body) }

// installed is what an install the CLI applied leaves, with a configuration a later CLI run wrote without applying it.
func (s *sandbox) installed(version string) {
	s.put("applied.json", `{"version":"`+version+`","build":"dmt-prod-install:build-2"}`)
	s.put("applied/main.tf.json", "applied-config")
	s.put("applied/.terraform.lock.hcl", "applied-lock")
	s.put("config/main.tf.json", "cli-config")
}

// prepared is the plan a dashboard plan request left, of version, made when appliedBuild was applied.
func (s *sandbox) prepared(version, appliedBuild string) {
	p := "plans/" + planBuild + "/"
	// a base no apply uses: it derives its own from the version
	s.put(p+"release.json", `{"version":"`+version+`","base":"https://evil.example/manager/`+version+`","request":"`+requestKey+`","appliedBuild":"`+appliedBuild+`"}`)
	s.put(p+"main.tf.json", "applied-config")
	s.put(p+".terraform.lock.hcl", "applied-lock")
	s.put(p+"tfplan", "saved-plan")
}

func outcome(status, message, action, version string) map[string]string {
	return map[string]string{"build": build, "status": status, "message": message, "action": action, "version": version}
}

var running = outcome("running", "", "", "")

// checkResults checks the results written, in order: each to results/<id>.json, then the same body to
// results/<build id>.json, which both hold the last one.
func (s *sandbox) checkResults(id string, want ...map[string]string) {
	s.t.Helper()
	lines := s.results()
	var got []map[string]string
	var last string
	for i, l := range lines {
		name, body, _ := strings.Cut(l, " ")
		if i%2 == 1 {
			if name != build+".json" || body != last {
				s.t.Errorf("result %d = %s, want %s.json %s", i, l, build, last)
			}
			continue
		}
		if name != id+".json" {
			s.t.Errorf("result %d = %s, want %s.json first", i, l, id)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			s.t.Fatalf("result %q: %v", l, err)
		}
		got, last = append(got, m), body
	}
	if len(lines)%2 != 0 {
		s.t.Errorf("a result written to one key only: %v", lines)
	}
	if !reflect.DeepEqual(got, want) {
		s.t.Errorf("results = %v, want %v", got, want)
	}
	for _, k := range []string{id, build} {
		if b, ok := s.uploaded(mb + "results/" + k + ".json"); !ok || strings.TrimSpace(b) != last {
			s.t.Errorf("results/%s.json = %q, want %s", k, b, last)
		}
	}
}

// checkNoTerraform checks that the build stopped before Terraform was downloaded.
func (s *sandbox) checkNoTerraform() {
	s.t.Helper()
	for _, l := range s.log() {
		if strings.HasPrefix(l, "terraform") || strings.Contains(l, "releases.hashicorp.com") {
			s.t.Errorf("went on to Terraform: %s", l)
		}
	}
}

func jsonOf(t *testing.T, s string) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return m
}

func TestManagerRequestPlanPublished(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.request(`{"action":"plan","version":"0.12.0","requestedBy":"alice@example.com","requestedAt":"2026-10-18T10:00:00Z"}`)
	out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	p := mb + "plans/dmt-prod-install:build-3/"
	r, _ := s.uploaded(p + "release.json")
	want := map[string]string{"version": "0.12.0", "base": releases + "0.12.0", "request": requestKey, "appliedBuild": "dmt-prod-install:build-2"}
	if got := jsonOf(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("release.json = %v, want %v", got, want)
	}
	for k, want := range map[string]string{"main.tf.json": "applied-config", ".terraform.lock.hcl": "applied-lock", "tfplan": "plan\n"} {
		if got, _ := s.uploaded(p + k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	log := s.log()
	for _, want := range []string{
		"config plan applied-config", "terraform plan -input=false -out=tfplan",
		"curl -fsSL -o terraform-aws-dormeto-0.12.0.zip " + releases + "0.12.0/terraform-aws-dormeto-0.12.0.zip",
	} {
		if !has(log, want) {
			t.Errorf("no %q in\n%s", want, strings.Join(log, "\n"))
		}
	}
	if has(log, "terraform apply") {
		t.Error("a plan request applied")
	}
	for _, l := range log {
		if strings.Contains(l, "prod/config/") {
			t.Errorf("config/ touched: %s", l)
		}
	}
	if _, ok := s.uploaded(mb + "config/.terraform.lock.hcl"); ok {
		t.Error("config/.terraform.lock.hcl written")
	}
	if w, r := index(log, "aws s3 cp --only-show-errors result.json s3://"+mb+"results/"+requestID+".json"),
		index(log, "aws s3 cp --only-show-errors s3://"+mb+"requests/"+requestID+".json"); w < 0 || r < w {
		t.Errorf("the running result must come before the request is read:\n%s", strings.Join(log, "\n"))
	}
	s.checkResults(requestID, running, outcome("succeeded", "manager 0.12.0 planned", "plan", "0.12.0"))
	for _, want := range []string{"dormeto: reading the request " + requestID + "\n", "dormeto: manager 0.12.0: published\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
}

func TestManagerRequestReleasesURL(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.request(`{"action":"plan","version":"0.12.0"}`)
	env := s.requestEnv("https://releases.example/manager/0.12.0")
	env["RELEASES_URL"] = "https://releases.example/manager"
	if out, err := s.run("manager.sh", env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r, _ := s.uploaded(mb + "plans/dmt-prod-install:build-3/release.json")
	if got := jsonOf(t, r)["base"]; got != "https://releases.example/manager/0.12.0" {
		t.Errorf("base = %q", got)
	}
}

func TestManagerRequestPlanStaged(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.put("release/SHA256SUMS", "good  terraform-aws-dormeto-0.12.0.zip\n")
	s.request(`{"action":"plan","version":"0.12.0"}`)
	out, err := s.run("manager.sh", s.requestEnv())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r, _ := s.uploaded(mb + "plans/dmt-prod-install:build-3/release.json")
	if got := jsonOf(t, r)["base"]; got != stagedBase {
		t.Errorf("base = %q, want %s", got, stagedBase)
	}
	if !has(s.log(), "aws s3 cp --only-show-errors "+stagedBase+"/terraform-aws-dormeto-0.12.0.zip terraform-aws-dormeto-0.12.0.zip") {
		t.Errorf("the archive not fetched from the staged release:\n%s", strings.Join(s.log(), "\n"))
	}
	if !strings.Contains(out, "dormeto: manager 0.12.0: staged\n") {
		t.Errorf("output:\n%s", out)
	}
	s.checkResults(requestID, running, outcome("succeeded", "manager 0.12.0 planned", "plan", "0.12.0"))
}

func TestManagerRequestNeitherPublishedNorStaged(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.put("release/SHA256SUMS", "good  terraform-aws-dormeto-0.12.0.zip\ngood  terraform-aws-dormeto-0.13.0.zip.old\n")
	s.request(`{"action":"plan","version":"0.13.0"}`)
	out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0"))
	if err == nil || !strings.Contains(out, "dormeto: manager 0.13.0 is neither published nor staged\n") {
		t.Fatalf("err %v\n%s", err, out)
	}
	s.checkNoTerraform()
	s.checkResults(requestID, running, outcome("failed", "manager 0.13.0 is neither published nor staged", "plan", "0.13.0"))
}

func TestManagerRequestNoDowngrade(t *testing.T) {
	for _, c := range []struct{ applied, version string }{
		{"0.11.0", "0.10.0"}, {"0.12.0", "0.12.0-dev.1"}, {"0.12.0-dev.2", "0.12.0-dev.10"}, {"1.0.0", "0.99.99"},
	} {
		t.Run(c.version+" after "+c.applied+": refused", func(t *testing.T) {
			s := newSandbox(t)
			s.installed(c.applied)
			s.request(`{"action":"plan","version":"` + c.version + `"}`)
			out, err := s.run("manager.sh", s.requestEnv(releases+c.version))
			want := "manager " + c.version + " is older than the applied " + c.applied
			if err == nil || !strings.Contains(out, "dormeto: "+want+"\n") {
				t.Fatalf("err %v\n%s", err, out)
			}
			s.checkNoTerraform()
			s.checkResults(requestID, running, outcome("failed", want, "plan", c.version))
		})
	}
	for _, c := range []struct{ applied, version string }{
		{"0.11.0", "0.11.0"}, {"0.9.0", "0.10.0"}, {"0.12.0-dev.1", "0.12.0-dev.2"}, {"0.12.0-dev.2", "0.12.0"},
	} {
		t.Run(c.version+" after "+c.applied+": planned", func(t *testing.T) {
			s := newSandbox(t)
			s.installed(c.applied)
			s.request(`{"action":"plan","version":"` + c.version + `"}`)
			if out, err := s.run("manager.sh", s.requestEnv(releases+c.version)); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			s.checkResults(requestID, running, outcome("succeeded", "manager "+c.version+" planned", "plan", c.version))
		})
	}
}

func TestManagerRequestETagStopsBeforeAnyDownload(t *testing.T) {
	etag := etagOf(`{"action":"plan","version":"0.12.0"}`)
	for name, env := range map[string]map[string]string{
		"with a quote":               {"REQUEST_ETAG": etag + `"`},
		"with a slash":               {"REQUEST_ETAG": "../" + etag},
		"closing the JSON string":    {"REQUEST_ETAG": etag + `","name":"RELEASES_URL","value":"https://evil.example`},
		"upper-case hexadecimal":     {"REQUEST_ETAG": strings.ToUpper(etag)},
		"too short":                  {"REQUEST_ETAG": etag[1:]},
		"longer than 64 characters":  {"REQUEST_ETAG": etag + "-" + strings.Repeat("1", 40)},
		"with a second line":         {"REQUEST_ETAG": etag + "\n" + etag},
		"empty":                      {"REQUEST_ETAG": ""},
		"an environment not a name":  {"ENV": ".*"},
		"the key the rule once gave": {"REQUEST_ETAG": "", "REQUEST_KEY": requestKey},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			s.installed("0.11.0")
			s.request(`{"action":"plan","version":"0.12.0"}`)
			e := s.requestEnv(releases + "0.12.0")
			for k, v := range env {
				e[k] = v
			}
			out, err := s.run("manager.sh", e)
			if err == nil || !strings.Contains(out, "dormeto: ") {
				t.Fatalf("err %v\n%s", err, out)
			}
			for _, l := range s.log() {
				if strings.HasPrefix(l, "aws ") || strings.HasPrefix(l, "curl ") {
					t.Errorf("went on: %s", l)
				}
			}
			if r := s.results(); r != nil {
				t.Errorf("results written: %v", r)
			}
		})
	}
}

func TestManagerRequestTakesTheEventsRequest(t *testing.T) {
	body := `{"action":"plan","version":"0.12.0","requestedAt":"2026-10-18T10:00:00Z"}`
	t.Run("among several requests, the one with the event's ETag", func(t *testing.T) {
		s := newSandbox(t)
		s.installed("0.11.0")
		s.put("requests/"+olderID+".json", `{"action":"plan","version":"0.12.1","requestedAt":"2026-10-18T09:00:00Z"}`)
		s.request(body)
		s.put("requests/20261018T110000Z-fedcba98.json", `{"action":"plan","version":"0.12.2","requestedAt":"2026-10-18T11:00:00Z"}`)
		out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0", releases+"0.12.1", releases+"0.12.2"))
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		s.checkResults(requestID, running, outcome("succeeded", "manager 0.12.0 planned", "plan", "0.12.0"))
		r, _ := s.uploaded(mb + "plans/" + build + "/release.json")
		if got := jsonOf(t, r)["request"]; got != requestKey {
			t.Errorf("release.json's request = %q", got)
		}
	})
	for _, c := range []struct{ name, older, taken string }{
		{"of two with the same content, the newer when the older succeeded", "succeeded", requestID},
		{"of two with the same content, the newer when the older failed", "failed", requestID},
		{"of two with the same content, the older while its result says running", "running", olderID},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.installed("0.11.0")
			s.put("requests/"+olderID+".json", body)
			s.put("results/"+olderID+".json", `{"build":"dmt-prod-install:build-1","status":"`+c.older+`","message":"","action":"plan","version":"0.12.0"}`)
			s.request(body)
			if out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0")); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			s.checkResults(c.taken, running, outcome("succeeded", "manager 0.12.0 planned", "plan", "0.12.0"))
		})
	}
	nothing := func(t *testing.T, s *sandbox, env map[string]string) {
		t.Helper()
		out, err := s.run("manager.sh", env)
		if err != nil || !strings.Contains(out, "dormeto: no request to take for this event\n") {
			t.Fatalf("err %v\n%s", err, out)
		}
		if r := s.results(); r != nil {
			t.Errorf("results written: %v", r)
		}
		for _, l := range s.log() {
			if strings.HasPrefix(l, "aws s3 cp --only-show-errors s3://"+mb+"requests/") || strings.HasPrefix(l, "aws s3 cp --only-show-errors result.json") ||
				strings.HasPrefix(l, "curl ") || strings.HasPrefix(l, "terraform") {
				t.Errorf("went on: %s", l)
			}
		}
	}
	t.Run("a request replayed after its result: nothing to take, nothing written", func(t *testing.T) {
		s := newSandbox(t)
		s.installed("0.11.0")
		s.request(body)
		s.put("results/"+requestID+".json", `{"build":"dmt-prod-install:build-1","status":"succeeded","message":"manager 0.12.0 planned","action":"plan","version":"0.12.0"}`)
		nothing(t, s, s.requestEnv(releases+"0.12.0"))
	})
	t.Run("no request with the event's ETag: nothing to take, nothing written", func(t *testing.T) {
		s := newSandbox(t)
		s.installed("0.11.0")
		s.request(body)
		env := s.requestEnv(releases + "0.12.0")
		env["REQUEST_ETAG"] = etagOf("another body")
		nothing(t, s, env)
	})
	t.Run("keys with the event's ETag that are not this environment's requests are never taken", func(t *testing.T) {
		s := newSandbox(t)
		s.installed("0.11.0")
		s.request(body)
		etag := etagOf(body)
		var contents []map[string]string
		for _, k := range []string{
			"staging/requests/" + requestID + ".json", "prod/results/" + requestID + ".json",
			"prod/requests/../requests/" + requestID + ".json", requestKey + "/../../applied.json",
			requestKey + "\nprod/requests/x.json", "prod/requests/2026101810000000-0123abcd.json",
			"prod/requests/20261018T100000Z-0123ABCD.json", "prod/requests/" + requestID + ".txt", requestKey + `"`,
			"prod/requests/" + strings.Repeat("2", 60) + "T100000Z-0123abcd.json",
		} {
			contents = append(contents, map[string]string{"Key": k, "ETag": `"` + etag + `"`})
		}
		b, _ := json.Marshal(map[string]any{"Contents": contents})
		s.write2(filepath.Join(s.fake, "listing.json"), string(b))
		nothing(t, s, s.requestEnv(releases+"0.12.0"))
		if has(s.log(), "aws s3api head-object") {
			t.Errorf("a key that is not a request was looked at:\n%s", strings.Join(s.log(), "\n"))
		}
	})
}

func TestManagerRequestFieldsRefused(t *testing.T) {
	notObject := "the request " + requestID + " is not a JSON object"
	noAction := "the request " + requestID + " asks for neither a plan nor an apply"
	noVersion := "the request " + requestID + " has no version of the form x.y.z"
	noPlan := "the request " + requestID + " has no plan build of prod"
	for _, c := range []struct{ name, body, message, action, version string }{
		{"not JSON", `nope`, notObject, "", ""},
		{"an array", `[{"action":"plan","version":"0.12.0"}]`, notObject, "", ""},
		{"two objects", `{"action":"plan","version":"0.12.0"} {"action":"apply"}`, notObject, "", ""},
		{"another action", `{"action":"destroy","version":"0.12.0"}`, noAction, "", ""},
		{"an action not a string", `{"action":["plan"],"version":"0.12.0"}`, noAction, "", ""},
		{"an action with a second line", `{"action":"plan\napply","version":"0.12.0"}`, noAction, "", ""},
		{"no action", `{"version":"0.12.0"}`, noAction, "", ""},
		{"a version with a second line", `{"action":"plan","version":"0.12.0\n0.13.0"}`, noVersion, "plan", ""},
		{"a version with a trailing new line", `{"action":"plan","version":"0.12.0\n"}`, noVersion, "plan", ""},
		{"a version not a string", `{"action":"plan","version":12}`, noVersion, "plan", ""},
		{"a version of two numbers", `{"action":"plan","version":"0.12"}`, noVersion, "plan", ""},
		{"a version with a v", `{"action":"plan","version":"v0.12.0"}`, noVersion, "plan", ""},
		{"a version with build metadata", `{"action":"plan","version":"0.12.0+abc"}`, noVersion, "plan", ""},
		{"a version with a path", `{"action":"plan","version":"0.12.0/../0.13.0"}`, noVersion, "plan", ""},
		{"an apply without a plan build", `{"action":"apply","version":"0.12.0"}`, noPlan, "apply", "0.12.0"},
		{"another environment's plan build", `{"action":"apply","version":"0.12.0","planBuild":"dmt-staging-install:6f1e2d3c-4b5a-4978-8a9b-0c1d2e3f4a5b"}`, noPlan, "apply", "0.12.0"},
		{"a plan build of another form", `{"action":"apply","version":"0.12.0","planBuild":"dmt-prod-install:plan-1"}`, noPlan, "apply", "0.12.0"},
		{"a plan build with a second line", `{"action":"apply","version":"0.12.0","planBuild":"` + planBuild + `\n` + planBuild + `"}`, noPlan, "apply", "0.12.0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.installed("0.11.0")
			s.prepared("0.12.0", "dmt-prod-install:build-2")
			s.request(c.body)
			out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0", releases+"0.13.0"))
			if err == nil || !strings.Contains(out, "dormeto: "+c.message+"\n") {
				t.Fatalf("err %v\n%s", err, out)
			}
			if strings.Contains(out, "0.13.0") {
				t.Errorf("the request's value printed:\n%s", out)
			}
			s.checkNoTerraform()
			s.checkResults(requestID, running, outcome("failed", c.message, c.action, c.version))
		})
	}
}

func TestManagerRequestIgnoresOtherFields(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.request(`{"action":"plan","version":"0.12.0","releaseBase":"https://evil.example","planBuild":"dmt-prod-install:x"}`)
	if out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r, _ := s.uploaded(mb + "plans/dmt-prod-install:build-3/release.json")
	if got := jsonOf(t, r)["base"]; got != releases+"0.12.0" {
		t.Errorf("base = %q", got)
	}
	for _, l := range s.log() {
		if strings.Contains(l, "evil.example") {
			t.Errorf("the request's base used: %s", l)
		}
	}
}

func TestManagerRequestWithoutAppliedConfiguration(t *testing.T) {
	// an install applied before plans kept their configuration: applied.json, no applied/
	s := newSandbox(t)
	s.put("applied.json", `{"version":"0.11.0","build":"dmt-prod-install:build-2"}`)
	s.put("config/main.tf.json", "cli-config")
	s.request(`{"action":"plan","version":"0.12.0"}`)
	out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0"))
	want := "run `dormeto install` with the CLI 0.2.0 or later"
	if err == nil || !strings.Contains(out, "dormeto: "+want+"\n") {
		t.Fatalf("err %v\n%s", err, out)
	}
	s.checkNoTerraform()
	s.checkResults(requestID, running, outcome("failed", want, "plan", "0.12.0"))
}

func TestManagerRequestApply(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.prepared("0.12.0", "dmt-prod-install:build-2")
	s.request(`{"action":"apply","version":"0.12.0","planBuild":"` + planBuild + `"}`)
	out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	log := s.log()
	plan := index(log, "aws s3 cp --only-show-errors s3://"+mb+"plans/"+planBuild+"/tfplan tfplan")
	if plan < 0 || index(log, "terraform apply -input=false tfplan") < plan || !has(log, "config apply applied-config") {
		t.Errorf("the apply must run the saved plan with its configuration:\n%s", strings.Join(log, "\n"))
	}
	if has(log, "terraform plan") {
		t.Error("an apply request planned")
	}
	if !has(log, "curl -fsSL -o terraform-aws-dormeto-0.12.0.zip "+releases+"0.12.0/terraform-aws-dormeto-0.12.0.zip") {
		t.Errorf("the release not derived from the version:\n%s", strings.Join(log, "\n"))
	}
	for _, l := range log {
		if strings.Contains(l, "evil.example") {
			t.Errorf("the plan's base used: %s", l)
		}
	}
	a, _ := s.uploaded(mb + "applied.json")
	if want := `{"version":"0.12.0","build":"dmt-prod-install:build-3"}`; strings.TrimSpace(a) != want {
		t.Errorf("applied.json = %q, want %s", a, want)
	}
	if got, _ := s.uploaded(mb + "applied/main.tf.json"); got != "applied-config" {
		t.Errorf("applied/main.tf.json = %q", got)
	}
	s.checkResults(requestID, running, outcome("succeeded", "manager 0.12.0 applied", "apply", "0.12.0"))
}

func TestManagerRequestApplyRefused(t *testing.T) {
	for _, c := range []struct{ name, planVersion, appliedBuild, version, message string }{
		{"a plan without its configuration", "0.12.0", "dmt-prod-install:build-2", "0.12.0", "the plan of " + planBuild + " has no configuration"},
		{"a plan of another version", "0.12.0", "dmt-prod-install:build-2", "0.12.1", "the plan of " + planBuild + " is not manager 0.12.1"},
		{"a plan made before the last apply", "0.12.0", "dmt-prod-install:build-1", "0.12.0", "the plan is stale: prepare again"},
		{"a plan made before the first apply", "0.12.0", "", "0.12.0", "the plan is stale: prepare again"},
		{"a plan the CLI made", "", "", "0.12.0", "the plan of " + planBuild + " was not prepared from the dashboard"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSandbox(t)
			s.installed("0.11.0")
			if c.planVersion != "" {
				s.prepared(c.planVersion, c.appliedBuild)
			}
			if strings.HasSuffix(c.message, "has no configuration") {
				if err := os.Remove(filepath.Join(s.fake, "s3", mb+"plans/"+planBuild+"/main.tf.json")); err != nil {
					t.Fatal(err)
				}
			}
			s.request(`{"action":"apply","version":"` + c.version + `","planBuild":"` + planBuild + `"}`)
			out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0", releases+"0.12.1"))
			if err == nil || !strings.Contains(out, "dormeto: "+c.message+"\n") {
				t.Fatalf("err %v\n%s", err, out)
			}
			s.checkNoTerraform()
			s.checkResults(requestID, running, outcome("failed", c.message, "apply", c.version))
		})
	}
}

func TestManagerRequestFailureOutsideTheChecks(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.request(`{"action":"plan","version":"0.12.0"}`)
	env := s.requestEnv(releases + "0.12.0")
	env["FAKE_BAD"] = "terraform-aws-dormeto-0.12.0.zip"
	if out, err := s.run("manager.sh", env); err == nil {
		t.Fatalf("no failure: %s", out)
	}
	s.checkResults(requestID, running, outcome("failed", "the checksum of terraform-aws-dormeto-0.12.0.zip does not match: stopped", "plan", "0.12.0"))

	s = newSandbox(t)
	s.installed("0.11.0")
	s.request(`{"action":"plan","version":"0.12.0"}`)
	env = s.requestEnv(releases + "0.12.0")
	env["FAKE_TERRAFORM_FAIL"] = "plan"
	if out, err := s.run("manager.sh", env); err == nil {
		t.Fatalf("no failure: %s", out)
	}
	s.checkResults(requestID, running, outcome("failed", "the build failed: see its log", "plan", "0.12.0"))
}

func TestManagerRequestFieldLongerThan64Characters(t *testing.T) {
	huge := strings.Repeat("a", 200*1024)
	for _, c := range []struct{ field, body, action, version string }{
		{"action", `{"action":"` + huge + `","version":"0.12.0"}`, "", ""},
		{"version", `{"action":"plan","version":"0.12.0-` + huge + `"}`, "plan", ""},
		{"planBuild", `{"action":"apply","version":"0.12.0","planBuild":"` + planBuild + huge + `"}`, "apply", "0.12.0"},
	} {
		t.Run(c.field, func(t *testing.T) {
			s := newSandbox(t)
			s.installed("0.11.0")
			s.request(c.body)
			out, err := s.run("manager.sh", s.requestEnv(releases+"0.12.0"))
			want := "the request " + requestID + ": its " + c.field + " is longer than 64 characters"
			if err == nil || !strings.Contains(out, "dormeto: "+want+"\n") || len(out) > 2048 {
				t.Fatalf("err %v\n%.4000s", err, out)
			}
			s.checkNoTerraform()
			s.checkResults(requestID, running, outcome("failed", want, c.action, c.version))
		})
	}
}

func TestManagerRequestReleasesUnreachable(t *testing.T) {
	s := newSandbox(t)
	s.installed("0.11.0")
	s.put("release/SHA256SUMS", "good  terraform-aws-dormeto-0.12.0.zip\n")
	s.request(`{"action":"plan","version":"0.12.0"}`)
	env := s.requestEnv(releases + "0.12.0")
	env["FAKE_CURL_FAIL"] = "28"
	out, err := s.run("manager.sh", env)
	want := "releases.dormeto.com cannot be reached: stopped"
	if err == nil || !strings.Contains(out, "dormeto: "+want+"\n") {
		t.Fatalf("err %v\n%s", err, out)
	}
	log := s.log()
	if !has(log, "curl -fsS --max-time 30 -o /dev/null "+releases+"0.12.0/SHA256SUMS") {
		t.Errorf("the published check:\n%s", strings.Join(log, "\n"))
	}
	if has(log, "aws s3api head-object --bucket dmt-install-123456789012-eu-west-1 --key prod/release/SHA256SUMS") {
		t.Error("a release that cannot be checked was taken for not published")
	}
	s.checkNoTerraform()
	s.checkResults(requestID, running, outcome("failed", want, "plan", "0.12.0"))
}
