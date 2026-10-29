package access

import (
	"bytes"
	"os"
	"testing"
)

// The embedded copy equals the router repository's manifest (a sibling checkout).
func TestEmbeddedCopyEqualsRouters(t *testing.T) {
	want, err := os.ReadFile("../../../../router/gcp/access.json")
	if err != nil {
		t.Skipf("the router repository is absent: %v", err)
	}
	if !bytes.Equal(want, raw) {
		t.Fatal("internal/gcp/access/access.json differs from router/gcp/access.json: copy it byte for byte")
	}
}
