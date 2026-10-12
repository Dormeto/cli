// Package cli holds the files embedded from the module root.
package cli

import _ "embed"

// Versions is testdata/parity/versions.json: the manager and router versions
// and the manager commit of the shared parity cases, written by the export.
//
//go:embed testdata/parity/versions.json
var Versions []byte
