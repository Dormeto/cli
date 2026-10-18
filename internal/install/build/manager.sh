#!/bin/sh
# Dormeto install: Terraform with the manager release, run by CodeBuild (buildspec.yml).
# Inputs: ACTION (plan | plan-destroy | apply | apply-destroy | request), BUCKET, ENV, TF_VERSION, RELEASE_BASE
# (https://... or s3://...), RELEASE_VERSION, PLAN_BUILD (apply actions), CODEBUILD_BUILD_ID (set by CodeBuild).
# ACTION=request is a dashboard update, started by the rule for an object created under $ENV/requests/: it takes
# REQUEST_ETAG (that object's ETag, the only value of the event the rule passes on) instead of RELEASE_BASE,
# RELEASE_VERSION and PLAN_BUILD. It takes the oldest request with that ETag and no final result, derives those from it
# once checked, and writes its outcome to $ENV/results/<request id>.json and $ENV/results/<build id>.json.
# RELEASES_URL overrides the published releases (tests only).
set -eu

REASON=
say() { echo "dormeto: $*"; }
die() {
  REASON=$*
  echo "dormeto: $*" >&2
  exit 1
}
need() { eval "[ -n \"\${$1:-}\" ]" || die "$1 is not set"; }

# matches VALUE CHARS ERE: VALUE is not empty, at most 64 characters, holds only CHARS (a bracket expression's, so
# never a new line) and matches the extended regular expression ERE
matches() {
  [ "${#1}" -le 64 ] || return 1
  case $1 in "" | *[!$2]*) return 1 ;; esac
  printf '%s\n' "$1" | LC_ALL=C grep -Eq "$3"
}

# exists KEY WHAT: whether $ENV/KEY is in the bucket; only a 404 means it is not, any other error stops
exists() {
  if aws s3api head-object --bucket "$BUCKET" --key "$ENV/$1" >/dev/null 2>head.err; then return 0; fi
  if grep -q -e '(404)' -e 'Not Found' head.err; then return 1; fi
  cat head.err >&2
  die "cannot check $2: stopped"
}

# older A B: whether version A sorts before version B: the three numbers, then a pre-release before its release, two
# pre-releases as text (the CLI's CompareVersions)
older() {
  LC_ALL=C awk -v a="$1" -v b="$2" '
    function core(v) { return index(v, "-") ? substr(v, 1, index(v, "-") - 1) : v }
    function pre(v) { return index(v, "-") ? substr(v, index(v, "-") + 1) : "" }
    BEGIN {
      split(core(a), x, "."); split(core(b), y, ".")
      for (i = 1; i <= 3; i++) if (x[i] + 0 != y[i] + 0) exit !(x[i] + 0 < y[i] + 0)
      p = pre(a); q = pre(b)
      exit !(p != q && p != "" && (q == "" || p < q))
    }'
}

# field NAME: sets FIELD to the request's field NAME when it is a string of printable ASCII characters, empty
# otherwise; one longer than 64 characters is refused before any command reads it
field() {
  FIELD=$(jq -r --arg f "$1" '.[$f] | select(type == "string" and all(explode[]; . > 31 and . < 127))' request.json)
  [ "${#FIELD}" -le 64 ] || die "the request $REQUEST_ID: its $1 is longer than 64 characters"
}

# result STATUS MESSAGE: the request's outcome, with its action and version once checked, under the request's id and
# under the build's, which the dashboard reads
result() {
  jq -nc --arg build "$CODEBUILD_BUILD_ID" --arg status "$1" --arg message "$2" --arg action "$REQUEST_ACTION" \
    --arg version "$REQUEST_VERSION" '{build: $build, status: $status, message: $message, action: $action, version: $version}' >result.json
  aws s3 cp --only-show-errors result.json "$S3/results/$REQUEST_ID.json"
  aws s3 cp --only-show-errors result.json "$S3/results/$CODEBUILD_BUILD_ID.json"
}

# finish: the request's result, from the build's exit status
finish() {
  code=$?
  trap - EXIT
  if [ "$code" -ne 0 ]; then
    result failed "${REASON:-the build failed: see its log}"
  elif [ "$ACTION" = plan ]; then
    result succeeded "manager $RELEASE_VERSION planned"
  else
    result succeeded "manager $RELEASE_VERSION applied"
  fi
  exit "$code"
}

