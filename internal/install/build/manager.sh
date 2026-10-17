#!/bin/sh
# Dormeto install: Terraform with the manager release, run by CodeBuild (buildspec.yml).
# Inputs: ACTION (plan | plan-destroy | apply | apply-destroy), BUCKET, ENV, TF_VERSION, RELEASE_BASE (https://... or
# s3://...), RELEASE_VERSION, PLAN_BUILD (apply actions), CODEBUILD_BUILD_ID (set by CodeBuild).
set -eu

say() { echo "dormeto: $*"; }
die() { echo "dormeto: $*" >&2; exit 1; }
need() { eval "[ -n \"\${$1:-}\" ]" || die "$1 is not set"; }

for v in ACTION BUCKET ENV TF_VERSION RELEASE_BASE RELEASE_VERSION CODEBUILD_BUILD_ID; do need "$v"; done
case $ACTION in
plan | plan-destroy) ;;
apply | apply-destroy) need PLAN_BUILD ;;
*) die "unknown ACTION $ACTION" ;;
esac

S3="s3://$BUCKET/$ENV"

# verify SUMS FILE: FILE's line of SUMS must match its checksum.
verify() {
  grep "  $2\$" "$1" >"$2.sum" || die "no checksum for $2 in $1: stopped"
  sha256sum -c "$2.sum" || die "the checksum of $2 does not match: stopped"
}

# fetch FILE: from the release base (https or s3), into the current directory.
fetch() {
  case $RELEASE_BASE in
  s3://*) aws s3 cp --only-show-errors "$RELEASE_BASE/$1" "$1" ;;
  *) curl -fsSL -o "$1" "$RELEASE_BASE/$1" ;;
  esac
}

say "downloading Terraform $TF_VERSION and checking it"
TF_ZIP="terraform_${TF_VERSION}_linux_amd64.zip"
TF_URL="https://releases.hashicorp.com/terraform/$TF_VERSION"
curl -fsSL -o "terraform_${TF_VERSION}_SHA256SUMS" "$TF_URL/terraform_${TF_VERSION}_SHA256SUMS"
curl -fsSL -o "$TF_ZIP" "$TF_URL/$TF_ZIP"
verify "terraform_${TF_VERSION}_SHA256SUMS" "$TF_ZIP"
unzip -q -o "$TF_ZIP" -d ./bin
PATH="$PWD/bin:$PATH"
# no call to HashiCorp's checkpoint service, no colors in the log the CLI follows
CHECKPOINT_DISABLE=1
TF_IN_AUTOMATION=1
TF_CLI_ARGS=-no-color
export PATH CHECKPOINT_DISABLE TF_IN_AUTOMATION TF_CLI_ARGS

say "reading the configuration of $ENV"
aws s3 cp --only-show-errors "$S3/config/main.tf.json" main.tf.json
case $ACTION in
apply*) aws s3 cp --only-show-errors "$S3/plans/$PLAN_BUILD/.terraform.lock.hcl" .terraform.lock.hcl ;;
*) # a kept lock keeps the providers: only a 404 means there is none, any other error stops
  if aws s3api head-object --bucket "$BUCKET" --key "$ENV/config/.terraform.lock.hcl" >/dev/null 2>lock.err; then
    aws s3 cp --only-show-errors "$S3/config/.terraform.lock.hcl" .terraform.lock.hcl
  elif ! grep -q -e '(404)' -e 'Not Found' lock.err; then
    cat lock.err >&2
    die "cannot check the lock file of the providers: stopped"
  fi ;;
esac

say "downloading the manager release $RELEASE_VERSION and checking it"
fetch SHA256SUMS
fetch "terraform-aws-dormeto-$RELEASE_VERSION.zip"
verify SHA256SUMS "terraform-aws-dormeto-$RELEASE_VERSION.zip"
unzip -q -o "terraform-aws-dormeto-$RELEASE_VERSION.zip" -d ./release

say "terraform init"
terraform init -input=false

case $ACTION in
plan | plan-destroy)
  say "terraform $ACTION"
  if [ "$ACTION" = plan-destroy ]; then
    terraform plan -input=false -destroy -out=tfplan
  else
    terraform plan -input=false -out=tfplan
  fi
  terraform show -json tfplan >plan.json
  say "keeping the plan as build $CODEBUILD_BUILD_ID"
  for f in tfplan plan.json .terraform.lock.hcl; do
    aws s3 cp --only-show-errors "$f" "$S3/plans/$CODEBUILD_BUILD_ID/$f"
  done
  aws s3 cp --only-show-errors .terraform.lock.hcl "$S3/config/.terraform.lock.hcl"
  ;;
apply | apply-destroy)
  say "applying the plan of build $PLAN_BUILD"
  aws s3 cp --only-show-errors "$S3/plans/$PLAN_BUILD/tfplan" tfplan
  # a saved plan needs no approval flag: it is the plan that was read
  terraform apply -input=false tfplan
  if [ "$ACTION" = apply ]; then
    say "recording the outputs and the applied version"
    terraform output -json >outputs.json
    aws s3 cp --only-show-errors outputs.json "$S3/outputs.json"
    printf '{"version":"%s","build":"%s"}\n' "$RELEASE_VERSION" "$CODEBUILD_BUILD_ID" >applied.json
    aws s3 cp --only-show-errors applied.json "$S3/applied.json"
  else
    say "removing the applied version and the outputs"
    aws s3 rm --only-show-errors "$S3/applied.json"
    aws s3 rm --only-show-errors "$S3/outputs.json"
  fi
  ;;
esac
say "done"
