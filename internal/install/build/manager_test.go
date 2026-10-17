package build

import (
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
	for _, k := range []string{"plans/dmt-prod-install:build-2/tfplan", "plans/dmt-prod-install:build-2/plan.json", "plans/dmt-prod-install:build-2/.terraform.lock.hcl", "config/.terraform.lock.hcl"} {
		if _, ok := s.uploaded(mb + k); !ok {
			t.Errorf("%s not uploaded\n%s", k, strings.Join(s.log(), "\n"))
		}
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
