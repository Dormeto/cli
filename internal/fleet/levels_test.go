package fleet

import "testing"

func TestSourceTextIsAwsWordsAndFixedOnGoogleCloud(t *testing.T) {
	name := "batch"
	for level, want := range map[string]string{"workload": "set on this workload", "default": "built-in", "router": "router", "fixed": "fixed on Google Cloud"} {
		if got := SourceText(level, nil); got != want {
			t.Errorf("%s: %q", level, got)
		}
	}
	if got := SourceText("type", &name); got != "type batch" {
		t.Errorf("%q", got)
	}
	if got := SourceText("type", nil); got != "its type" {
		t.Errorf("%q", got)
	}
}
