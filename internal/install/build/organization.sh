#!/bin/sh
# Dormeto install: the organization's access, run by CodeBuild in the management account (buildspec.yml).
# Inputs: BUCKET, ENV, MANAGER_ACCOUNT, ORGANIZATION, MEMBER_ROLE (a path may precede the name), DISCOVERY_ROLE and
# COMMAND_ROLE (the roles' names the manager is given, a path may precede them; the release script's defaults
# otherwise), RELEASE_BASE, RELEASE_VERSION, CODEBUILD_BUILD_ID. The plan, in the bucket under $ENV/organization/:
# policy.json, policy-base.json, accounts.txt and discovery-only.txt (one account id per line).
set -eu

say() { echo "dormeto: $*"; }
die() { echo "dormeto: $*" >&2; exit 1; }
need() { eval "[ -n \"\${$1:-}\" ]" || die "$1 is not set"; }

for v in BUCKET ENV MANAGER_ACCOUNT ORGANIZATION MEMBER_ROLE RELEASE_BASE RELEASE_VERSION CODEBUILD_BUILD_ID; do need "$v"; done
DISCOVERY_ROLE=${DISCOVERY_ROLE:-DmtDiscoveryRole}
COMMAND_ROLE=${COMMAND_ROLE:-DmtCommandRole}
S3="s3://$BUCKET/$ENV/organization"
ACCESS="dormeto-access-commands-$RELEASE_VERSION.sh"

# the plan's accounts, from the bucket: an organization's hundreds of ids do not fit the build's environment
aws s3 cp --only-show-errors "$S3/accounts.txt" accounts.txt
aws s3 cp --only-show-errors "$S3/discovery-only.txt" discovery-only.txt
ACCOUNTS=$(cat accounts.txt)
for a in $ACCOUNTS $(cat discovery-only.txt); do
  case $a in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;; *) die "the plan's accounts: $a is not an account id" ;; esac
done

verify() {
  grep "  $2\$" "$1" >"$2.sum" || die "no checksum for $2 in $1: stopped"
  sha256sum -c "$2.sum" || die "the checksum of $2 does not match: stopped"
}

fetch() {
  case $RELEASE_BASE in
  s3://*) aws s3 cp --only-show-errors "$RELEASE_BASE/$1" "$1" ;;
  *) curl -fsSL -o "$1" "$RELEASE_BASE/$1" ;;
  esac
}

say "downloading the access commands of release $RELEASE_VERSION and checking them"
fetch SHA256SUMS
fetch "$ACCESS"
verify SHA256SUMS "$ACCESS"

# the CLI follows roles.json: what a previous run left must not be read as this run's
: >roles.json
aws s3 cp --only-show-errors roles.json "$S3/roles.json"

say "checking that the delegation policy is the one that was planned"
aws s3 cp --only-show-errors "$S3/policy-base.json" policy-base.json
aws s3 cp --only-show-errors "$S3/policy.json" policy.json
if aws organizations describe-resource-policy --query ResourcePolicy.Content --output text >policy-now.json 2>policy-now.err; then
  if [ "$(cat policy-now.json)" = None ]; then : >policy-now.json; fi
elif grep -q ResourcePolicyNotFoundException policy-now.err; then
  : >policy-now.json
else
  cat policy-now.err >&2
  die "cannot read the delegation policy"
fi
[ "$(cat policy-now.json)" = "$(cat policy-base.json)" ] || die "the delegation policy changed since the plan: plan again"
say "putting the delegation policy"
aws organizations put-resource-policy --content file://policy.json

# access ACCOUNT: the account's roles, with the member role's credentials; its output is the job's log. What the AWS CLI
# writes on stderr is kept apart from the credentials, said only when the role cannot be assumed.
access() {
  creds=$(aws sts assume-role --role-arn "arn:aws:iam::$1:role/$MEMBER_ROLE" --role-session-name "dmt-$ENV-organization" \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text 2>"assume.$1.err") || {
    cat "assume.$1.err"
    return 1
  }
  # shellcheck disable=SC2086
  set -- "$1" $creds
  AWS_ACCESS_KEY_ID=$2
  AWS_SECRET_ACCESS_KEY=$3
  AWS_SESSION_TOKEN=$4
  export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
  ACCOUNT=$1
  ENVS=$ENV
  # an empty COMMAND_ROLE: discovered, never napped (no command role, no boundary)
  if grep -qx "$1" discovery-only.txt; then COMMAND_ROLE=; fi
  export ACCOUNT MANAGER_ACCOUNT ENVS ORGANIZATION DISCOVERY_ROLE COMMAND_ROLE
  bash "$ACCESS" 2>&1
}

# job ACCOUNT: access in a subshell, then its result line in res.ACCOUNT.
job() {
  if (access "$1") >"out.$1" 2>&1; then
    printf '{"account":"%s","ok":true}\n' "$1" >"res.$1"
  else
    err=$(grep . "out.$1" | tail -n 1 | tr -d '\000-\037' | sed 's/\\/\\\\/g; s/"/\\"/g')
    printf '{"account":"%s","ok":false,"error":"%s"}\n' "$1" "$err" >"res.$1"
  fi
  return 0
}

batch=
n=0
total=0
flush() {
  wait
  for a in $batch; do
    cat "res.$a" >>roles.json
    # the CLI counts these lines: the accounts done out of the total; a failed account's whole output comes first, each
    # line prefixed with the account
    if grep -q '"ok":true' "res.$a"; then
      say "roles of $a: ok"
    else
      sed "s/^/dormeto: $a: /" "out.$a"
      say "roles of $a: failed"
    fi
  done
  aws s3 cp --only-show-errors roles.json "$S3/roles.json"
  batch=
  n=0
}
for a in $ACCOUNTS; do
  job "$a" &
  batch="$batch $a"
  n=$((n + 1))
  total=$((total + 1))
  [ "$n" -lt 10 ] || flush
done
if [ "$n" -gt 0 ] || [ "$total" -eq 0 ]; then flush; fi
say "done"