# take_request: finds the request of the event's ETag, checks it, trusting nothing in it beyond these checks, and sets
# ACTION, RELEASE_BASE, RELEASE_VERSION and PLAN_BUILD from it
take_request() {
  for v in BUCKET ENV CODEBUILD_BUILD_ID REQUEST_ETAG; do need "$v"; done
  # what is not an environment or an ETag stops at once: no download, and no id to write a result under
  matches "$ENV" 'a-z0-9-' '^[a-z0-9-]+$' || die "ENV is not an environment name: stopped"
  matches "$REQUEST_ETAG" '0-9a-f-' '^[0-9a-f]{32}(-[0-9]+)?$' || die "REQUEST_ETAG is not an ETag: stopped"
  S3="s3://$BUCKET/$ENV"

  # the oldest request (by name) of this environment with that ETag and no final result: a request replayed after its
  # result has the same content, so the same ETag, and is not taken again
  aws s3api list-objects-v2 --bucket "$BUCKET" --prefix "$ENV/requests/" --output json >requests.json
  jq -r --arg etag "$REQUEST_ETAG" '.Contents[]?
    | select((.ETag | type) == "string" and (.ETag | ltrimstr("\"") | rtrimstr("\"")) == $etag)
    | .Key | select(type == "string" and all(explode[]; . > 32 and . < 127))' requests.json | LC_ALL=C sort >requests.txt
  REQUEST_KEY=
  while IFS= read -r k; do
    matches "$k" 'a-z0-9/TZ.-' "^$ENV/requests/[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}\\.json\$" || continue
    id=${k##*/}
    id=${id%.json}
    if exists "results/$id.json" "the result of the request $id"; then
      aws s3 cp --only-show-errors "$S3/results/$id.json" earlier.json
      [ "$(jq -r '.status // ""' earlier.json)" = running ] || continue
    fi
    REQUEST_KEY=$k
    REQUEST_ID=$id
    break
  done <requests.txt
  if [ -z "$REQUEST_KEY" ]; then
    say "no request to take for this event"
    exit 0
  fi
  trap finish EXIT
  result running ""

  say "reading the request $REQUEST_ID"
  aws s3 cp --only-show-errors "$S3/requests/$REQUEST_ID.json" request.json
  jq -e -s 'length == 1 and (.[0] | type == "object")' request.json >/dev/null 2>&1 ||
    die "the request $REQUEST_ID is not a JSON object"
  field action
  case $FIELD in plan | apply) ;; *) die "the request $REQUEST_ID asks for neither a plan nor an apply" ;; esac
  ACTION=$FIELD
  REQUEST_ACTION=$FIELD
  field version
  matches "$FIELD" '0-9A-Za-z.-' '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' ||
    die "the request $REQUEST_ID has no version of the form x.y.z"
  RELEASE_VERSION=$FIELD
  REQUEST_VERSION=$FIELD
  PLAN_BUILD=
  if [ "$ACTION" = apply ]; then
    field planBuild
    case $FIELD in "dmt-$ENV-install:"*) ;; *) FIELD= ;; esac # this project's own builds only
    matches "$FIELD" 'a-z0-9:-' '^dmt-[a-z0-9-]+-install:[0-9a-f-]{36}$' ||
      die "the request $REQUEST_ID has no plan build of $ENV"
    PLAN_BUILD=$FIELD
  fi

  # the release base comes from the version, never from the request; only an HTTP error (22) means not published
  status=0
  curl -fsS --max-time 30 -o /dev/null "$RELEASES_URL/$RELEASE_VERSION/SHA256SUMS" || status=$?
  if [ "$status" -eq 0 ]; then
    RELEASE_BASE=$RELEASES_URL/$RELEASE_VERSION
    say "manager $RELEASE_VERSION: published"
  elif [ "$status" -ne 22 ]; then
    die "releases.dormeto.com cannot be reached: stopped"
  else
    RELEASE_BASE=
    if exists release/SHA256SUMS "the staged release"; then
      aws s3 cp --only-show-errors "$S3/release/SHA256SUMS" staged.sums
      if awk -v n="terraform-aws-dormeto-$RELEASE_VERSION.zip" '$2 == n { found = 1 } END { exit !found }' staged.sums; then
        RELEASE_BASE=$S3/release
      fi
    fi
    [ -n "$RELEASE_BASE" ] || die "manager $RELEASE_VERSION is neither published nor staged"
    say "manager $RELEASE_VERSION: staged"
  fi

  APPLIED_BUILD=
  if exists applied.json "the applied version"; then
    aws s3 cp --only-show-errors "$S3/applied.json" applied.json
    applied=$(jq -r '.version // ""' applied.json)
    APPLIED_BUILD=$(jq -r '.build // ""' applied.json)
    if older "$RELEASE_VERSION" "$applied"; then die "manager $RELEASE_VERSION is older than the applied $applied"; fi
  fi

  if [ "$ACTION" = plan ]; then
    # a dashboard plan changes the version only: the configuration last applied, never one a CLI run wrote since
    if ! exists applied/main.tf.json "the applied configuration" ||
      ! exists applied/.terraform.lock.hcl "the applied configuration"; then
      die "run \`dormeto install\` with the CLI 0.2.0 or later"
    fi
  else
    say "checking the plan of build $PLAN_BUILD"
    exists "plans/$PLAN_BUILD/release.json" "the plan of build $PLAN_BUILD" ||
      die "the plan of $PLAN_BUILD was not prepared from the dashboard"
    exists "plans/$PLAN_BUILD/main.tf.json" "the plan of build $PLAN_BUILD" ||
      die "the plan of $PLAN_BUILD has no configuration"
    aws s3 cp --only-show-errors "$S3/plans/$PLAN_BUILD/release.json" release.json
    [ "$(jq -r '.version // ""' release.json)" = "$RELEASE_VERSION" ] ||
      die "the plan of $PLAN_BUILD is not manager $RELEASE_VERSION"
    # nothing applied since the plan was made (Terraform also refuses a saved plan its state has moved past)
    [ "$(jq -r '.appliedBuild // ""' release.json)" = "$APPLIED_BUILD" ] || die "the plan is stale: prepare again"
  fi
}

