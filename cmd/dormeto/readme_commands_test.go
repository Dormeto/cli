package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every command a README code block shows, and the hidden old names its text says go on working, exists in the tree.
func TestReadmeCommandsExistInTheTree(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	root := newRoot(build{})
	word := regexp.MustCompile(`^[a-z][a-z-]*$`)
	seen := 0
	for _, block := range regexp.MustCompile("(?s)```[a-z]*\n(.*?)```").FindAllStringSubmatch(string(b), -1) {
		for _, line := range strings.Split(block[1], "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[0] != "dormeto" || (fields[1] != "aws" && fields[1] != "gcp") {
				continue
			}
			cur := root
			for _, w := range fields[1:] {
				if !word.MatchString(w) || len(cur.Commands()) == 0 {
					break
				}
				var next *cobra.Command
				for _, c := range cur.Commands() {
					if c.Name() == w {
						next = c
					}
				}
				if next == nil {
					t.Errorf("README.md shows %q: %q is no command of %q", line, w, cur.CommandPath())
					break
				}
				cur = next
			}
			seen++
		}
	}
	if seen < 50 {
		t.Fatalf("only %d command lines read", seen)
	}
	// the names the README says are the new ones, and the hidden old ones that go on working
	for _, path := range [][]string{
		{"gcp", "routers", "install"}, {"gcp", "routers", "uninstall"}, {"gcp", "routers", "show"}, {"gcp", "routers", "settings"},
		{"gcp", "routers", "pause"}, {"gcp", "routers", "resume"}, {"gcp", "routers", "waiting-page"},
		{"gcp", "install"}, {"gcp", "uninstall"}, {"gcp", "status"},
		{"gcp", "workloads", "wake"}, {"gcp", "workloads", "pause"}, {"gcp", "workloads", "list"}, {"gcp", "workloads", "offboard"},
		{"aws", "routers", "settings"}, {"aws", "types", "set"}, {"aws", "settings", "set"}, {"aws", "workloads", "settings"},
		{"aws", "install", "check"}, {"aws", "install", "status"}, {"aws", "workloads", "idle-window"},
	} {
		if c, _, err := root.Find(path); err != nil || c.CommandPath() != "dormeto "+strings.Join(path, " ") {
			t.Errorf("no command %v in the tree (%v)", path, err)
		}
	}
	for _, old := range [][]string{{"gcp", "install"}, {"gcp", "uninstall"}, {"gcp", "status"}} {
		if c, _, _ := root.Find(old); c == nil || !c.Hidden {
			t.Errorf("%v is not a hidden command", old)
		}
	}
	// the commands without --dry-run, which the README names, and the commands that have it
	for path, want := range map[string]bool{"aws install": false, "aws uninstall": false, "aws routers pause": true, "gcp workloads nap": true, "gcp workloads offboard": true} {
		c, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Flags().Lookup("dry-run") != nil; got != want {
			t.Errorf("%s has --dry-run: %v, the README says %v", path, got, want)
		}
	}
}
