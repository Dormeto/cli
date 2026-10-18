package install

// --adopt-state (adopt.go): the manager module's part of a state moved to module.dormeto, the state's checks, and a first
// install that takes it over through run_test.go's fakes: what it writes and when, its refusals, the detach message
// after its first apply, and the uninstall refused before it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/install/build"
)

const adFixture = "testdata/adopt/manager.tfstate"

// adProject is the install of rwWorld: prod in rwAccount, eu-west-1.
var adProject = Names(rwAccount, "eu-west-1", "prod")

// adLeft are the fixture's resources outside module.manager.
var adLeft = []string{"aws_acm_certificate.edge", "aws_acm_certificate_validation.edge", "aws_route53_record.certificate_validation"}

func adSource(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(adFixture)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// adDecode is a state's JSON as values, its numbers kept as written.
func adDecode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v map[string]any
	if err := d.Decode(&v); err != nil {
		t.Fatalf("%v:\n%s", err, b)
	}
	return v
}

// adWithout is resource r without its instances' dependencies, and its module replaced by module.
func adWithout(r map[string]any, module string) map[string]any {
	out := map[string]any{}
	for k, v := range r {
		out[k] = v
	}
	if module != "" {
		out["module"] = module
	}
	var instances []any
	for _, i := range r["instances"].([]any) {
		c := map[string]any{}
		for k, v := range i.(map[string]any) {
			if k != "dependencies" {
				c[k] = v
			}
		}
		instances = append(instances, c)
	}
	out["instances"] = instances
	return out
}

// adDeps are the dependencies of a resource's first instance.
func adDeps(r map[string]any) []string {
	var out []string
	for _, d := range r["instances"].([]any)[0].(map[string]any)["dependencies"].([]any) {
		out = append(out, d.(string))
	}
	return out
}

// The manager module's resources, those of its nested modules too, move to module.dormeto, unchanged but their module
// and their dependencies; the resources outside it are left and listed; the serial goes up, the lineage and the
// Terraform version stay; outputs and check results go.
func TestMoveStateTakesTheManagerModule(t *testing.T) {
	src := adSource(t)
	m, err := MoveState(src, "manager", adProject, build.TerraformVersion)
	if err != nil {
		t.Fatal(err)
	}
	rwEqual(t, "left", m.Left, adLeft)
	rwEqual(t, "resources", m.Resources, 9)
	rwEqual(t, "terraform", m.Terraform, "1.16.2")

	got, was := adDecode(t, m.State), adDecode(t, src)
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range got {
			if !yield(k) {
				return
			}
		}
	})
	rwEqual(t, "keys", keys, []string{"check_results", "lineage", "outputs", "resources", "serial", "terraform_version", "version"})
	rwEqual(t, "version", got["version"], json.Number("4"))
	rwEqual(t, "terraform_version", got["terraform_version"], "1.16.2")
	rwEqual(t, "serial", got["serial"], json.Number("318"))
	rwEqual(t, "lineage", got["lineage"], was["lineage"])
	rwEqual(t, "outputs", got["outputs"], map[string]any{})
	rwEqual(t, "check_results", got["check_results"], nil)

	modules := map[string]string{
		"module.manager": "module.dormeto",
		"module.manager.module.organization_roles[0]":                        "module.dormeto.module.organization_roles[0]",
		"module.manager.module.organization_roles[0].module.command_role[0]": "module.dormeto.module.organization_roles[0].module.command_role[0]",
	}
	var moved []any
	for _, r := range was["resources"].([]any) {
		if mod, ok := r.(map[string]any)["module"].(string); ok {
			moved = append(moved, adWithout(r.(map[string]any), modules[mod]))
		}
	}
	res := got["resources"].([]any)
	if len(res) != len(moved) {
		t.Fatalf("%d resources, want %d", len(res), len(moved))
	}
	for i, r := range res {
		rwEqual(t, "resource "+r.(map[string]any)["name"].(string), adWithout(r.(map[string]any), ""), moved[i])
	}
	byName := func(module, typ, name string) map[string]any {
		for _, r := range res {
			r := r.(map[string]any)
			if r["module"] == module && r["type"] == typ && r["name"] == name {
				return r
			}
		}
		t.Fatalf("no %s.%s.%s", module, typ, name)
		return nil
	}
	rwEqual(t, "the function's dependencies", adDeps(byName("module.dormeto", "aws_lambda_function", "api")), []string{
		"aws_acm_certificate_validation.edge",
		"module.dormeto.aws_dynamodb_table.manager",
		"module.dormeto.aws_iam_role.api",
		"module.dormeto.data.aws_caller_identity.current",
		"module.dormeto.module.organization_roles.aws_iam_role.discovery",
	})
	rwEqual(t, "the command role's dependencies", adDeps(byName("module.dormeto.module.organization_roles[0].module.command_role[0]", "aws_iam_role", "this")), []string{
		"module.dormeto.module.organization_roles.aws_iam_policy.boundary",
		"module.dormeto.module.organization_roles.module.command_role.data.aws_partition.current",
	})
	permissions := byName("module.dormeto", "aws_lambda_permission", "routers")["instances"].([]any)
	if len(permissions) != 2 || permissions[1].(map[string]any)["index_key"] != "555555555555" ||
		!slices.Equal(adDeps(map[string]any{"instances": permissions[1:]}), []string{"module.dormeto.aws_lambda_function.api"}) {
		t.Errorf("the permissions' instances: %v", permissions)
	}
	// The values as written: no HTML escaping, the digits of a number kept.
	for _, s := range []string{`<b>&c`, `12345678901234567890`} {
		if !strings.Contains(string(m.State), s) {
			t.Errorf("%s changed in:\n%s", s, m.State)
		}
	}
	if !strings.HasSuffix(string(m.State), "}\n") {
		t.Errorf("no final newline")
	}
}