REQUEST_ID=
REQUEST_KEY=
REQUEST_ACTION=
REQUEST_VERSION=
RELEASES_URL=${RELEASES_URL:-https://releases.dormeto.com/manager}
need ACTION
if [ "$ACTION" = request ]; then take_request; fi

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

case $ACTION in
apply*)
  # the plan's own configuration; config/'s for a plan the CLI made before plans kept theirs, never for a request
  CONFIG=config
  if [ -n "$REQUEST_ID" ] || exists "plans/$PLAN_BUILD/main.tf.json" "the configuration of the plan"; then
    CONFIG=plans/$PLAN_BUILD
  fi
  say "reading the configuration of the plan of build $PLAN_BUILD"
  aws s3 cp --only-show-errors "$S3/$CONFIG/main.tf.json" main.tf.json
  aws s3 cp --only-show-errors "$S3/plans/$PLAN_BUILD/.terraform.lock.hcl" .terraform.lock.hcl
  ;;
*)
  if [ -n "$REQUEST_ID" ]; then
    say "reading the configuration last applied to $ENV"
    aws s3 cp --only-show-errors "$S3/applied/main.tf.json" main.tf.json
    aws s3 cp --only-show-errors "$S3/applied/.terraform.lock.hcl" .terraform.lock.hcl
  else
    say "reading the configuration of $ENV"
    aws s3 cp --only-show-errors "$S3/config/main.tf.json" main.tf.json
    # a kept lock keeps the providers
    if exists config/.terraform.lock.hcl "the lock file of the providers"; then
      aws s3 cp --only-show-errors "$S3/config/.terraform.lock.hcl" .terraform.lock.hcl
    fi
  fi
  ;;
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
  for f in tfplan plan.json .terraform.lock.hcl main.tf.json; do
    aws s3 cp --only-show-errors "$f" "$S3/plans/$CODEBUILD_BUILD_ID/$f"
  done
  if [ -n "$REQUEST_ID" ]; then
    # what an apply request checks: the version, and that nothing was applied since
    jq -nc --arg version "$RELEASE_VERSION" --arg base "$RELEASE_BASE" --arg request "$REQUEST_KEY" \
      --arg appliedBuild "$APPLIED_BUILD" '{version: $version, base: $base, request: $request, appliedBuild: $appliedBuild}' >release.json
    aws s3 cp --only-show-errors release.json "$S3/plans/$CODEBUILD_BUILD_ID/release.json"
  else
    aws s3 cp --only-show-errors .terraform.lock.hcl "$S3/config/.terraform.lock.hcl"
  fi
  ;;
apply | apply-destroy)
  say "applying the plan of build $PLAN_BUILD"
  aws s3 cp --only-show-errors "$S3/plans/$PLAN_BUILD/tfplan" tfplan
  # a saved plan needs no approval flag: it is the plan that was read
  terraform apply -input=false tfplan
  if [ "$ACTION" = apply ]; then
    say "recording the outputs, the applied version and its configuration"
    terraform output -json >outputs.json
    aws s3 cp --only-show-errors outputs.json "$S3/outputs.json"
    # the configuration before the version that names it: a dashboard plan reads both
    for f in main.tf.json .terraform.lock.hcl; do
      aws s3 cp --only-show-errors "$f" "$S3/applied/$f"
    done
    printf '{"version":"%s","build":"%s"}\n' "$RELEASE_VERSION" "$CODEBUILD_BUILD_ID" >applied.json
    aws s3 cp --only-show-errors applied.json "$S3/applied.json"
  else
    say "removing the applied version, its configuration and the outputs"
    aws s3 rm --only-show-errors "$S3/applied.json"
    aws s3 rm --only-show-errors --recursive "$S3/applied/"
    aws s3 rm --only-show-errors "$S3/outputs.json"
  fi
  ;;
esac
say "done"
