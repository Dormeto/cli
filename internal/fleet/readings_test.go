package fleet

import "testing"

func TestShortARN(t *testing.T) {
	for in, want := range map[string]string{
		"arn:aws:sts::1:assumed-role/Ci/run-42":            "Ci/run-42",
		"arn:aws:sts::1:assumed-role/Path/To/Role/session": "Role/session",
		"arn:aws:iam::1:user/bob":                          "bob",
		"arn:aws:iam::1:root":                              "arn:aws:iam::1:root",
		"ana@example.com":                                  "ana@example.com",
		"":                                                 "",
	} {
		if got := ShortARN(in); got != want {
			t.Errorf("ShortARN(%q) = %q, want %q", in, got, want)
		}
	}
}
