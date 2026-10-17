// Package build holds what CodeBuild runs for the install: a one-line buildspec and two POSIX shell scripts, kept in
// the binary and uploaded to the install's bucket (Terraform with the manager release; the organization's access).
package build

import "embed"

// Files are the buildspec and the scripts.
//
//go:embed buildspec.yml manager.sh organization.sh
var Files embed.FS

// TerraformVersion is the Terraform CodeBuild downloads (and checks against HashiCorp's SHA256SUMS).
const TerraformVersion = "1.16.5"

// Buildspec is the project's buildspec: it fetches the script named by SCRIPT from the bucket and runs it.
func Buildspec() string { return string(read("buildspec.yml")) }

// Script is "manager.sh" or "organization.sh".
func Script(name string) []byte { return read(name) }

func read(name string) []byte {
	b, err := Files.ReadFile(name)
	if err != nil {
		panic("build: " + err.Error())
	}
	return b
}
