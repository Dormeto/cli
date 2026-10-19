# Changelog

## 0.5.0

- `dormeto workloads keep-awake`, `resume` and `schedule show|set|clear`: keep workloads awake for up to 7 days
  (`--for 2h` or `--until 17:30`, with `--wait` until they are awake, for CI), end the pause, and set, show or clear
  their weekly awake and nap windows, one workload or every workload with a tag (`--tag`, `--yes`, `--dry-run`).
  `keep-awake` and `resume` work with any manager 0.15.0 or later (they use pauses); schedules need manager 0.16.0 and
  routers 0.31.0.

## 0.4.1

- `dormeto install` prints what Terraform did, from its own closing line (`applied: 2 added, 1 changed, 0 destroyed`),
  and `applied the plan: ...` with the plan's counts when that line could not be read; `dormeto uninstall` does the same
  for its `destroyed` line.
- `dormeto routers upgrade`: a start the manager answered but whose answer could not be read is reported unknown (with
  the hint to check the Operations), a start that was never sent (AWS credentials failing) is reported failed, `not
  started: ...` (safe to run again; `not started` when the run was cut short by `--timeout` or a signal), a start
  answered without an operation id is reported unknown, the Operations hint comes after the summary when upgrades did
  not finish, and the manager's `Retry-After` (429, 503) is waited, up to 30 seconds, before a read is retried.
- The Homebrew cask removes the macOS quarantine with Homebrew 7's `postflight_steps`, so `brew install` no longer warns
  that `postflight` is deprecated.
- `make dry-run` without `VERSION` says `VERSION is required: make dry-run VERSION=x.y.z` at once.

## 0.4.0

- `dormeto routers list`, `dormeto routers upgrade` and `dormeto workloads list`: the manager's routers and workloads
  from the command line, with the install's AWS profile, through the manager's IAM-authorized `/iam/*` routes (the API
  found by its name, `dmt-<env>-manager-api`, or `--api-url`). `routers list` shows each router's version (and the one
  available), health and workloads, `--outdated` only the routers to upgrade; `workloads list` filters by `--state`,
  `--router`, `--tag` and `--search`; `--json` prints the API's answer.
- `routers upgrade [<router>...]` upgrades the routers named or selected (`--all` for every one), prints the plan with
  the routers skipped and why (written in Terraform, up to date, newer than the version available, of unknown version,
  busy with an operation), asks for confirmation (`--yes` without a terminal, `--dry-run` to stop at the plan), runs the
  upgrades in the manager at most `--concurrency` at once (4 by default) and follows them until `--timeout` (60m).
- The routers are narrowed by `--account`, `--router-region`, `--name` (a glob) and `--tag key=value` (repeatable),
  which selects the routers that serve a workload with these Dormeto tags.
- The caller needs `execute-api:Invoke` on the API's `/iam/*` routes (`GET` alone for the two lists) and acts as an
  administrator of the manager; `apigateway:GET` finds the API unless `--api-url` is given. Manager 0.15.0 or later. The
  README has the section "Routers and workloads" and the policy.

## 0.3.0

- `dormeto install --tag key=value` (repeatable): the customer's tags join the fixed ones in the AWS provider's
  `default_tags` of the manager's Terraform configuration. The tags given replace the install's tags (`--tag ''` alone
  removes them all; without `--tag`, the stored ones are kept), are kept in `install.json` and by the dashboard's
  updates, and are shown by `dormeto install status`. Keys are compared without case, as IAM does; `aws:`, `dormeto:`,
  `project` and `managed-by` are refused. The bootstrap's resources keep the fixed tags.
- An unreadable local release no longer fails the existing-install check with an empty version: the release row says
  why.

## 0.2.0