// adState is a state of one resource, the manager's API function (module its module path: "" none).
func adState(version int, terraform, module, typ, function, arn string) []byte {
	r := map[string]any{"mode": "managed", "type": typ, "name": "api", "provider": `provider["registry.terraform.io/hashicorp/aws"]`,
		"instances": []any{map[string]any{"schema_version": 0, "attributes": map[string]any{"function_name": function, "arn": arn}}}}
	if module != "" {
		r["module"] = module
	}
	b, _ := json.Marshal(map[string]any{"version": version, "terraform_version": terraform, "serial": 1, "lineage": "l", "outputs": map[string]any{},
		"resources": []any{r}})
	return b
}

const (
	adFunction = "dmt-prod-manager-api"
	adARN      = "arn:aws:lambda:eu-west-1:" + rwAccount + ":function:dmt-prod-manager-api"
)

// A state is adopted only when it is of format 4, written by a Terraform no newer than the build's, and holds the
// module's API function of this env, in the profile's account and region.
func TestMoveStateRefusals(t *testing.T) {
	for _, c := range []struct {
		name, module string
		state        []byte
		want         string
	}{
		{"not JSON", "manager", []byte("{"), "not a Terraform state"},
		{"format 3", "manager", adState(3, "0.11.14", "module.manager", "aws_lambda_function", adFunction, adARN), "state format version 3, not 4"},
		{"a newer Terraform", "manager", adState(4, "1.17.0", "module.manager", "aws_lambda_function", adFunction, adARN),
			"written by Terraform 1.17.0, newer than the install's " + build.TerraformVersion},
		{"no Terraform version", "manager", adState(4, "", "module.manager", "aws_lambda_function", adFunction, adARN), `terraform_version "" is not a version`},
		{"another module", "manager", adState(4, "1.16.2", "module.platform", "aws_lambda_function", adFunction, adARN),
			"no module.manager in the state (its modules: module.platform)"},
		{"no module", "manager", adState(4, "1.16.2", "", "aws_lambda_function", adFunction, adARN), "no module.manager in the state (its modules: none)"},
		{"no function", "manager", adState(4, "1.16.2", "module.manager", "aws_lambda_alias", adFunction, adARN),
			"module.manager has no aws_lambda_function.api: not a manager's state"},
		{"another env", "manager", adState(4, "1.16.2", "module.manager", "aws_lambda_function", "dmt-dev-manager-api",
			"arn:aws:lambda:eu-west-1:"+rwAccount+":function:dmt-dev-manager-api"),
			"module.manager's aws_lambda_function.api is dmt-dev-manager-api, not env prod's dmt-prod-manager-api"},
		{"another account", "manager", adState(4, "1.16.2", "module.manager", "aws_lambda_function", adFunction,
			"arn:aws:lambda:eu-west-1:111111111111:function:dmt-prod-manager-api"),
			"dmt-prod-manager-api is in 111111111111 eu-west-1, not in the profile's " + rwAccount + " eu-west-1"},
		{"another region", "manager", adState(4, "1.16.2", "module.manager", "aws_lambda_function", adFunction,
			"arn:aws:lambda:us-east-1:"+rwAccount+":function:dmt-prod-manager-api"),
			"dmt-prod-manager-api is in " + rwAccount + " us-east-1, not in the profile's " + rwAccount + " eu-west-1"},
		{"no ARN", "manager", adState(4, "1.16.2", "module.manager", "aws_lambda_function", adFunction, ""),
			`dmt-prod-manager-api's ARN "" is not a Lambda function's`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := MoveState(c.state, c.module, adProject, build.TerraformVersion)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want %q", err, c.want)
			}
		})
	}
}

