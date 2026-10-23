<div align="center">

<a href="https://dormeto.com"><img src="https://dormeto.com/img/og.png" alt="Dormeto: your idle web apps nap, the first visitor wakes them" width="760"></a>

# Dormeto CLI

**Estimate what Dormeto would save on your AWS accounts, from your laptop, before anything is installed.**

[Website](https://dormeto.com) · [Get started](https://dormeto.com/get-started.html) · [Product](https://dormeto.com/product.html) · [Security](https://dormeto.com/security.html) · [Manager](https://github.com/Dormeto/manager) · [Router](https://github.com/Dormeto/router)

![License](https://img.shields.io/badge/license-MIT-3546c4)
![Go](https://img.shields.io/badge/Go-single_binary-00ADD8?logo=go&logoColor=white)
![AWS](https://img.shields.io/badge/AWS-estimate_read--only-FF9900?logo=amazonwebservices&logoColor=white)

</div>

`dormeto` is the Dormeto command line. `dormeto estimate` reads an AWS account or a whole organization with read-only
access and estimates what Dormeto would save on the web workloads behind your Application Load Balancers (EC2, Auto
Scaling, ECS, EKS, and the RDS databases that nap with them): the workloads that could nap, how long each would sleep and
wake, and the monthly saving net of Dormeto's own cost. It gives a summary in the terminal and a Markdown and a JSON
report. It installs nothing in your accounts and sends nothing anywhere but to AWS.

`dormeto install` then installs the Dormeto manager in your AWS account with CodeBuild, after its checks and your
confirmation of the plan (see [Install the manager](#install-the-manager)). It is the only command that changes your
accounts itself.

Once the manager runs, every operation of its dashboard is a command too, with the same AWS profile: routers, workloads,
discovery, operations, users, the audit, the manager's update (see [Routers and workloads](#routers-and-workloads) and
the sections after it). These ask the manager, which acts as it does for the dashboard.

## Install

Pick one channel. They all install the same binary, `dormeto`, with no runtime to install.

### Homebrew (macOS, Linux)

```sh
brew install dormeto/tap/dormeto
```

### The script (macOS, Linux)

```sh
curl -fsSL https://dormeto.com/install.sh | sh
```

It downloads the archive for your system, checks it against the release's checksums and installs `dormeto`. Two
variables change it: `DORMETO_VERSION` (a version, without the `v`, to install instead of the latest) and `DORMETO_INSTALL_DIR` (where to
install `dormeto`; `$HOME/.local/bin` by default). It needs no sudo and changes nothing else.

```sh
curl -fsSL https://dormeto.com/install.sh | DORMETO_VERSION=0.1.0 DORMETO_INSTALL_DIR="$HOME/.local/bin" sh
```

### Go

```sh
go install github.com/Dormeto/cli/cmd/dormeto@latest
```

### Windows, and the release archives

Each release has an archive per system (macOS, Linux and Windows, on Intel/AMD and ARM) and a `checksums.txt` on the
[releases page](https://github.com/Dormeto/cli/releases). On Windows, download `dormeto_<version>_windows_amd64.zip`
(`_arm64` on an ARM PC), unzip it and put `dormeto.exe` in a folder of your `PATH`. Elsewhere, unpack the archive and put
`dormeto` in your `PATH`.

An archive downloaded with a browser is blocked by the system the first time:

- macOS: remove the quarantine mark, `xattr -d com.apple.quarantine ./dormeto`.
- Windows: in SmartScreen's warning, choose "More info" and then "Run anyway".

(`curl`, Homebrew and `go install` do not need this.)

Check the install with `dormeto version`.

## First run

Log in with a profile that can read the accounts (see [Permissions](#permissions)), then estimate:

```sh
aws sso login --profile <profile>
dormeto estimate --profile <profile> --uptime
```

`--uptime` also measures when each EC2 instance, ECS service and database actually ran, so the estimate does not count
the hours a schedule already stops them. The first run reads every account and region: minutes to an hour for a large
organization. A second run reads the [cache](#cache). `dormeto estimate --help` prints the options.

## Options

```
Usage: dormeto estimate --profile <profile>[,<profile>...] [--accounts 111111111111,222222222222]
  [--regions eu-west-1,eu-west-3] [--exclude-tags 'env=dev|test,team=web' ...] [--uptime | --ec2-uptime | --ecs-uptime | --rds-uptime] [--role <name>]
  [--baseline always|business-hours] [--timezone Europe/Paris] [--discount-compute 0-90] [--discount-databases 0-90]
  [--refresh discovery|metrics] [--days 14] [--window <minutes>] [--max-wakes 3|none]
  [--min-saving 1|none] [--max-wakes-rds 2|none] [--parallel 5] [--out <dir>]
```

An option bad or missing prints its message and the usage, and exits with code 2. Credentials that do not work exit
with code 1 and AWS's message; an SSO session expired, invalid or never logged in adds the command that renews it,
`aws sso login --profile <profile>`, for the profile that failed.

- `--profile <profile>[,<profile>...]`: the AWS profile (required). The organization's active accounts when the profile
  may list them (the management account or a delegated administrator), else its own account only, and the report says
  why. Member accounts are reached through `--role`; the profile's own account uses its own credentials. Several
  profiles, comma-separated (for example one SSO profile per account when none may list the organization): each reaches
  its accounts, merged into one estimate; an account a profile logs in to directly is read with it, never through
  another's role.
- `--accounts <id>[,<id>...]`: restrict the estimate to some of the organization's accounts (with several profiles, to
  the merged accounts). An id the organization does not have is listed apart; if none of them is found, the command
  exits with code 2.
- `--regions <region>[,<region>...]`: the regions to scan, instead of every enabled region.
- `--exclude-tags <criterion>` (repeatable): leave out the workloads with these tags on their load balancer, target
  group or listener rule. In one criterion, every comma-separated key must match (and), each with one of its
  `|`-separated values (or); a key alone matches any value. Repeated, any criterion excludes (or). For example
  `--exclude-tags 'env=dev|test,team=web' --exclude-tags dormeto:ignore`. They are left out before their requests are
  read and listed apart in the report, with the criterion that matched.
- `--uptime`: sets `--ec2-uptime`, `--ecs-uptime` and `--rds-uptime`.
- `--ec2-uptime`: measures when each EC2 instance without an Auto Scaling group ran over the days read (its
  `CPUUtilization`, which EC2 publishes every 5 minutes while an instance runs): its actual cost counts the hours it
  ran, its savings only the hours Dormeto would nap it while it ran (an instance stopped at night by a schedule saves
  less). An instance found stopped is estimated from the hours it ran (without the option, it is left out). Instances
  Dormeto already manages and Auto Scaling groups are not measured.
- `--ecs-uptime`: measures when each ECS service had a task over the days read (its `CPUUtilization`, which ECS publishes
  every minute while the service has a task), and how many tasks it had (its `LiveTaskCount`, the most in each 5
  minutes): its actual cost and its savings count only those hours, at its average number of tasks then, whatever its
  count when discovered (a service found at 0 at night, or autoscaled). The report's Uptime column gives both, such as
  "36%, 2.4 tasks". A service without `LiveTaskCount` is priced at its desired count when discovered, one task when
  found at 0. Without the option, a service found at 0 tasks is left out. Services Dormeto already manages are not
  measured.
- `--rds-uptime`: measures when each companion database was available (its `CPUUtilization`): its saving and cost count
  only those hours. A database stopped when discovered (a schedule may start it) is estimated from them, else left out.
- `--role <name>`: the role assumed in each member account (`OrganizationAccountAccessRole` by default: accounts
  created by AWS Organizations have it, invited ones do not). An account whose role cannot be assumed is listed with
  AWS's reason and how to include it.
- `--baseline always|business-hours`: what the workloads cost without Dormeto, for the saving: running all the time
  (`always`, the default), or only during business hours, 8:00 to 20:00 each day (`business-hours`, for workloads you already stop at night).
- `--timezone <zone>`: the time zone of `business-hours` (an IANA name such as `Europe/Paris`; `UTC` by default).
- `--discount-compute <0-90>`: your discount on compute (EC2, ECS, EKS) in percent, such as a Savings Plan's, applied to
  the prices.
- `--discount-databases <0-90>`: your discount on databases (RDS) in percent, such as a reserved instance's.
- `--refresh discovery|metrics`: read again what the [cache](#cache) holds. `discovery` reads discovery and the requests
  again, `metrics` the requests only.
- `--days <n>`: the days of requests read before the run (14 by default, 1 to 15: CloudWatch keeps the requests by the
  minute for 15 days). The terminal and the report give the period read, with its dates.
- `--window <minutes>`: one idle window, 5 to 1440, for every workload's figures (a what-if); the report then also gives
  the savings at each workload's recommended window. Without it, every figure (the savings, the cost with Dormeto, by
  account and region) is at each workload's recommended window, the one a customer would set.
- `--max-wakes <n>|none`: leave out a workload that would wake more than `n` times a day (3 by default; its visitors
  would wait more than it saves). It is listed apart in the report with its window, wakes a day and the saving left
  out. `none` keeps them all.
- `--min-saving <dollars>|none`: leave out a workload that would save less than that a month (1 by default; not worth
  installing Dormeto for). Listed apart, as `--max-wakes`' are. `none` keeps them all.
- `--max-wakes-rds <n>|none`: leave out the companion database of a workload that would wake more than `n` times a day
  (2 by default: a database takes minutes to start). `none` keeps them. A companion database is an RDS instance or an
  Aurora provisioned cluster (not Aurora Serverless) whose security group lets in a workload's: it naps with it, and
  its saving is added to the workload's (before `--min-saving`) and listed apart. Left out, with why: excluded by its
  own tags, reached by another workload of the region, a read replica or one with replicas, or its workload waking too
  often.
- `--parallel <n>`: how many accounts (discovery) or regions (requests and uptime) are read at a time (5 by default, 1
  to 50). A region's workloads are described eight at a time.
- `--out <dir>`: where the reports are written (the current directory by default).

The estimate is conservative: every request counts as activity and as a wake (there is no waker yet to set scanners
apart), a wake lasts 60 seconds, there is no visitor's wait, and databases not attached to a load balancer are not
counted. It runs in three phases: discovery (what each eligible workload's backend would stop, as the router
describes it), requests (each workload's `RequestCount` per minute over the days read, `--days`, five metrics per
`GetMetricData` call), and the estimate (Dormeto's replay priced with the product's price book, at each workload's
recommended window, or at `--window`, net of Dormeto's cost). Before them, it says what it reads to reach the accounts:
the profile's account and organization, then the organization's accounts, listed one organizational unit at a time (a
large organization takes a while). In a terminal, each phase shows one progress line; piped or redirected, it prints a
line per account and region.

Where Dormeto is already installed, the estimate finds the workloads it manages (marked "managed by Dormeto"), asleep
ones included. Dormeto's cost then starts from the installation as it is now (the manager, a router per account and
region with onboarded workloads, each onboarded workload) and adds a signed "New recommendation" line: the workloads to
onboard, less the onboarded ones that `--exclude-tags`, `--max-wakes` or `--min-saving` leave, with the routers added
or removed. The total is what Dormeto costs once the recommendation is applied. An onboarded workload asleep when
discovered stays in that cost, but its saving is not counted (nothing is running to measure).

## Permissions

These are the estimate's (the install's are in [Permissions to install](#permissions-to-install)). Read-only: the estimate calls `Describe*`, `List*` and `Get*` operations, plus `sts:AssumeRole` to reach member
accounts, and Kubernetes `GET`s. It changes nothing.

- `sts:GetCallerIdentity`, `AssumeRole`
- `organizations:DescribeOrganization`, `ListRoots`, `ListOrganizationalUnitsForParent`, `ListAccountsForParent`, `ListTagsForResource`
- `ec2:DescribeRegions`, `DescribeInstances`, `DescribeSpotPriceHistory`, `DescribeSecurityGroups`
- `elasticloadbalancing:Describe*`
- `autoscaling:DescribeAutoScalingGroups`
- `ecs:ListClusters`, `ListServices`, `DescribeServices`, `DescribeTaskDefinition`
- `eks:ListClusters`, `DescribeCluster`
- `rds:DescribeDBInstances`, `DescribeDBClusters`
- `cloudwatch:GetMetricData` (about $0.01 per 1,000 workloads)
- `pricing:GetProducts`

The `ReadOnlyAccess` AWS managed policy covers them all: attach it to the profile's role, and to the role of each
member account.

An EKS cluster's workloads are read through its Kubernetes API, which needs the profile's own EKS access entry on the
cluster (the `AmazonEKSViewPolicy` access policy is enough). A cluster it cannot read is listed with the reason.

## Report

The terminal gets a summary: the savings, the table by account and the workloads with the most to save. Two files are
written in `--out`, named after the date of the run: `estimate-<date>.md`, the full report, and `estimate-<date>.json`,
the same figures for a program (its `tool` says `dormeto` and the version that wrote it). The table by account counts
each account's excluded workloads, whatever the reason (tags, left out, too many wakes, too small a saving); the
workloads table gives each one's uptime (measured, else 100% assumed), its recommended window, saving and wakes a day.

## Cache

`~/.cache/dormeto/estimate/<organization or account>/`, under `$XDG_CACHE_HOME` when it is set. It holds discovery and
each account's regions for a day, the requests while their days (`--days`) end on the same UTC day, and the prices. An
interrupted run resumes where it stopped; a cache file that cannot be read is read again, never an error.
`--refresh discovery` reads discovery and the requests again, `--refresh metrics` the requests only. To start over,
delete the directory.

## Privacy

`dormeto` sends nothing but its calls to AWS and to your EKS clusters' APIs, with your credentials, from your machine.
There is no telemetry and no update check. What it reads stays in the cache and the reports you choose to share.

That is the estimate's promise, and it holds. `dormeto install` also downloads from `releases.dormeto.com`
(`latest.json`, then the release's `SHA256SUMS` and the release, checked against it). Its builds, in your account,
download Terraform from `releases.hashicorp.com` and the providers through the Terraform registry, with Terraform's
checkpoint (its version check) off. The CLI and the builds use only your credentials and your account; still no
telemetry.

## Install the manager

`dormeto install` installs the Dormeto manager in an AWS account, with CodeBuild: Terraform runs in your account, not
on your machine. It asks what it needs, checks what can fail before anything is created, shows the plan, applies it once
you confirm, and follows the build. It needs a manager release 0.11.0 or later (an account outside any organization
needs 0.11.0). In order:

1. **Where it runs.** It reads the profile's account and its organization, and suggests the mode: `organization` (the
   manager reads every account of the organization) in an organization with all features, else `account` (the manager
   reads its own account only, with why). A manager in the organization's management account is allowed after a warning.
2. **The questions.** The release (the latest by default), the region (the profile's, else `eu-west-1` is suggested),
   the environment (`prod`), the administrators' emails, the mode, the management account's profile, a custom domain
   and SES. Each has a flag; a question already answered by a flag or a previous run is not asked.
3. **The checks**, read-only (`dormeto install check` runs them alone): credentials, administrator rights (simulated),
   the region and CodeBuild, the organization, an existing install of the same environment or another manager's
   resources and Dormeto roles, the release, the administrators' addresses, the domain's certificate and zone, the SES
   identity, and organization access. A failure stops the install before anything is created; a warning is shown and
   asked about once.
4. **What it creates in your account**, by API calls, tagged `project=dormeto` and `managed-by=dormeto-cli`: the bucket
   `dmt-install-<account>-<region>` (the answers, the generated Terraform configuration, the state and the plans), the
   role and the CodeBuild project `dmt-<env>-install`, and the log group `/aws/codebuild/dmt-<env>-install` (90 days).
5. **The plan.** A first build runs `terraform plan -out` and keeps the saved plan. The CLI shows what it creates,
   changes and destroys (a destroy in an install or an upgrade first, as a warning), and asks. After your confirmation,
   a second build applies exactly that saved plan, never `-auto-approve`. An install is two builds, about 15 minutes.
6. **The tracking.** In a terminal, one status line (the phase, the resources done out of the plan's total, the one in
   progress, the elapsed time); without one, or with `--verbose`, Terraform's lines. Ctrl-C detaches: the build goes on,
   and `dormeto install status` follows it again. A failure prints the last lines of the log and where to read the rest.
7. **The result.** The dashboard's URL must answer over HTTPS (a new distribution can take some minutes), and each
   administrator's invitation is checked.

The generated Terraform configuration is written again at every run: edit nothing in it by hand. An input of the manager
module without a flag is set with `--var name=<JSON value>` (`--var 'alert_emails=["ops@example.com"]'`), kept for the
next runs.

### First run

Log in with a profile that has administrator rights in the account to install in (see
[Permissions to install](#permissions-to-install)), then:

```sh
aws sso login --profile <profile>
dormeto install --profile <profile>
```

Without a terminal (a script, a pipeline), every answer comes from a flag. Answers that have a default take it: the
latest release, the suggested mode, no domain and no SES, the environment `prod`; only the administrators' emails are
required. The run stops after the plan, prints what it would do and the command that applies it:

```sh
dormeto install --profile <profile> --admin-emails you@example.com
dormeto install apply --profile <profile>
```

`dormeto install apply` is the confirmation when you run it yourself: it shows the last saved plan, applies it and
follows the build. It refuses a plan made for an uninstall. `--yes` takes the suggested answers; it never confirms a plan.

### Flags

`dormeto install check`, `apply` and `status` take the same flags, except `--adopt-state` and `--adopt-module`.
`dormeto install --help` prints them.

- `--profile <profile>`: the AWS profile (the SDK's default, such as `AWS_PROFILE`, when absent).
- `--region <region>`: the manager's region (the profile's, else asked, with `eu-west-1` suggested).
- `--env <name>`: the environment, 1 to 10 lowercase letters, digits or inner hyphens (`prod` by default). One
  environment per account.
- `--version <version>`: the manager release to install (the latest by default, from `releases.dormeto.com`).
- `--release <dir>`: a local release directory, uploaded to the install's bucket instead of downloaded: an unreleased
  manager.
- `--admin-emails <email>[,<email>...]`: the administrators, at least one; each gets an invitation.
- `--mode organization|account`: where the manager discovers workloads (suggested from the account's organization).
- `--domain <host>`: the dashboard's custom domain (a `cloudfront.net` host by default), with `--certificate-arn` and
  `--zone-id`.
- `--certificate-arn <arn>`: the domain's ACM certificate, in `us-east-1`, issued and covering the dashboard's host and
  `auth.<host>`.
- `--zone-id <id>`: the domain's Route 53 hosted zone.
- `--ses-identity <identity>`: the SES identity that sends the invitations (Cognito sends them by default, about 50 a
  day), with `--email-from`. An identity not verified is a failure; an SES account still in its sandbox, a warning.
- `--email-from <address>`: the invitations' sender, with `--ses-identity`.
- `--var name=<JSON>`: another input of the manager module, its value as JSON; repeatable.
- `--tag key=value`: a Terraform default tag of the manager's resources; repeatable. The tags given replace the
  install's tags (`--tag ''` alone removes them all); without `--tag`, the stored ones are kept. They are the provider's
  `default_tags` of the manager's resources, after the fixed ones; the bootstrap's resources keep the fixed tags. Keys
  are compared without regard to case, as IAM does: `Project` collides with `project`, `Team` with `team`.
- `--build-role <arn>`: an existing role for CodeBuild instead of the one the install creates; it must trust CodeBuild.
- `--management-profile <profile>`: the management account's profile, for [organization access](#organization-access).
- `--member-role <name>`: the role assumed in each member account from the management account
  (`OrganizationAccountAccessRole` by default).
- `--accounts <id>[,<id>...]`: the member accounts that get the roles (every active account by default).
- `--exclude-accounts <id>[,<id>...]`: member accounts left out.
- `--discovery-only <id>[,<id>...]`: member accounts that get the discovery role but not the command role: the manager
  reads them, and cannot put their workloads to sleep.
- `--access`: runs organization access alone, the manager being installed (needs the management account's profile).
- `--adopt-state <file>`: the state of a manager installed with the Terraform module directly (`terraform state pull`),
  taken over by a first install: see [Adopting a manager installed with Terraform](#adopting-a-manager-installed-with-terraform).
- `--adopt-module <name>`: the manager module's name in that state (`manager` by default, as `module "manager"`).
- `--yes`: takes the suggested answers without asking; never confirms a plan.
- `--verbose`: prints Terraform's lines instead of the status line.

### Organization access

Organization mode needs the delegation policy in the management account and, in each other account, the discovery role
(and the command role, where workloads nap) trusting the manager. They are deployed with the management account's
credentials: `--management-profile <profile>`, or `--profile` when it is the management account's.

```sh
dormeto install --profile <manager-account-profile> --management-profile <management-profile>
```

- The CLI plans it read-only, shows it (the delegation policy as it would be, the accounts and the roles each gets),
  and after your confirmation one build in the management account applies it. That build's role and project
  (`dmt-<env>-organization`) are created by API calls for the run, and deleted when it ends; the bucket stays, with the
  reports. Detached, `dormeto install status` deletes them once the build ends.
- The member role is `--member-role` (`OrganizationAccountAccessRole` by default: the accounts that AWS Organizations
  created have it, the invited ones do not). An account where it cannot be assumed is left out and listed, with the
  procedure for it.
- An account that already holds another manager's Dormeto roles or boundary is left out and listed: a boundary named
  `DmtRouterBoundary` is shared by every Dormeto role, so one account holds one manager's.
- The manager's own account (its roles come from the manager module) and the management account (the member role is not
  there, and AWS advises no workloads in it) get no roles by default. `--accounts`, `--exclude-accounts` and
  `--discovery-only` narrow the rest.
- The delegation policy is merged, not replaced: Dormeto's statement `DormetoReadOrganization` is added or updated, the
  other statements are kept. The organization has one such policy: a Terraform configuration that writes it whole (the
  release's organization module) removes the merged statement at its next apply.

Without the management account's credentials, organization access is disabled: the manager is installed all the same,
but it cannot list the accounts until the delegation policy exists, nor read an account without its roles. Deploy them
with the procedure, <https://dormeto.com/organization-access.html>, and check them with
`dormeto install check --management-profile <profile>`.

Without a terminal, `dormeto install apply --management-profile <profile>` deploys organization access: after the
manager's plan when there is one to apply, alone when there is none.

### Status, upgrade, uninstall

`dormeto install status` shows where the install stands: a running build, followed; the manager's version and the latest
release; the last plan and apply; the dashboard and the invitations; organization access.

An upgrade is `dormeto install` again (`--version <newer>` for a given release): the answers of the last run are reused,
nothing already answered is asked, then the plan is shown and confirmed as before. Organization access runs again when
you give the management profile (its script is idempotent); without it, the release's notes may ask for the roles to
be applied again.

`dormeto uninstall` removes the manager, in this order:

1. It refuses while routers are installed: offboard the workloads and uninstall the routers in the dashboard first. It
   asks you to confirm that event-driven refresh is off.
2. The deletion protection is lifted by a plan, shown, confirmed and applied.
3. The destroy plan is built, shown and confirmed; that saved plan is applied and followed.
4. If you say so, the install's bucket (its state and answers), role, log group and project are removed.

Organization access is kept: its roles keep working for a reinstall. To remove them and Dormeto's statement of the
delegation policy, see <https://dormeto.com/organization-access.html>.

Without a terminal, `dormeto uninstall` stops after each plan: `dormeto install apply` applies the deletion protection's,
`dormeto uninstall apply` the destroy plan, which keeps the bootstrap (a run with a terminal removes it on request).
`dormeto install apply` refuses a destroy plan, and `dormeto uninstall apply` an install plan. `uninstall` takes
`--profile`, `--region`, `--env`, `--verbose` and `--yes`, which here answers that event-driven refresh is off and
keeps the bootstrap; it never confirms a plan.

The plans of `uninstall` run with the version installed, not with a later plan's (an upgrade not applied). When that
version came from a local release, `--release <dir>` gives that release's directory again.

### Updating from the dashboard

A manager installed by CLI 0.2.0 or later with manager 0.12.0 or later can be updated from its dashboard, by its
administrators. The install sets up an EventBridge rule, `dmt-<env>-install-requests`, that starts the install project
when the manager's API drops a request in the install bucket (`<env>/requests/`), through a role,
`dmt-<env>-install-events`, that may start that project only. The rule passes nothing the API wrote: the build reads
its request itself and checks it. The dashboard can request a plan of a newer published version, or of a version staged
by the CLI (`--release`), with the configuration last applied (`applied/`), then the apply of that plan. The build
checks every request (its fields, its version, that the plan it applies is the current one) and refuses what does not
pass; `dormeto install status` shows the dashboard's builds as the last dashboard update. The CLI records its version
in `<env>/cli.json`, which the dashboard reads to know the install can be updated.

`--build-role` leaves it off: the rule, its role and the manager's access to the bucket are not created, and the
dashboard says the install cannot be updated there. `dormeto uninstall` removes the rule and its role. An install made
with an older CLI gets it by running `dormeto install` once more with this one.

### Adopting a manager installed with Terraform

A manager deployed with the Terraform module directly can become an install of the CLI without being recreated: its
users, its history and the registration of its routers stay. The CLI takes the manager module's part of that
configuration's state and makes it the install's own state; Terraform then plans the difference between the resources as
they are and the CLI's configuration.

```sh
terraform -chdir=<the configuration that installed it> state pull > manager.tfstate
dormeto install --profile <profile> --region <region> --env <env> --adopt-state manager.tfstate \
  --admin-emails you@example.com --domain <host> --certificate-arn <arn> --zone-id <id> --var name=<JSON>
```

- Give the inputs of that configuration with the flags and `--var`: the CLI does not read its variables. Read the first
  plan before applying it: it shows any difference, and flags what it replaces or destroys.
- `--adopt-module <name>` names the module in that state when it is not `module "manager"`.
- The checks refuse an environment already installed with the CLI (it has an `install.json` or an `applied.json`), a
  state that is not of format version 4 or that a Terraform newer than the install's wrote, and a state whose module
  does not hold this environment's manager (its function `dmt-<env>-manager-api`) in the profile's account and region.
  The manager found is then the one adopted, not another install; Dormeto's roles found are the adopted manager's when
  the state moves each of them. Another state already at `<env>/manager.tfstate` (an earlier adoption, interrupted)
  stops it too, with how to start over.
- The resources of the module, and of the modules in it, move to the install's module; the resources outside it (a
  certificate, for example) stay in the source and are listed. Its outputs and check results are left out.
- After the bootstrap, before the plan, the install's bucket gets the state as it was read (a backup,
  `<env>/adopted/<UTC time>.tfstate`), then the moved state (`<env>/manager.tfstate`, only when there is none), and
  `install.json` records where it came from (`adoptedFrom`: the file's name and SHA-256; `adoptedModule`).
- The CLI never reads or writes the source's backend. After the first apply, it says how to detach the manager from the
  source: `terraform state rm module.<name>` there, then remove the module from that configuration. While both states
  name the resources, applying the source configuration again would fight the install.
- Until the first apply, `dormeto uninstall` refuses: destroying would delete the adopted manager. To abandon the
  adoption instead, remove `<env>/install.json` and `<env>/manager.tfstate` from the install's bucket; the source state
  is untouched, and the bootstrap can stay or be removed by hand.

### Permissions to install

The install's profile needs administrator rights in the manager's account: the checks simulate the actions of the
bootstrap (the bucket, the role, the project, the log group, and the rule that starts the install build on a dashboard request with its own role) and `iam:PassRole`, and the install is refused when one is
denied. The build role the install creates gets the `AdministratorAccess` managed policy in that account only, since the
manager module creates IAM roles, Lambda functions, a user pool, a distribution and tables; it is trusted by CodeBuild
for the install's project only. `--build-role` gives your own role instead, and `dormeto uninstall` removes the one the
install created.

That role keeps `AdministratorAccess` until `dormeto uninstall` (it serves the upgrades). Anyone allowed
`codebuild:StartBuild` or `codebuild:UpdateProject` on the project `dmt-<env>-install` can therefore run commands as an
administrator of that account: allow those actions to the install's operators only, or give your own role with
`--build-role`.

In the management account, the profile reads the organization (`organizations:DescribeOrganization`, `ListAccounts`,
`DescribeResourcePolicy`), assumes the member role in each chosen account (`sts:AssumeRole`; there `iam:GetRole` and
`GetPolicy` read the Dormeto roles), and creates and deletes the organization build's bucket, role, project and log
group. The build's role has no managed policy: only a scoped one.

- `sts:AssumeRole` on `arn:aws:iam::*:role/<member role>`, in the organization's accounts only (`aws:ResourceOrgID`)
- `organizations:DescribeResourcePolicy`, `PutResourcePolicy`, `ListAccounts`
- `s3:GetObject`, `PutObject`, `ListBucket` on the install's bucket
- `logs:CreateLogStream`, `PutLogEvents` on its log group

Through the member role, the build runs Dormeto's release script, which creates or updates the two Dormeto roles and the
boundary in each account: the member role must be allowed to do that (the role AWS Organizations creates is an
administrator).

## Routers and workloads

From CLI 0.4.0 and manager 0.15.0, `dormeto` calls the manager's API, with the AWS profile that `dormeto install` uses,
on its IAM-authorized `/iam/*` routes (see [Permission](#permission-to-call-the-manager)); nothing is installed. From
CLI 0.6.0, every operation of the manager is a command, a verb per action of the dashboard: this section has the
routers and the workloads, then come [Discovery and exclusions](#discovery-and-exclusions), [Operations](#operations),
[Users and groups](#users-and-groups), [Audit, savings and alerts](#audit-savings-and-alerts),
[The manager](#the-manager) and [Raw calls](#raw-calls). What they all share comes first: where the manager is, how
targets, confirmations, operations and output work, the exit codes and the permission.

```sh
dormeto whoami
dormeto routers list --env dev --outdated
dormeto workloads list --tag team=payments --state ASLEEP
dormeto workloads wake prod-eu/w-1 --wait
dormeto routers upgrade --all --dry-run
dormeto routers upgrade --all --yes --json > upgrade.json   # in CI
```

### Where the manager is

Every manager command takes `--profile` (the AWS profile; the SDK's default), `--region` (the manager's region; the
profile's by default), `--env` (the manager's environment, `prod` by default) and `--api-url`. The API is found by its
name, `dmt-<env>-manager-api`, in the manager's region; `--api-url` gives its URL and skips the lookup. When no API has
that name, the message says which environment and region were searched. `--api-url` is the API's execute-api endpoint
(the module's `api_endpoint` output, `https://<api id>.execute-api.<region>.amazonaws.com`), not the dashboard's URL.

```sh
dormeto whoami [--json]
```

`whoami` shows the caller as the manager names it (the profile's identity, by its ARN) and its role, then the manager
that answered: its version, its API's URL, the environment it was found by (not with `--api-url`) and its region. Run
it first to check the profile and the permission. `--json` prints `{me, version, apiUrl}`.

### What every manager command shares

- **Targets.** A router is its name. A workload is `<router>/<workload id>`; the workload writes take several, or
  `--tag key=value` (repeatable; every workload that carries all the tags, all pages of the listing), never both, and a
  `--tag` that matches nothing exits 2. When a router does not answer (or the listing cannot be completed), the
  workloads resolved are acted on, then the command names what could not be resolved and exits 1 (`--wait` still
  waits for the resolved ones). A discovery candidate is its id (the target group's ARN) or the name of its target
  group when it is unique among the candidates of the status the command works on; an ambiguous name exits 2 and lists
  the matching ARNs. A user is an email, a group a name, an operation and an exclusion their id.
- **Regions.** `--region` is always the manager's region. A router's region is `--router-region` (`routers list`,
  `upgrade`, `install`), a candidate's `--candidate-region` (`discovery candidates`).
- **Confirmation.** On a terminal, a write on several workloads or routers is confirmed, and these always are, even for
  one target: `routers uninstall`, `workloads offboard`, `workloads exclude`, `exclusions add`, `users disable`,
  `users role`, `groups delete`, `discovery config set` and `manager update`. The question names the targets and what
  happens; a workload write lists its targets first (`Offboard 1 workload?`). Without a terminal, `--yes` is required
  (exit 2). A declined confirmation sends nothing and exits 0. `--dry-run`, where a command has it, lists the targets
  and sends nothing.
- **Operations.** Onboarding, offboarding, the install, upgrade and uninstall of routers, exclusions and their lifting,
  and the manager's update are operations of the manager. The CLI starts them and follows them, reading each every 5
  seconds: `started <type> <id> for <target>`, each step on stderr as it changes (`  … install router`,
  `  ✓ verify router (12s)`), then `✓ <target>: <type> succeeded in 3m12s` or
  `✗ <target>: <type> failed at <step>: <detail> (operation <id>)`. A start the manager refuses is printed with its
  message; the others go on, and the exit is 1. An operation that another one holds is refused with that one's id:
  `(operation <id>: dormeto operations watch <id>)`.
- **Following.** `--no-wait` prints `follow it: dormeto operations watch <id>` instead of following. `--timeout` (60m
  by default) and Ctrl-C stop following: the operations go on in the manager, and the CLI prints
  `<target>: still running in the manager: dormeto operations watch <id>` for each one (exit 1; 130 on Ctrl-C). A wake,
  a keep-awake and a discovery run are not operations: `--wait` waits for them, with their own `--timeout`.
- **Durations and times.** `--for` takes a duration: `90m`, `2h`, or whole days first, `3d`, `1d12h`. `--until` takes
  an RFC 3339 time or `HH:MM` today in the local time zone, refused when already past (it never means tomorrow). Each
  command's limit is the manager's: 7 days for `keep-awake`, 366 days for pauses and exclusions.
- **Checks.** A value the manager checks is checked before anything is sent where the rule is known (exit 2, nothing
  sent), never more strictly than the manager does.
- **Output.** Lists are tables; `show` commands print labelled lines. Times in lists are ages (`45m`, `3h`, `2d`); in
  `show`, the audit and operations, local times with their zone (`2026-10-21 14:05 CEST`). The manager's refusals are
  printed as it words them, on stderr.
- **JSON.** Every command has `--json`: the manager's answer, after the CLI's own filters, on stdout alone; lines,
  progress, questions and dry-run lists then go to stderr, so that stdout always parses:
  - a list prints the API's object, its rows under the API's key after the filters, every page in it and no page's
    cursor (`{users: […]}`, `{operations: […]}`, `{candidates: […]}`);
  - a command that starts operations prints them as an array when it takes several targets (`routers uninstall`,
    `workloads offboard` and `exclude`, `exclusions lift`), whatever their number, `[]` when none started, and as an
    object when it takes one (`routers install`, `workloads onboard`, `exclusions add`), `{}` when it did not start;
  - the other writes on several targets print the manager's answers by target (`<router>/<workload>`, or the router),
    `{}` when none succeeded;
  - a dry run prints `{"dryRun": true, "targets": […]}` and a declined confirmation `{"confirmed": false, "targets":
    […]}` (exit 0); `routers upgrade` and `manager update` print their own result instead.

### Exit codes

- 0: success (and a declined confirmation, which sends nothing).
- 1: AWS or the manager refused, an operation or an upgrade failed or did not start, a workload failed to wake, an
  operation or a wait was not followed to its end (`--timeout`), a `--tag` selection was incomplete, or `dormeto api`
  got a status other than 2xx.
- 2: a bad flag or argument, or no `--yes` without a terminal.
- 128 plus the signal's number when SIGINT or SIGTERM stopped the command (130 for Ctrl-C).

### Permission to call the manager

The caller needs `execute-api:Invoke` on the manager API's `/iam/*` routes, with credentials of the manager's account (a
caller from another account assumes a role there that has it). Whatever it may call, such a caller acts as an
administrator of the manager, outside its MFA policy: the audit log, the operations, the pauses and every change name
its ARN. Its IAM policy is the only limit: grant it the routes, by method and path, that its commands call. Three
policies, from the narrowest.

**Read-only.** Every command that only reads: the lists, `show`, `history`, `idle-window`, `schedule show`, `whoami`,
`overview`, `audit`, `operations watch`, `manager status` and `changes`, and the `--dry-run` of the writes (but
`manager update --dry-run`, which plans):

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "execute-api:Invoke",
    "Resource": "arn:aws:execute-api:<region>:<account>:<api id>/*/GET/iam/*"
  }]
}
```

It still reads everything an administrator reads through the `/iam/*` routes: the users and their emails, the audit,
the update state.

**Wake and nap.** Check the caller (`whoami`), read the workloads (`workloads list`, `show`, `history`, `idle-window`,
the `--tag` selections), wake them (`--wait` included) and nap them, and nothing else:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "execute-api:Invoke",
    "Resource": [
      "arn:aws:execute-api:<region>:<account>:<api id>/*/GET/iam/me",
      "arn:aws:execute-api:<region>:<account>:<api id>/*/GET/iam/health",
      "arn:aws:execute-api:<region>:<account>:<api id>/*/GET/iam/workloads",
      "arn:aws:execute-api:<region>:<account>:<api id>/*/GET/iam/workloads/*",
      "arn:aws:execute-api:<region>:<account>:<api id>/*/POST/iam/workloads/*/wake",
      "arn:aws:execute-api:<region>:<account>:<api id>/*/POST/iam/workloads/*/nap"
    ]
  }]
}
```

Add `.../POST/iam/workloads/*/pause` and `.../POST/iam/workloads/*/resume` for `keep-awake`, `pause` and `resume`.

**Every route.** Every command, the writes included. The manager module's output `api_iam_arn` is this resource:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "execute-api:Invoke",
    "Resource": "arn:aws:execute-api:<region>:<account>:<api id>/*/*/iam/*"
  }]
}
```

For another policy, start from the read-only one (every command reads with `GET`) and add the routes of the writes it
needs; in a resource, each `{…}` of a path is `*` (`.../POST/iam/routers/*/pause`):

| Command | Route it writes, under `/iam` |
|---|---|
| `routers install`, `upgrade`, `uninstall`, `workloads onboard`, `offboard` | `POST /operations` |
| `routers pause`, `resume` | `POST /routers/{router}/pause`, `POST /routers/{router}/resume` |
| `workloads wake`, `nap`, `pause` and `keep-awake`, `resume`, `rename` | `POST /workloads/{router}/{id}/wake` (`nap`, `pause`, `resume`, `rename`) |
| `workloads tags` | `PUT /workloads/{router}/{id}/tags` |
| `settings set` | `PUT /settings/workloads` |
| `routers settings` | `PUT /routers/{router}/settings` |
| `workloads settings` | `PUT /workloads/{router}/{id}/settings` |
| `workloads schedule set`, `clear` | `PUT /workloads/{router}/{id}/schedule`, `DELETE` on the same path |
| `workloads exclude`, `exclusions add` | `POST /exclusions` |
| `exclusions lift` | `DELETE /exclusions/{id}` |
| `discovery run`, `dismiss`, `restore` | `POST /discovery/run` (`dismiss`, `restore`) |
| `discovery config set` | `PUT /discovery/config` |
| `users invite` | `POST /users` |
| `users role`, `groups` | `PUT /users/{email}/role`, `PUT /users/{email}/groups` |
| `users disable`, `enable`, `resend`, `reset-mfa` | `POST /users/{email}/disable` (`enable`, `resend`, `mfa/reset`) |
| `groups create`, `update`, `delete` | `POST /groups`, `PUT /groups/{name}`, `DELETE /groups/{name}` |
| `savings discounts` | `PUT /savings/settings` |
| `manager check` | `PUT /update/check` |
| `manager update` | `POST /update/plan`, `POST /update/apply` |

A manager installed with the CLI has no Terraform outputs at hand: find the API id by the API's name, then build the
ARN with it:

```sh
aws apigatewayv2 get-apis --query "Items[?Name=='dmt-<env>-manager-api'].ApiId" --output text
```

To find the API by name, the caller also needs `apigateway:GET`; `--api-url` does without.

Versions: every command needs manager 0.15.0 or later (the `/iam/` routes): an older one answers `Not Found`, and the
CLI says to update it. A command a router is too old for is refused with the version to upgrade it to, printed as the
manager words it: `routers show` and the pauses need routers 0.16.0, `workloads rename` 0.20.0, `workloads tags` 0.30.0,
schedules 0.31.0, hibernation settings 0.32.0 (and manager 0.19.0). The manager refuses to demote or disable its last
enabled administrator from manager 0.18.0 (409, "the last administrator: give the admin role to another user first");
an older manager does not, and the CLI does not check it itself, so there `users role` and `users disable` can leave no
person able to administer the manager.

### routers list

```sh
dormeto routers list [--account <id>]... [--router-region <region>]... [--name <glob>] [--tag k=v]... [--outdated] [--json]
```

Lists each router's name, account, region, source (`manager` or `terraform`), version (`0.29.0 → 0.30.0` when below the
version available, `upgrading` while an upgrade runs), health and workloads (the total and the count per state), then a
last line with the version available and how many routers are outdated. A router that does not answer is listed, its
error on stderr.

- `--outdated`: only the routers below the version available.
- `--account <id>`: only the routers of these AWS accounts.
- `--router-region <region>`: only the routers in these regions (`--region` is the manager's).
- `--name <glob>`: only the routers whose name matches the glob (`*`, `?`, `[…]`).
- `--tag key=value`: only the routers that serve a workload carrying this Dormeto tag.
- `--json`: the API's answer, its rows after the filters.

`--account` and `--router-region` take repeated or comma-separated values, of which one must match. `--tag` is
repeatable and all the tags must be carried by the same workload. The tags are those of the workloads in the manager (see
`workloads list`), not AWS tags on the routers; a router that does not answer has unknown workloads' tags, so a `--tag`
cannot select it (a warning says so).

### routers upgrade

```sh
dormeto routers upgrade [<router>...] [filters] [--all] [--concurrency <n>] [--timeout <d>] [--dry-run] [--yes] [--json]
```

```sh
dormeto routers upgrade --tag team=payments
dormeto routers upgrade --all --dry-run
```

Upgrades the routers named, narrowed by the filters, to the version available. Without a name or a filter, `--all` is
required (exit 2); `--all` takes neither names nor filters. An unknown name exits 2.

It prints the plan: each router to upgrade, from → to, then the routers skipped and why. A router is skipped when it is:

- written in Terraform (upgraded with its Terraform);
- up to date;
- newer than the version available;
- of unknown version (its error is given);
- busy with an operation (the operation's type and id are given).

The plan is confirmed on a terminal. Without one, `--yes` is required (exit 2 after the plan). A declined confirmation
starts nothing and exits 0. `--dry-run` prints the plan and stops. Then one `upgrade-router` operation per router is
started in the manager, at most `--concurrency` at once, and followed every five seconds; each result is printed as it
ends (the version change and the duration, or the step that failed, its detail and the operation's id).

- `--concurrency <n>`: how many upgrades run at once, from 1 to 20 (4 by default).
- `--timeout <duration>`: stop following the upgrades after this long (60m by default). Ctrl-C does the same: the
  upgrades already started go on in the manager, and the CLI says where to follow them (its Operations view).
  Routers not yet started are not started (status `not started`); a network error while following is waited out until the
  timeout.
- `--dry-run`: print the plan and stop.
- `--yes`: do not ask for confirmation.
- `--json`: print the result on stdout as `{available, upgraded: [{router, from, to, operation, status, error?}],
  skipped: [{router, reason}]}`; the plan and the progress go to stderr.
- `--all`, `--account`, `--router-region`, `--name` and `--tag`: the selection, as for `routers list`.

### routers show

```sh
dormeto routers show <router> [--json]
```

Shows one router as it tells its details: its version and build, its health and problems, the idle check (its schedule
and last run), the Lambda and queue figures, the workloads by state, the pauses, the EKS clusters and the IP lists, then
each configuration value with where it is set (`terraform`: the router's module; `manager`). Times are in the local
time zone. `--json` prints the manager's answer.

### routers install and uninstall

```sh
dormeto routers install --account <12 digits> --router-region <region> [--no-wait] [--timeout <d>] [--json]
dormeto routers uninstall <router>... [--dry-run] [--yes] [--no-wait] [--timeout <d>] [--json]
```

```sh
dormeto routers install --account 111122223333 --router-region eu-west-3
dormeto routers uninstall staging-eu --yes --no-wait
```

- `install` starts the manager's install of a router in the account and region (an `install-router` operation) and
  follows it. The account needs the command role the manager assumes: without it, the manager refuses (`no_access`) and
  the CLI prints why.
- `uninstall` starts an `uninstall-router` operation for each router, 3 at once at most, and follows them. It is always
  confirmed on a terminal. The operation's "check empty" step refuses a router that still has workloads: offboard them
  first.
- `--json`: the operations, as the manager last said them: `install`'s as an object (`{}` when it did not start),
  `uninstall`'s as an array (`[]` when none started).

### routers pause and resume

```sh
dormeto routers pause <router>... --reason <text> [--for <duration> | --until <time>] [--keep-asleep] [--dry-run] [--yes] [--json]
dormeto routers resume <router>... [--dry-run] [--yes] [--json]
```

```sh
dormeto routers pause prod-eu --reason "database migration" --for 3h
dormeto routers resume prod-eu
```

- `pause`: none of the routers' workloads is napped automatically, and the workloads asleep are woken unless
  `--keep-asleep` (the woken ones are printed). `--reason` (1 to 256 characters) is shown in the manager. `--for` is up
  to 366 days, `--until` within 366 days; without either, the pause lasts until `routers resume`.
- `resume`: the workloads follow their schedule and idle rules again; a router that was not paused says `not paused`.
- Several routers are confirmed on a terminal. A router refused does not stop the others (exit 1). `--json` prints the
  manager's answers, by router (`{}` when none succeeded, the answers collected when Ctrl-C stops it); the lines go to
  stderr.

### workloads list

```sh
dormeto workloads list [--state <state>]... [--router <name>] [--tag k=v]... [--search <text>] [--json]
```

Lists each workload's router, id, name, state, Dormeto tags (`key=value`, sorted by key) and the time since its last
change, then a last line with the total. A router that does not answer is reported on stderr, the others are listed.

- `--state <state>`: only the workloads in these states (repeatable or comma-separated; case-insensitive).
- `--router <name>`: only the workloads of this router.
- `--tag key=value`: only the workloads with this Dormeto tag (repeatable: all must match).
- `--search <text>`: only the workloads whose name or id contains the text.
- `--json`: the API's answer.

### workloads show, history and idle-window

```sh
dormeto workloads show        <router>/<workload> [--json]
dormeto workloads history     <router>/<workload> [--days 1..31] [--json]
dormeto workloads idle-window <router>/<workload> [--json]
```

- `show`: one workload as the manager's workload view shows it: its state and since when, name, URL, account and region,
  router version and tags, its settings, its pauses (who, why, until when), its schedule with the next events (in the
  schedule's time zone), the share of the last 7 days in each state, its idle hours, and what its naps saved, with the
  energy not used.
- `history`: from the manager's store, never the router: the time in each state over `--days` (1 to 31, 7 by default),
  the wakes (how many, the median and the longest, the failed ones) and the savings estimate, then the latest 50
  changes, newest first, with what started each and how long a wake took. `--json` has every change.
- `idle-window`: the manager's advice on the idle window: the last 14 days of requests replayed against windows from 5
  to 120 minutes, each with its savings a month (hours asleep a day when nothing is priced), its wakes and wait a day,
  the current and the recommended window marked. The manager computes it once a day: the first read may take a while.

### Waking, napping, pausing, renaming and tagging workloads

```sh
dormeto workloads wake   <target>... | --tag k=v... [--wait] [--timeout <d>]
dormeto workloads nap    <target>... | --tag k=v...
dormeto workloads pause  <target>... | --tag k=v... --reason <text> [--for <duration> | --until <time>] [--keep-asleep]
dormeto workloads rename <router>/<workload> <name>
dormeto workloads tags   <target>... | --tag k=v... [--set k=v]... [--unset k]... [--clear]
```

```sh
dormeto workloads wake prod-eu/w-1 --wait
dormeto workloads nap --tag env=dev --yes
dormeto workloads pause --tag team=payments --reason "load test" --until 18:00 --yes
dormeto workloads rename prod-eu/w-1 "checkout api"
dormeto workloads tags prod-eu/w-1 --set team=payments --unset owner
```

- `wake` asks the routers to wake the workloads now. `--wait` reads each one every 5 seconds until `AWAKE`, as
  `keep-awake --wait` does: `FAILED` ends it with the failure (exit 1), `--timeout` (15m by default) names the workloads
  not awake (exit 1), Ctrl-C exits 130.
- `nap` asks the routers to put the workloads to sleep now. A router refuses a workload that is paused or not awake: its
  reason is printed for that workload, and the exit is 1.
- `pause`: the routers do not put the workloads to sleep, and wake them first unless `--keep-asleep`. `--reason` (1 to
  256 characters) is shown in the manager. `--for` is up to 366 days, `--until` within 366 days; without either, the
  pause lasts until `workloads resume`.
- `rename` sets the display name of one workload: 1 to 64 characters, no control characters. Routers 0.20.0 and later;
  the router refuses a workload written by Terraform.
- `tags` reads each workload's tags, empties them with `--clear`, removes every `--unset` key, adds every `--set`, and
  sends them only when they changed (`unchanged` otherwise). Tags are given by flags alone, since a key may hold `/`; a
  key given twice is refused (exit 2). The manager's rules are checked first: at most 50 tags, a key of 1 to 128
  characters, a value of up to 256, letters, digits, spaces and `_ . : / = + - @`, no key starting with `dormeto:`.
  Routers 0.30.0 and later.
- More than one target is confirmed on a terminal, its targets listed before the question; `--yes` skips it and is
  needed without one; `--dry-run` lists the targets and sends nothing. The other workloads go on when one is refused,
  and the exit is 1. `--json` prints the manager's answers of those that succeeded, by `<router>/<workload>` (`{}`
  when none did); the lines and failures go to stderr. For a workload `tags` leaves unchanged, the answer is its tags
  as they are, in the manager's shape: `{workloadId, tags}`.

### Keeping workloads awake and scheduling them

From CLI 0.5.0, `workloads` also keeps workloads awake for a while and sets their weekly schedules (awake and nap
windows, a maximum nap that wakes for maintenance). Targets are `<router>/<workload id>` arguments, or
`--tag key=value`, as for the other workload writes: more than one target is confirmed on a terminal, `--yes` skips it
and is needed without one, `--dry-run` lists the targets and sends nothing, and the other workloads go on when one is
refused (exit 1). From CLI 0.6.0, each of these commands has `--json`, as the other workload writes do.

```sh
dormeto workloads keep-awake <target>... | --tag k=v... (--for <duration> | --until <time>) [--reason <text>] [--wait] [--timeout <d>]
dormeto workloads resume     <target>... | --tag k=v...
dormeto workloads schedule show  <router>/<workload> [--json]
dormeto workloads schedule set   <target>... | --tag k=v... --file <path or ->
dormeto workloads schedule clear <target>... | --tag k=v...
```

```sh
dormeto workloads keep-awake prod-eu/w-1 --for 2h --wait          # in CI: wake, then wait until awake
dormeto workloads keep-awake --tag team=payments --until 18:30 --yes
dormeto workloads resume prod-eu/w-1                              # at the end of the CI job
dormeto workloads schedule set --tag team=payments --file schedule.json --yes
dormeto workloads schedule show prod-eu/w-1
dormeto workloads schedule show prod-eu/w-1 --json | dormeto workloads schedule set prod-eu/w-2 --file -
```

- `keep-awake` posts a pause that leaves the workload awake (`keepAsleep: false`): it is woken when asleep and not put
  to sleep before the end. `--for` is a duration up to 7 days (`90m`, `2h`, `3d`, `1d12h`, `168h`); `--until` is an
  RFC 3339 time or `HH:MM` today in the local time zone, refused when already past (it never means tomorrow) or more
  than 7 days away. One of them is required. `--reason` is the pause's reason ("keep-awake from the CLI" by default).
  It works with any manager 0.15.0 or later: it uses pauses.
- `--wait` reads each workload every 5 seconds until `AWAKE` (a workload whose status answers 4xx, or fails 3 times in a row, is dropped and the exit is 1). A `FAILED` workload ends it with its failure (exit 1),
  `--timeout` (15m by default) names the workloads not awake (exit 1), Ctrl-C exits 130. In each case the pause stays:
  `workloads resume` ends it.
- `resume` ends the pause, a keep-awake included.
- `schedule set` sends the file as it is (`-` for standard input) and the router validates it, its refusal is printed as
  it words it (exit 1). The file is the schedule's JSON: `timeZone` (an IANA name), `windows` (`kind` `awake` or `nap`,
  `days` from `mon` to `sun`, `start` and `end` as `HH:MM` in steps of 5 minutes, an `end` not after the `start` ends
  the next day, `wake` `never` or `header` for a nap), `exceptions` (local dates) and `maxNapDays` (1 to 60). It
  prints the next events. It needs manager 0.16.0 and routers 0.31.0 (older ones answer "upgrade the router to 0.31.0
  or later").
- `schedule show` prints the time zone, the windows (`awake mon–fri 08:30–19:00`), the exceptions, the maximum nap
  and the next events in the schedule's time zone, every time labelled with its zone (`Mon 26 Oct 08:00 CET`; UTC,
  with a note, when the zone is unknown to the CLI). `--json` prints `{schedule, next, maintenanceDue}`: `schedule`
  is left out for a router before 0.31.0 (the text output then says "the router does not report schedules (upgrade
  it to 0.31.0 or later)") and is `null` when none. `schedule set --file -` takes that shape back (or a bare
  schedule) and sends its `schedule` member (`by` and `at` in it are accepted by the router); a file with no
  schedule (`null`, `{"schedule": null}`, or the output of a router before 0.31.0) is refused with exit 2.
- `schedule clear` removes the schedule.

### Hibernation

```sh
dormeto settings show [--json]
dormeto settings set --hibernate true|false [--json]
dormeto routers settings <router>... --hibernate true|false|inherit [--dry-run] [--yes] [--json]
dormeto workloads settings <target>... | --tag k=v... --hibernate true|false|inherit [--dry-run] [--yes] [--json]
```

```sh
dormeto settings show
dormeto settings set --hibernate false
dormeto routers settings prod-eu --hibernate inherit
dormeto workloads settings --tag env=dev --hibernate true --yes
```

From CLI 0.7.0, whether the EC2 instances that were launched with hibernation are hibernated, not stopped, when a
workload naps is set at three levels; the most specific that is set wins: the workload, then its router, then the
manager's default (on, for a router that reads none). Dormeto never launches or changes an instance: every other
instance is stopped as before. A workload with fallback instance types does not hibernate unless it is set true on the
workload itself (a hibernated instance cannot change type).

- `settings show` prints the manager's default and who changed it and when: `Hibernate: on (changed by ann at 2026-10-19
  10:00 CEST)`. `settings set` changes it (administrators) and prints what the manager says: the routers apply it at
  their next check, within 5 minutes.
- `routers settings` sets one router's value for the workloads that set none, `inherit` to follow the manager's default
  (administrators; routers 0.32.0 and later, an older one is refused with the version to upgrade it to).
- `workloads settings` sets a workload's own value, `inherit` to follow its router's (operators and administrators;
  routers 0.32.0 and later). A workload written by Terraform is refused (`set hibernate in its workload module`).
- Several routers or workloads are confirmed on a terminal; `--yes` skips it and is needed without one; `--dry-run`
  lists the targets and sends nothing. A refusal is printed for its target, the others go on (exit 1). `--json` prints
  the manager's answers (by router, or by `<router>/<workload>`).
- `routers show` prints a `hibernate` line in its configuration: its own value, or `inherit (manager: on, read 2m ago)`
  (an error of the last read after it), or `inherit (built-in default: on)` for a router that reads no manager
  settings. `workloads show` prints the value and where it comes from (`on, from the router`) and, when the router lists
  the instances, `2 of 3 instances were launched with hibernation; i-0abc is stopped instead`. Nothing is printed for a
  router before 0.32.0.

### Onboarding, offboarding and excluding workloads

```sh
dormeto workloads onboard <candidate> [--name <name>] [--idle-minutes <5..1440>] [--wake-mode any|browser]
                          [--database <id>|cluster:<id>]... [--fallback-type <type>]... [--set-tag k=v]...
                          [--hibernate true|false]
dormeto workloads offboard <target>... | --tag k=v... [--concurrency <n>] [--dry-run] [--yes]
dormeto workloads exclude  <target>... | --tag k=v... --reason <text> [--for <duration> | --until <time>] [--dry-run] [--yes]
```

All three take `--no-wait`, `--timeout <d>` and `--json`: the operations, as the manager last said them, `onboard`'s
as an object, `offboard`'s and `exclude`'s as an array.

```sh
dormeto discovery candidates
dormeto workloads onboard shop-staging --idle-minutes 30 --database cluster:shop-aurora --set-tag team=shop
dormeto workloads offboard prod-eu/w-1 --yes
dormeto workloads exclude prod-eu/w-2 --reason "under migration" --for 14d --yes
```

- `onboard` starts the onboarding of a candidate pending onboarding, given by its id (the target group's ARN) or the
  name of its target group, follows it, then prints the new workload as `onboarded <router>/<workload id>`. An unknown
  or ambiguous name exits 2 (its ARNs listed) and sends nothing. It sends what the dashboard's Onboard dialog sends:
  - `--name`: the workload's display name, up to 64 characters (the target group's name by default, or when empty);
  - `--idle-minutes`: the idle window, 5 to 1440 minutes (15 by default);
  - `--wake-mode`: `any` request that passes the filters (the default), or `browser` (only the waiting page's script
    or the `x-dormeto-wake: 1` header wakes it);
  - `--database`: a companion RDS instance identifier, or `cluster:<identifier>`, stopped after the application and
    started before it (repeatable);
  - `--fallback-type`: an EC2 instance type, such as `t3a.medium`, tried in order at a wake when AWS has no capacity
    for the instance's own type (at most 5; for the EC2 instances of a workload);
  - `--set-tag key=value`: a Dormeto tag of the new workload (repeatable). It is `--set-tag`, not `--tag`, which selects
    targets everywhere else;
  - `--hibernate true|false`: hibernation of its EC2 instances (see [Hibernation](#hibernation)); without it the
    workload inherits its router's setting, then the manager's. `inherit` is not a value here: leave the flag out.
- `offboard` starts the offboarding of each workload, at most `--concurrency` at once (1 to 20, 4 by default): the
  workload is woken, its application given back as it was, and it leaves Dormeto. It is always confirmed on a terminal.
- `exclude` excludes each workload's target group from discovery, with a reason: the manager pauses its managed
  workloads (an `exclude` operation). `--reason` is 1 to 246 characters; `--for` is up to 366 days, `--until` within
  366 days; without either, the exclusion lasts until `dormeto exclusions lift`. It is always confirmed on a terminal.
- A workload another operation holds is refused with that operation's id; the others go on (exit 1).

## Discovery and exclusions

From CLI 0.6.0, `discovery` shows what the manager's discovery found, runs it, dismisses and restores candidates and
replaces its configuration, and `exclusions` lists, adds and lifts the manual exclusions. Targets, confirmations,
operations and output are as in [What every manager command shares](#what-every-manager-command-shares).

```sh
dormeto discovery status [--json]
dormeto discovery run [--wait] [--timeout <d>] [--json]
dormeto discovery candidates [--status <status>] [--account <id> [--candidate-region <region>]] [--limit <n>] [--json]
dormeto discovery dismiss <candidate>... [--note <text>] [--json]
dormeto discovery restore <candidate>... [--json]
dormeto discovery access [--limit <n>] [--json]
dormeto discovery config show [--json]
dormeto discovery config set --file <path>|- [--yes] [--json]
dormeto exclusions list [--json]
dormeto exclusions add (--target-group <arn> | --load-balancer <arn> | --account <id>) --reason <text> [--for <d> | --until <time>]
dormeto exclusions lift <id>... [--no-wait] [--timeout <d>] [--json]
```

```sh
dormeto discovery run --wait
dormeto discovery candidates --account 111122223333 --candidate-region eu-west-1
dormeto discovery dismiss legacy-admin --note "decommissioned in December"
dormeto discovery config show > discovery.json
dormeto discovery config set --file discovery.json
dormeto exclusions add --account 444455556666 --reason "production account" --yes
dormeto exclusions lift 5f0c2b7e-1d4a-4e8b-9c3f-7a2d6e1b8c40
```

### discovery status and run

- `status` shows the last run (when, by whom, how many account-regions it scanned, skipped and failed), a Run now
  refused since, the candidates per status, the roles the manager uses and what the lint found. Times are in the local
  time zone.
- `run` asks the manager for a run now; it runs on its own. With `--wait`, the last run is read every 5 seconds until a
  run newer than the request has ended, then printed as `status` does; a run the manager refused, because another was in
  progress, exits 1. A read that fails for a passing reason (a network error, 429, 5xx) is waited out, as the following
  of an operation does. Ctrl-C and `--timeout` (30m by default) stop waiting, the run going on in the manager. `--json`
  prints the manager's answer, with `--wait` the status once the run ended.

### discovery candidates, dismiss and restore

- `candidates` lists the candidates of one `--status` (`pending-onboarding` by default; `pending-offboarding`,
  `managed`, `ineligible`, `dismissed`, `excluded`, `out-of-scope`, `gone`): target group, account, region, hosts, idle
  hours and requests over 7 days, the workload or the operation running on it, and why it has that status. Every page
  is read unless `--limit` stops at that many (0, the default, reads all). `--account` keeps one AWS account,
  `--candidate-region` one region of it (it needs `--account`; `--region` is the manager's). `--json` prints the
  manager's answer, `{candidates}`, every page in it.
- `dismiss` takes candidates out of the pending onboarding, each given by its id, sent as it is whatever the
  candidate's status, or the name of its target group, resolved among the candidates pending onboarding before anything
  is sent: an unknown name, or one that names the target groups of several accounts or regions (their ARNs are listed),
  exits 2 and nothing is dismissed. `--note` says why (cut at 256 characters).
- `restore` brings dismissed candidates back, each id or name resolved among the dismissed ones in the same way.
- `dismiss` and `restore` print, with `--json`, the candidates as the manager answers them.

### discovery access

`access` shows the organization access of the last run: how many accounts are ready, the delegation policy, the event
forwarders, and a table of the accounts that are not ready with what is missing (the discovery or the command role).
Every page is read unless `--limit` stops at that many accounts (0, the default, reads all). `--json` prints the
manager's answer, every page in it.

### discovery config show and set

- `config show` prints the configuration as JSON alone on stdout, so that `config show > discovery.json` gives a file
  that `config set` takes, and what the lint finds in it on stderr. `--json` prints the manager's whole answer instead:
  the configuration as its `config` member, who set it and when, the lint.
- `config set --file <path>` replaces the whole configuration with the file (`-` reads standard input): the output of
  `config show`, or the answer of `config show --json`, whose `config` member is sent. It is always confirmed on a
  terminal. The manager checks it, and a refusal prints each of its errors on a line (exit 1).

### exclusions

- `exclusions list` lists the manual exclusions: id, target, reason, who excluded it, until when (local time zone), the
  workloads it paused by name, and its state (`active`, `expired`, or `lifting` while a lift runs). `--json` prints the
  manager's answer, `{exclusions}`.
- `exclusions add` excludes one target from discovery: an application target group (`--target-group <arn>`), an
  application load balancer (`--load-balancer <arn>`) or a whole account (`--account <12 digits>`). The manager pauses
  its managed workloads (an `exclude` operation, followed). `--reason` is 1 to 246 characters; `--for` is up to 366
  days, `--until` within 366 days; without either, the exclusion lasts until it is lifted. It is always confirmed on a
  terminal; `--dry-run` names the target and sends nothing. It takes `--no-wait`, `--timeout`, `--yes` and `--json` (its
  operation, an object).
- `exclusions lift <id>...` lifts each exclusion (its id is in `exclusions list`) and follows the operations: the
  workloads it paused are resumed, then the exclusion is deleted. One that stays listed (a workload could not be
  resumed) is lifted again by running the command again. An exclusion another operation holds is refused with that
  operation's id, the others go on (exit 1). `--json` prints the operations, an array.

## Operations

From CLI 0.6.0, `operations` lists the manager's operations, shows one and follows one, started from the CLI or the
dashboard.

```sh
dormeto operations list [--running] [--type <type>]... [--limit <n>] [--json]
dormeto operations show  <id> [--json]
dormeto operations watch <id> [--timeout <d>] [--json]
```

```sh
dormeto operations list --running
dormeto operations list --type install-router,uninstall-router --limit 0
dormeto operations watch 5f0c2b7e-1d4a-4e8b-9c3f-7a2d6e1b8c40
```

- `list` lists the operations, newest first by creation: id, type, target, status, current step, who started it (an ARN
  shortened to its last two parts) and its age. `--running` keeps those still running; `--type` the types given
  (repeatable or comma-separated: `onboard`, `offboard`, `install-router`, `upgrade-router`, `uninstall-router`,
  `exclude`, `lift-exclusion`, `update-manager`, ...); `--limit` caps the rows (50 by default; 0 for all). `--json`
  prints the manager's answer, `{operations}`, its records after these filters.
- `show` prints one operation: type, target, status, who started it, its times (local time zone) and error, then its
  steps with their status, how long each took and what it says.
- `watch` follows an operation, printing its steps on stderr as they change, until it ends: exit 0 when it succeeded, 1
  when it failed (with the failed step's detail). One already finished prints its outcome at once. Ctrl-C and
  `--timeout` (60m by default) stop following, the operation going on in the manager (exit 1; 130 on Ctrl-C). `--json`
  prints the operation as last read, on stdout. It is what `--no-wait` and a refused conflict point to.

## Users and groups

From CLI 0.6.0, `users` and `groups` manage the people of the manager's dashboard and their custom groups. They manage
Cognito users: the CLI's own caller is an IAM principal, never one of them.

```sh
dormeto users list [--json]
dormeto users invite <email> --role <member|viewer|operator|admin> [--group <name>]... [--json]
dormeto users role <email> <member|viewer|operator|admin> [--dry-run] [--yes] [--json]
dormeto users groups <email> [<name>...] [--json]
dormeto users disable <email> [--dry-run] [--yes] [--json]
dormeto users enable <email> [--json]
dormeto users resend <email> [--json]
dormeto users reset-mfa <email> [--json]
dormeto groups list [--json]
dormeto groups show <name> [--json]
dormeto groups create <name> [--description <text>] [--grant <tags>:<permissions>]... [--json]
dormeto groups update <name> [--description <text>] [--grant <tags>:<permissions>]... [--no-grants] [--json]
dormeto groups delete <name> [--dry-run] [--yes] [--json]
```

```sh
dormeto groups create payments-dev --description "Payments, dev" --grant team=payments,env=dev:read,wake
dormeto users invite ana@example.com --role member --group payments-dev
dormeto users role ana@example.com operator
dormeto users groups ana@example.com          # no group: clears them
```

### users

- `list`: email, role, custom groups, status (`disabled` when they are), whether their authenticator is set up, and
  the age of the account. `--json` prints the manager's answer, `{users}`.
- `invite`: the manager emails the user a temporary password. `--role` is `member` (sees only what their groups grant),
  `viewer`, `operator` or `admin`; `--group`, repeated, puts them in existing custom groups. The answer says whether
  the invitation was sent or waits for the address to be verified.
- `role` sets the user's built-in role. `groups` sets their custom groups to those named, replacing their groups;
  naming none clears them (it applies at their next sign-in, within an hour).
- `disable` stops the user from signing in, `enable` lets them sign in again, `resend` sends the invitation again to a
  user who has not signed in yet, `reset-mfa` resets their authenticator (they set it up again at their next sign-in).
- `role` and `disable` are always confirmed on a terminal. From manager 0.18.0, the manager refuses to demote or
  disable its last enabled administrator, and the CLI prints its refusal (exit 1); see
  [Permission](#permission-to-call-the-manager) for older managers.

### groups

- `list`: name, description, how many grants and the age of the last change (`--json`: the manager's answer,
  `{groups}`). `show`: the description, who changed it last and when, and each grant.
- `create`: the name is 1 to 48 lower-case letters, digits and hyphens, not starting with `dmt-`; the description at
  most 200 characters.
- `update` changes the description, or replaces the grants: the grants given replace the group's (take `groups show`
  first to keep some); `--no-grants` removes them all. Give at least one of `--description`, `--grant` and
  `--no-grants`.
- A grant is `<tags>:<permissions>`, split at the last `:`: the permissions `read`, `wake` and `nap`, comma-separated,
  on the workloads that carry all the tags, `key=value` comma-separated (none: every workload), such as
  `team=payments,env=dev:read,wake` or `:read`. At most 20 grants.
- `delete` removes the group and its grants: its members lose what it granted at their next sign-in. It is always
  confirmed on a terminal.

## Audit, savings and alerts

From CLI 0.6.0, the dashboard's readings: the overview, the audit log, the savings and their discounts, the alerts and
the IP lists.

```sh
dormeto overview [--json]
dormeto audit [--since <duration|time|date>] [--action <action>]... [--who <text>] [--target <text>] [--limit <n>] [--json]
dormeto savings show [--json]
dormeto savings discounts --compute <0..90> --databases <0..90> [--json]
dormeto alerts list [--json]
dormeto ip-lists list [--json]
```

```sh
dormeto overview
dormeto audit --since 2d --action pause,resume
dormeto audit --who ci-deploy --limit 0 --json > audit.json
dormeto savings discounts --compute 20 --databases 15
```

- `overview` shows what the dashboard's Overview shows: the workloads by state, the share of the last 7 days in each
  state, the estimated savings, the open alerts, the running operations and the routers that are outdated. It reads six
  routes: one that fails is reported on stderr and its block left out (exit 1 at the end); one the manager does not have
  (404) is "not available" (`null` in `--json`). `--json` prints `{workloads: {total, byState}, states, savings,
  alerts: {open}, operations: {running}, routers: {total, outdated, available}}`.
- `audit` shows the audit log, newest first (times in the local time zone): when, who (an ARN shortened to its last two
  parts), action, target and result, of every mutating call and refused action. The manager sends it whole; the filters
  are the CLI's: `--since` a duration back from now (`2d`, `12h`), an RFC 3339 time or a date (`YYYY-MM-DD`, local
  midnight); `--action` an action's name (repeatable or comma-separated, such as `pause` or `operation-onboard`);
  `--who` and `--target` a part of the text, in any case; `--limit` the newest entries only (100 by default; 0 for
  all). `--json` prints `{entries}` as the manager sends them, after the filters.
- `savings show` shows the estimate of the daily job as the Savings card does: the last 30 days, this month, since the
  first day computed, Dormeto's own cost and the net, the top savers, what is not priced, and the discounts for
  commitments the prices are reduced by.
- `savings discounts` sets those discounts, in whole percent from 0 to 90, off the list prices of compute (EC2,
  Fargate, EKS nodes) and of databases. Both are required (what is not given would be reset); they apply from the next
  day the job computes.
- `alerts list` lists the open health alarms (level, kind, name, age, problems), then the alert topic's subscriptions:
  how many are confirmed and pending, and each endpoint, masked. `no alerts configured` when the manager has none.
- `ip-lists list` lists the manager's IP lists: name, action (`defer`: a request from a listed address does not wake a
  workload by itself; `ignore`: it never wakes one), entries, the age of the last download and its status. They are set
  with `ip_lists` in the manager's Terraform.

## The manager

From CLI 0.6.0, `manager` shows the manager's version and its update state, turns its daily check on or off, prints the
release notes and updates it, as the dashboard's Update page does. A manager installed with the CLI is updated by its
install build (see [Updating from the dashboard](#updating-from-the-dashboard)); one installed with the Terraform module
is updated by changing the module's `source`.

```sh
dormeto manager status [--json]
dormeto manager check (--enable [--alert | --no-alert] | --disable [--no-alert]) [--json]
dormeto manager changes [--to <version>] [--json]
dormeto manager update [--version <version>] [--dry-run] [--yes] [--no-wait] [--timeout <d>] [--json]
```

```sh
dormeto manager status
dormeto manager changes
dormeto manager update --dry-run
dormeto manager update --version 0.18.0
```

- `status` shows the version the manager runs, how it was installed (with the CLI, whose install build updates it, or
  with Terraform), the version last applied, the newer versions (with their router version, or why one cannot be
  applied from the manager), the last install build, a request waiting for its build, the plan ready to apply and its
  summary, and the daily check (`not available` in a manager without it). `--json` prints `{version, update, check}`.
- `check --enable` checks daily for new versions, `--disable` stops it (and its alert: `--no-alert` may say so,
  `--alert` is refused). `--alert` also sends a new version to the alert topic, `--no-alert` stops that; with
  `--enable` alone the alert stays as it is.
- `changes` prints the release notes of the versions the manager offers, newest first, up to `--to` (the newest by
  default), with a note where a version asks for an access change that the update does not apply. A manager installed
  with the CLI only.
- `update` plans the update to `--version` (the newest the manager offers by default) in the install build and follows
  it, prints the plan once the manager shows it (the resources to add, change and destroy, those replaced or
  destroyed), then, once confirmed (always on a terminal; `--yes` without one), applies that plan and follows it, and
  reads the version the manager then runs. `--dry-run` stops after the plan. `--no-wait` starts the apply without
  following it (the plan is always followed: its summary is what is confirmed). `--timeout` (60m by default) applies to
  the plan and to the apply each; while the apply replaces the manager's API, a read that fails is tried again until
  then, each new error said once on stderr. A manager installed with Terraform is refused with that advice (exit 1).
  `--json` prints `{update, operations}`: the update's state as last read and the operations followed.

## Raw calls

From CLI 0.6.0, `dormeto api` calls any route of the manager's API, signed with the profile, and prints its answer: the
escape hatch for what a later manager adds before the CLI has a command for it.

```sh
dormeto api <method> <path> [--data <json> | --data @<file> | --data -] [-q key=value]...
```

```sh
dormeto api GET /workloads -q state=ASLEEP -q limit=10
dormeto api GET /routers/prod-eu
dormeto api POST /routers/prod-eu/pause --data '{"reason":"maintenance"}'
dormeto api PUT /discovery/config --data @discovery.json
```

- The method is `GET`, `POST`, `PUT` or `DELETE`; the path starts with `/` and is the route's under `/iam` (an `/iam`
  prefix is dropped), percent-encoded where a segment needs it. `-q key=value` adds a query parameter (repeatable).
- `--data` is the body, sent as it is: JSON, `@<file>`, or `-` for standard input.
- The answer's body is printed on stdout, indented when it is JSON. A `GET` answered 429 or 5xx is tried up to 3 times
  in all. Any status but 2xx prints `HTTP <status>` on stderr (the body still on stdout) and exits 1.
- `--json` changes nothing: the body is the API's answer. It is accepted as every command accepts it.

## Building from source

Go 1.26 or later; Node.js for the release scripts' tests; GoReleaser 2 for `make snapshot`. `make` lists the targets:

```
make build                                          # bin/dormeto, as a release builds it
make run ARGS="estimate --profile <profile> --uptime"
make test                                           # go vet, the Go tests (shared cases, end to end), the scripts' tests
make install                                        # into ~/.local/bin (INSTALL_DIR), then which dormeto runs
make uninstall                                      # removes it
make snapshot                                       # dist/: the release's archives, checksums and cask
make parity                                         # the shared cases exported again from ../manager
make dry-run VERSION=0.1.0                          # a release's checks, nothing published
```

## Releasing

For maintainers. `scripts/release.sh <version>` (for example `0.1.0`, without a `v`) checks the repository, tags,
publishes the GitHub release and the Homebrew tap with GoReleaser, and checks the result; `--dry-run` checks and builds
without publishing, tagging or reading the token. The version needs a `## <version>` section in
[CHANGELOG.md](CHANGELOG.md). npm is not a channel: 0.1.0's packages (`@dormeto/cli`) are deprecated, and no other
version is published there.

The checks after it, `scripts/after-release.sh <version>` (safe to run again), install the version through each channel
and run it: the release's archives against `checksums.txt`, the Homebrew cask (installed, or upgraded when it is), the
install script and `go install`. The install script is `https://dormeto.com/install.sh`, or, while that does not answer
(the website not deployed yet), the website checkout's `site/install.sh`: `DORMETO_WEBSITE_DIR`, by default
`../website` beside this repository.

`scripts/test-brew-linux.sh [arm64] [amd64]` is an opt-in check of the Homebrew cask on Linux, with podman: it builds a
snapshot, checks the cask's Linux entries against `checksums.txt`, serves `dist/` locally, and in a `homebrew/brew`
container per architecture (by default the machine's own; Homebrew hangs under amd64 emulation on an ARM Mac) installs
the generated cask from a local tap and runs `dormeto version`. It removes its containers and server on every exit.

It publishes to two repositories of the Dormeto GitHub account (a personal account, not an organization), both public,
since Homebrew, the install script and `go install` download without a token: `Dormeto/cli` and the tap,
`Dormeto/homebrew-tap` (the name `brew` turns into `dormeto/tap`), created with a README so that GoReleaser adds the
cask, `Casks/dormeto.rb`, to a `main` branch that exists.

It reads one token from the macOS Keychain, the item `github-dormeto` for the account `$USER`. It is never written to
a repository or to a log. Create it, then store it with `security`, which asks for the value (`-w` last, with no value)
so it does not appear in a command line or the shell's history: on GitHub, signed in as Dormeto, Settings, Developer
settings, Personal access tokens, Fine-grained tokens, Generate new token, with Dormeto as resource owner, "Only select
repositories" `cli` and `homebrew-tap`, the Contents repository permission at Read and write (Metadata, read-only, comes
with it), and an expiration of 90 days.

```sh
security add-generic-password -U -a "$USER" -s github-dormeto -w
```

The release checks the token before it tags anything; an expired or revoked one stops it with a message. That check
proves only that the token is valid (both repositories are public): its write grant is proven by the release itself,
and a refusal there stops the release with the state report, what was done and the commands that finish it.

## License

[MIT](LICENSE)
