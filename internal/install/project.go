package install

import "strings"

// Project is the install's own AWS resources, named from the account, the region and the env.
type Project struct {
	Account, Region, Env string
	Bucket               string // dmt-install-<account>-<region>
	Name                 string // dmt-<env>-install (the CodeBuild project)
	Role                 string // dmt-<env>-install (its role)
	LogGroup             string // /aws/codebuild/dmt-<env>-install
}

// Names is the project of an account, a region and an env.
func Names(account, region, env string) Project {
	name := "dmt-" + env + "-install"
	return Project{
		Account: account, Region: region, Env: env,
		Bucket:   "dmt-install-" + account + "-" + region,
		Name:     name,
		Role:     name,
		LogGroup: "/aws/codebuild/" + name,
	}
}

// Key is an S3 key in the install's bucket: <env>/ then the parts joined by "/".
func (p Project) Key(parts ...string) string {
	return p.Env + "/" + strings.Join(parts, "/")
}

// StateKey is the key of the manager's Terraform state.
func (p Project) StateKey() string { return p.Key("manager.tfstate") }

// Tags are the tags of every resource the install creates.
func (p Project) Tags() map[string]string {
	return map[string]string{"project": "dormeto", "dormeto:env": p.Env, "managed-by": "dormeto-cli"}
}