// --adopt-module names the module; a module whose name only begins with it is not it, and is left.
func TestMoveStateOfAnotherModuleName(t *testing.T) {
	var state map[string]any
	if err := json.Unmarshal(adState(4, "1.16.2", "module.platform", "aws_lambda_function", adFunction, adARN), &state); err != nil {
		t.Fatal(err)
	}
	other := map[string]any{"module": "module.platform2", "mode": "managed", "type": "aws_s3_bucket", "name": "x", "instances": []any{}}
	data := map[string]any{"mode": "data", "type": "aws_caller_identity", "name": "current", "instances": []any{}}
	state["resources"] = append(state["resources"].([]any), other, data)
	b, _ := json.Marshal(state)
	m, err := MoveState(b, "platform", adProject, build.TerraformVersion)
	if err != nil {
		t.Fatal(err)
	}
	rwEqual(t, "left", m.Left, []string{"module.platform2.aws_s3_bucket.x", "data.aws_caller_identity.current"})
	if r := adDecode(t, m.State)["resources"].([]any); len(r) != 1 || r[0].(map[string]any)["module"] != "module.dormeto" {
		t.Errorf("resources %v", r)
	}
}

// adRequest is rwFlags adopting the fixture.
func adRequest() Request {
	r := rwFlags()
	r.AdoptState = adFixture
	return r
}

// adWorld is rwWorld with the manager the fixture holds in AWS: its API function found.
func adWorld(t *testing.T) *rwWorld {
	w := rwNew(t)
	w.found = map[string]bool{"dmt-prod-manager-api": true}
	return w
}

const adBackup = "prod/adopted/20261017T120000Z.tfstate"

// An adoption without a terminal: the state checked with the others, the another install check passing; after the
// bootstrap, the original kept, the moved state written only where none is, install.json naming where it came from; then
// the plan.
func TestInstallAdoptsAState(t *testing.T) {
	w := adWorld(t)
	code, out, _ := w.run(Install, adRequest(), false)
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out,
		"  ✓ existing install: none: a first install of env prod\n",
		"  ✓ adopted state: module.manager of manager.tfstate (Terraform 1.16.2): 9 resources to module.dormeto; left in the source: "+
			strings.Join(adLeft, ", ")+"\n",
		"  ✓ another install: dmt-prod-manager: the manager adopted\n",
		"\nBootstrap (API calls): ",
		"Adopted: module.manager of manager.tfstate is the install's state, s3://dmt-install-532105290256-eu-west-1/prod/manager.tfstate; "+
			"the original is kept as s3://dmt-install-532105290256-eu-west-1/"+adBackup+"\n",
		"Plan (build dmt-prod-install:b1)\n",
		"Plan saved. Apply it: dormeto install apply "+rwArgs+"\n",
	)
	src := adSource(t)
	if w.objects[adBackup] != string(src) {
		t.Errorf("the original kept: %q", w.objects[adBackup])
	}
	moved, err := MoveState(src, "manager", adProject, build.TerraformVersion)
	if err != nil {
		t.Fatal(err)
	}
	if w.objects["prod/manager.tfstate"] != string(moved.State) {
		t.Errorf("the install's state:\n%s", w.objects["prod/manager.tfstate"])
	}
	rwEqual(t, "conditional puts", w.conditional, []string{"prod/manager.tfstate"})
	sum := sha256.Sum256(src)
	s := w.saved()
	rwEqual(t, "adoptedFrom", s.AdoptedFrom, &AdoptedFrom{File: "manager.tfstate", SHA256: hex.EncodeToString(sum[:])})
	rwEqual(t, "adoptedModule", s.AdoptedModule, "manager")
	if !strings.Contains(w.objects["prod/install.json"], `"adoptedFrom": {`) || !strings.Contains(w.objects["prod/install.json"], `"adoptedModule": "manager"`) {
		t.Errorf("install.json:\n%s", w.objects["prod/install.json"])
	}
	at := func(call string) int {
		i := slices.Index(w.writes, call)
		if i < 0 {
			t.Fatalf("no %s in %v", call, w.writes)
		}
		return i
	}
	order := []string{"codebuild:CreateProject", "s3:PutObject " + adBackup, "s3:PutObject prod/manager.tfstate", "s3:PutObject prod/install.json",
		"s3:PutObject prod/config/main.tf.json", "codebuild:StartBuild plan"}
	for i := 1; i < len(order); i++ {
		if at(order[i-1]) > at(order[i]) {
			t.Errorf("%s before %s: %v", order[i], order[i-1], w.writes)
		}
	}
}

