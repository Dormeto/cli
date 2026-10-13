package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

func run(t *testing.T, b build, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	root := newRoot(b)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

func TestVersionWithLinkTimeValues(t *testing.T) {
	b := build{version: "0.1.0", commit: "abc1234", date: "2026-10-13T00:00:00Z",
		versions: []byte(`{"manager":"0.10.0","router":"0.29.0","commit":"deadbeef"}`)}
	want := "dormeto 0.1.0\ncommit: abc1234\ndate: 2026-10-13T00:00:00Z\nmanager: 0.10.0\nrouter: 0.29.0\n"
	if got := run(t, b, "version"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersionWithoutLinkTimeValuesUsesBuildInfo(t *testing.T) {
	b := build{
		info: &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}, Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "feedface"}, {Key: "vcs.time", Value: "2026-10-01T10:00:00Z"}}},
		versions: []byte(`{"manager":"","router":"","commit":""}`),
	}
	want := "dormeto 0.2.0\ncommit: feedface\ndate: 2026-10-01T10:00:00Z\nmanager: none\nrouter: none\n"
	if got := run(t, b, "version"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersionDevelopmentBuild(t *testing.T) {
	b := build{info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, versions: []byte(`{}`)}
	want := "dormeto dev\ncommit: unknown\ndate: unknown\nmanager: none\nrouter: none\n"
	if got := run(t, b, "version"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCompletionPrintsAScript(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		got := run(t, build{version: "0.1.0"}, "completion", shell)
		if !strings.Contains(got, "dormeto") || len(got) < 100 {
			t.Errorf("%s: no script: %q", shell, got)
		}
	}
	if got := run(t, build{}, "completion", "zsh"); !strings.Contains(got, "#compdef dormeto") {
		t.Errorf("zsh script lacks #compdef: %q", got)
	}
}

// F7: `dormeto --version` (and -v) print what `dormeto version` prints first: "dormeto <version>".
func TestVersionFlagPrintsTheFirstLineOfVersion(t *testing.T) {
	cases := []struct {
		b    build
		want string
	}{
		{build{version: "0.1.0", commit: "abc1234", versions: []byte(`{}`)}, "dormeto 0.1.0\n"},
		{build{info: &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}, versions: []byte(`{}`)}, "dormeto 0.2.0\n"},
		{build{info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, versions: []byte(`{}`)}, "dormeto dev\n"},
	}
	for _, c := range cases {
		for _, flag := range []string{"--version", "-v"} {
			got := run(t, c.b, flag)
			if got != c.want {
				t.Errorf("%s: %q, want %q", flag, got, c.want)
			}
			if first, _, _ := strings.Cut(run(t, c.b, "version"), "\n"); got != first+"\n" {
				t.Errorf("%s: %q, not the first line of version %q", flag, got, first)
			}
		}
	}
}