- `dormeto install` and `dormeto uninstall`: the Dormeto manager installed in an AWS account by CodeBuild, guided. The
  CLI suggests organization or account mode, asks what it needs (each answer has a flag), checks the prerequisites
  read-only (`dormeto install check`), creates the install's bucket, role, CodeBuild project and log group by API calls,
  runs `terraform plan` in a build, shows it and applies that saved plan once you confirm (`--yes` never confirms;
  without a terminal it stops after the plan and `dormeto install apply` applies it), tracks the builds, and checks the
  dashboard and the invitations. `dormeto install status` follows a running build; an upgrade is `dormeto install` again.
  With `--management-profile`, organization access (the delegation policy merged, the member accounts' roles) is
  planned, confirmed and deployed by a build in the management account, whose role and project are deleted when it ends;
  without it, it is disabled with the procedure. `dormeto uninstall` removes the manager (routers first, deletion
  protection lifted, the destroy plan confirmed) and, on request, the bootstrap. Manager 0.11.0 or later. The README
  has the section "Install the manager", its flags and its permissions; the Privacy section names what the install
  downloads.
- The dashboard can update the manager (from manager 0.12.0): `dormeto install` sets up a rule
  (`dmt-<env>-install-requests`) that starts the install project when the manager's API drops a request in the install
  bucket (bucket events on), through a role (`dmt-<env>-install-events`) that may start that project only;
  `--build-role` leaves it off and `dormeto uninstall` removes it. The build takes a request (plan of a newer version,
  then the apply of that plan), checks it, plans with the configuration last applied (`applied/`) and writes its result
  under `results/`. The CLI records its version in `<env>/cli.json` and passes the module's `install` input from manager
  0.12.0. `dormeto install status` shows the last dashboard update. The README has "Updating from the dashboard".
- `dormeto install --adopt-state <file>` takes over a manager installed with the Terraform module directly, from its
  state (`terraform state pull`): the module's resources (`--adopt-module`, `manager` by default) move to the install's
  state, nothing recreated, and the resources outside it stay in the source, listed. The state is checked first (format
  version 4, a Terraform no newer than the install's, this environment's manager in the profile's account and region;
  refused once the environment has an `install.json`, an `applied.json` or another state); the original is kept in the install's bucket (`<env>/adopted/`),
  the moved state written only where there is none, and `install.json` records `adoptedFrom` and `adoptedModule`. After
  the first apply the CLI says how to detach the manager from its source; until then `dormeto uninstall` refuses. The
  README has "Adopting a manager installed with Terraform".
- `--ecs-uptime` counts each ECS service's tasks (CloudWatch `LiveTaskCount`) and prices the service at its average
  number of tasks while it ran, instead of its desired count when discovered: a service stopped at night by a schedule
  (found at 0, priced as one task before) or autoscaled is priced as it ran. The Uptime column and the JSON
  (`uptime.tasks`) give the count. A service without the metric keeps its count when discovered. An uptime kept in the
  cache from before is read again.
- The figures are at each workload's recommended idle window by default: the savings, the cost with Dormeto and the
  tables by account and region. `--window <minutes>` sets one window for every workload instead (a what-if); the
  recommended windows' savings are then given too. The JSON's `options.window` is `null` without it.
- `--days <n>`: the days of requests read before the run, 14 by default, 1 to 15 (CloudWatch keeps the requests by the
  minute for 15 days). The terminal, the Markdown report and the JSON (`period`: its days and dates) give the period read.
- Before the estimate, the command says what it reads to reach the accounts: the profile's account and organization,
  then the organization's accounts, with their progress in a terminal (a large organization takes a while).
- npm is no longer a channel: 0.1.0's packages (`@dormeto/cli` and its platform packages) are deprecated, and later
  versions are not published there. Install by Homebrew, `https://dormeto.com/install.sh`, `go install` or a release
  archive (on Windows, the `.zip`).

## 0.1.0

The first release of the Dormeto CLI: `dormeto estimate`, the savings estimate of the manager, as a single Go binary. It
reads an AWS account or organization with read-only access and gives the same figures, reports and cache as the manager's
Node estimate (pinned to the product's code by shared test cases), with nothing to install but the binary.

- `dormeto estimate`: discovery of the workloads behind Application Load Balancers (EC2, Auto Scaling, ECS, EKS, RDS),
  their requests and uptime, and the estimate; the terminal summary and `estimate-<date>.md` and `.json` in `--out`.
- Install by Homebrew (`dormeto/tap/dormeto`), npm (`@dormeto/cli`), `https://dormeto.com/install.sh`, `go install` or a
  release archive for macOS, Linux and Windows (Intel/AMD and ARM).
- `dormeto version` and shell completion.
- Where Dormeto is already installed: the workloads it manages, asleep ones included, are found and marked; Dormeto's
  cost is the installation's now plus a signed "New recommendation" line (workloads to onboard, onboarded ones the
  filters leave, routers added or removed), so its total is the cost once the recommendation is applied.