// Without --adopt-state, the manager found is another install's, as before.
func TestInstallWithoutAdoptionRefusesTheManagerFound(t *testing.T) {
	w := adWorld(t)
	code, out, _ := w.run(Install, rwFlags(), false)
	if code != 1 || !strings.Contains(out, "  ✗ another install: dmt-prod-manager resources exist") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// --adopt-state is for a first install: an env with install.json refuses it before anything is written.
func TestInstallAdoptRefusedOnceInstalled(t *testing.T) {
	w := adWorld(t)
	w.installed("0.11.0")
	code, out, _ := w.run(Install, adRequest(), false)
	if code != 1 || !strings.HasSuffix(out, "env prod is already installed with the CLI: --adopt-state is for a first install\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// adFile writes a state to a file of its own and gives its path.
func adFile(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "other.tfstate")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A state that fails its check stops the install with the other checks shown, before anything is written.
func TestInstallAdoptAStateOfAnotherEnv(t *testing.T) {
	w := adWorld(t)
	r := adRequest()
	r.AdoptState = adFile(t, adState(4, "1.16.2", "module.manager", "aws_lambda_function", "dmt-dev-manager-api",
		"arn:aws:lambda:eu-west-1:"+rwAccount+":function:dmt-dev-manager-api"))
	code, out, _ := w.run(Install, r, false)
	rwInOrder(t, out, "  ✗ adopted state: other.tfstate: module.manager's aws_lambda_function.api is dmt-dev-manager-api, not env prod's dmt-prod-manager-api\n",
		"  ✗ another install: dmt-prod-manager resources exist", "Nothing was created.\n")
	if code != 1 || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// The flags of an adoption, refused before anything is read in AWS: --adopt-module alone, a module name that is not
// one, a file that cannot be read, --access.
func TestInstallAdoptFlagsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(r *Request)
		want string
	}{
		{"module alone", func(r *Request) { r.AdoptState, r.AdoptModule = "", "platform" }, "--adopt-module: only with --adopt-state"},
		{"bad module", func(r *Request) { r.AdoptModule = "a.b" }, `--adopt-module: "a.b" is not a module name`},
		{"no file", func(r *Request) { r.AdoptState = "testdata/adopt/none.tfstate" }, "--adopt-state: open testdata/adopt/none.tfstate: no such file or directory"},
		{"access", func(r *Request) { r.Access = true }, "--adopt-state: not with --access"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := adWorld(t)
			w.identityErr = errors.New("AWS read") // a run that reaches AWS says it
			r := adRequest()
			c.edit(&r)
			code, out, _ := w.run(Install, r, false)
			if code != 2 || out != c.want+"\n" || len(w.writes) != 0 {
				t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
			}
		})
	}
}

// A state already there is not replaced: the adoption stops after the original is kept; the same state (an adoption
// that stopped after writing it) goes on.
func TestInstallAdoptAStateAlreadyThere(t *testing.T) {
	w := adWorld(t)
	w.bucket = true
	w.objects["prod/manager.tfstate"] = rwState
	code, out, _ := w.run(Install, adRequest(), false)
	if code != 1 || !strings.HasSuffix(out, "env prod has a Terraform state already (s3://dmt-install-532105290256-eu-west-1/prod/manager.tfstate): "+
		"--adopt-state is for a first install; nothing was adopted\n") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
	if w.objects["prod/manager.tfstate"] != rwState || slices.Contains(w.writes, "s3:PutObject prod/install.json") || slices.Contains(w.actions(), "plan") {
		t.Errorf("writes %v", w.writes)
	}

	w = adWorld(t)
	w.bucket = true
	moved, err := MoveState(adSource(t), "manager", adProject, build.TerraformVersion)
	if err != nil {
		t.Fatal(err)
	}
	w.objects["prod/manager.tfstate"] = string(moved.State)
	if code, out, _ := w.run(Install, adRequest(), false); code != 0 || w.saved().AdoptedFrom == nil {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// The first apply of an adopted manager says how to detach it from the configuration that installed it; a later one
// does not.
func TestInstallAdoptedFirstApplySaysHowToDetach(t *testing.T) {
	const detach = "The manager is now this install's. Detach it from the configuration that installed it: terraform state rm module.manager there, " +
		"then remove the module from that configuration.\n"
	w := adWorld(t)
	r := adRequest()
	r.AdoptModule = "manager"
	code, out, _ := w.run(Install, r, true, "y")
	if code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	rwInOrder(t, out, "  ✓ applied: 3 added, 0 changed, 0 destroyed\n", detach, "Next: sign in at ")

	w.release("0.12.0", true)
	r = rwFlags()
	r.Flags.Version = "0.12.0"
	code, out, _ = w.run(Install, r, true, "y")
	if code != 0 || !strings.Contains(out, "  ✓ applied: ") || strings.Contains(out, "Detach it") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// install apply of an adoption's plan says it too.
func TestApplyAdoptedSaysHowToDetach(t *testing.T) {
	w := adWorld(t)
	r := adRequest()
	r.AdoptState, r.AdoptModule = adFile(t, bytes.ReplaceAll(adSource(t), []byte(`"module.manager`), []byte(`"module.platform`))), "platform"
	if code, out, _ := w.run(Install, r, false); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	rwInOrder(t, out, "  ✓ applied: 3 added, 0 changed, 0 destroyed\n",
		"The manager is now this install's. Detach it from the configuration that installed it: terraform state rm module.platform there, "+
			"then remove the module from that configuration.\n")
	if code != 0 {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}

// An adoption not applied yet: uninstall (and uninstall apply) refuse, saying how to abandon the adoption instead; once
// applied, uninstall goes on.
func TestUninstallRefusesAnAdoptionNotApplied(t *testing.T) {
	w := adWorld(t)
	if code, out, _ := w.run(Install, adRequest(), false); code != 0 {
		t.Fatalf("exit code %d:\n%s", code, out)
	}
	const refused = "env prod's manager is adopted (from manager.tfstate) and nothing is applied yet: an uninstall would destroy the adopted manager. " +
		"To abandon the adoption instead, remove prod/install.json and prod/manager.tfstate from the bucket dmt-install-532105290256-eu-west-1: " +
		"the source state is untouched, and the bootstrap can stay or be removed by hand.\n"
	for _, cmd := range []struct {
		name string
		run  func(w *rwWorld) (int, string)
	}{
		{"uninstall", func(w *rwWorld) (int, string) { c, o, _ := w.run(Uninstall, unRequest, true); return c, o }},
		{"uninstall apply", func(w *rwWorld) (int, string) { c, o, _ := w.run(UninstallApply, unRequest, false); return c, o }},
	} {
		w.writes = nil
		code, out := cmd.run(w)
		if code != 1 || !strings.HasSuffix(out, refused) || len(w.writes) != 0 {
			t.Errorf("%s: exit code %d, writes %v:\n%s", cmd.name, code, w.writes, out)
		}
	}

	if code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false); code != 0 {
		t.Fatalf("apply: exit code %d:\n%s", code, out)
	}
	w.plan = unProtection
	code, out, _ := w.run(Uninstall, unRequest, false)
	if code != 0 || strings.Contains(out, "is adopted") || !strings.Contains(out, "Plan saved. Apply it: dormeto install apply") {
		t.Errorf("exit code %d:\n%s", code, out)
	}
}
