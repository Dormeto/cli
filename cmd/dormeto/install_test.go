package main

// `dormeto install`, `install check` and `install apply`: the flags read into the run's request, the run each command
// calls, the exit codes, the usage; the real dependencies without a terminal.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/install"
)

// ran is what a fake run was given.
type ran struct {
	command string
	r       install.Request
	d       install.Deps
}

// installed runs `dormeto <args>` with fake runs that return code, and gives what they were called with.
func installed(t *testing.T, code int, args ...string) (calls []ran, stdout, stderr string, exit int) {
	t.Helper()
	fake := func(command string) installRun {
		return func(_ context.Context, d install.Deps, r install.Request) int {
			calls = append(calls, ran{command, r, d})
			return code
		}
	}
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	root.AddCommand(newInstall(installRuns{install: fake("install"), check: fake("check"), apply: fake("apply")}))
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	exit = execute(root)
	return calls, out.String(), errs.String(), exit
}

// only is the one call a command line made.
func only(t *testing.T, calls []ran) ran {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("%d calls: %+v", len(calls), calls)
	}
	return calls[0]
}

func TestInstallFlagsMakeTheRequest(t *testing.T) {
	calls, _, errs, exit := installed(t, 0, "install", "--profile", "p", "--region", "eu-west-1", "--env", "dev", "--version", "0.12.0",
		"--admin-emails", "a@x.io,b@y.io", "--mode", "account", "--ses-identity", "x.io", "--email-from", "d@x.io",
		"--build-role", "arn:aws:iam::532105290256:role/ci", "--release", "./release", "--yes", "--verbose",
		"--var", `alert_emails=["o@x.io"]`, "--var", "retention_days=30")
	if exit != 0 || errs != "" || len(calls) != 1 || calls[0].command != "install" {
		t.Fatalf("exit %d, errors %q, calls %+v", exit, errs, calls)
	}
	r := calls[0].r
	want := install.Request{
		Profile: "p", Region: "eu-west-1", Release: "./release", Yes: true, Verbose: true,
		Flags: install.Settings{Env: "dev", Region: "eu-west-1", Version: "0.12.0", Mode: "account", AdminEmails: []string{"a@x.io", "b@y.io"},
			SESIdentity: "x.io", EmailFrom: "d@x.io", BuildRole: "arn:aws:iam::532105290256:role/ci",
			Vars: map[string]json.RawMessage{"alert_emails": json.RawMessage(`["o@x.io"]`), "retention_days": json.RawMessage("30")}},
		Set: map[string]bool{"profile": true, "region": true, "env": true, "version": true, "admin-emails": true, "mode": true,
			"ses-identity": true, "email-from": true, "build-role": true, "release": true, "yes": true, "verbose": true, "var": true},
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("request\n%+v\nwant\n%+v", r, want)
	}
}

func TestInstallWithoutFlagsSetsNothing(t *testing.T) {
	calls, _, _, exit := installed(t, 0, "install")
	if exit != 0 || len(calls) != 1 {
		t.Fatalf("exit %d, calls %+v", exit, calls)
	}
	if r := calls[0].r; len(r.Set) != 0 || r.Flags.Domain != nil || r.Flags.Vars != nil || r.Flags.AdminEmails != nil {
		t.Errorf("request %+v", r)
	}
}

func TestInstallAdministratorsAreTrimmed(t *testing.T) {
	calls, _, _, _ := installed(t, 0, "install", "--admin-emails", "a@x.io, b@y.io,", "--admin-emails", " c@z.io")
	if got := only(t, calls).r.Flags.AdminEmails; !reflect.DeepEqual(got, []string{"a@x.io", "b@y.io", "c@z.io"}) {
		t.Errorf("administrators %q", got)
	}
}

func TestInstallDomainFlagsAreTheDomainKey(t *testing.T) {
	calls, _, _, _ := installed(t, 0, "install", "--domain", "dormeto.example.com", "--certificate-arn", "arn:aws:acm:us-east-1:1:certificate/x", "--zone-id", "Z1")
	r := only(t, calls).r
	if !reflect.DeepEqual(r.Set, map[string]bool{"domain": true}) {
		t.Errorf("set %v", r.Set)
	}
	if want := (&install.Domain{Dashboard: "dormeto.example.com", CertificateARN: "arn:aws:acm:us-east-1:1:certificate/x", ZoneID: "Z1"}); !reflect.DeepEqual(r.Flags.Domain, want) {
		t.Errorf("domain %+v", r.Flags.Domain)
	}
	// One of them alone is the domain too, which the settings' check then finds incomplete.
	calls, _, _, _ = installed(t, 0, "install", "--zone-id", "Z1")
	if r := only(t, calls).r; !r.Set["domain"] || r.Flags.Domain == nil || r.Flags.Domain.ZoneID != "Z1" {
		t.Errorf("request %+v", r)
	}
}

func TestInstallSubcommandsAndTheirFlags(t *testing.T) {
	for _, c := range []struct {
		args    []string
		command string
	}{
		{[]string{"install", "check", "--profile", "p"}, "check"},
		{[]string{"install", "--profile", "p", "check"}, "check"},
		{[]string{"install", "apply", "--profile", "p"}, "apply"},
		{[]string{"install", "--profile", "p"}, "install"},
	} {
		calls, _, errs, exit := installed(t, 0, c.args...)
		if exit != 0 || errs != "" || len(calls) != 1 || calls[0].command != c.command || calls[0].r.Profile != "p" || !calls[0].r.Set["profile"] {
			t.Errorf("%q: exit %d, errors %q, calls %+v", c.args, exit, errs, calls)
		}
	}
}

func TestInstallExitCodeIsTheRuns(t *testing.T) {
	for _, code := range []int{1, 2, 130} {
		_, out, errs, exit := installed(t, code, "install", "apply")
		if exit != code || out != "" || errs != "" {
			t.Errorf("run's %d: exit %d, out %q, errors %q", code, exit, out, errs)
		}
	}
}

func TestInstallBadFlagsExitCode2(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"install", "--var", "nonsense"}, `--var: "nonsense" is not name=value`},
		{[]string{"install", "--var", "=1"}, `--var: "=1" is not name=value`},
		{[]string{"install", "check", "--nope"}, "unknown flag: --nope"},
		{[]string{"install", "--yes=maybe"}, `invalid argument "maybe" for "--yes"`},
		{[]string{"install", "apply", "extra"}, `unexpected argument "extra"`},
	} {
		calls, out, errs, exit := installed(t, 0, c.args...)
		if exit != 2 || len(calls) != 0 || out != "" || !strings.Contains(errs, c.want) {
			t.Errorf("%q: exit %d, calls %d, out %q, errors %q", c.args, exit, len(calls), out, errs)
		}
	}
}

func TestInstallHelpListsTheFlags(t *testing.T) {
	_, out, _, exit := installed(t, 0, "install", "--help")
	if exit != 0 || !strings.Contains(out, "Install the Dormeto manager in an AWS account") {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	for _, f := range []string{"--profile", "--region", "--env", "--version", "--admin-emails", "--mode", "--domain", "--certificate-arn",
		"--zone-id", "--ses-identity", "--email-from", "--var", "--build-role", "--release", "--yes", "--verbose", "check", "apply"} {
		if !strings.Contains(out, f) {
			t.Errorf("the usage lacks %s:\n%s", f, out)
		}
	}
	if out, _, _ := executed(t, build{}, "--help"); !strings.Contains(out, "install     Install the Dormeto manager in an AWS account, with CodeBuild\n") {
		t.Errorf("the commands listed without install:\n%s", out)
	}
}

func TestInstallRealDependenciesWithoutATerminal(t *testing.T) {
	calls, _, _, _ := installed(t, 0, "install")
	d := only(t, calls).d
	if d.Terminal || d.Status != nil || d.Out == nil || d.Clients == nil || d.Region == nil || d.Now == nil || d.Every == nil {
		t.Errorf("dependencies %+v", d)
	}
	if h, ok := d.HTTP.(*http.Client); !ok || h.Timeout != 30*time.Second {
		t.Errorf("HTTP %#v", d.HTTP)
	}
	if _, err := d.Prompter.Ask("Region", "eu-west-1"); err != install.ErrNoTerminal {
		t.Errorf("Ask without a terminal: %v", err)
	}
}

// stdin from /dev/null (a Kubernetes Job, systemd, docker run without -i) is a character device, not a terminal to ask.
func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("/dev/null is taken for a terminal")
	}
}
