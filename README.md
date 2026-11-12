<div align="center">

<a href="https://dormeto.com"><img src="https://dormeto.com/img/og.png" alt="Dormeto: your idle web apps nap, the first visitor wakes them" width="760"></a>

# Dormeto CLI

**Estimate what Dormeto would save on your AWS accounts, from your laptop, before anything is installed.**

[Website](https://dormeto.com) · [Get started](https://dormeto.com/get-started.html) · [Product](https://dormeto.com/product.html) · [Security](https://dormeto.com/security.html) · [Manager](https://github.com/Dormeto/manager) · [Router](https://github.com/Dormeto/router)

![License](https://img.shields.io/badge/license-MIT-3546c4)
![Go](https://img.shields.io/badge/Go-single_binary-00ADD8?logo=go&logoColor=white)
![AWS](https://img.shields.io/badge/AWS-estimate_read--only-FF9900?logo=amazonwebservices&logoColor=white)

</div>

`dormeto` is the Dormeto command line. `dormeto aws estimate` reads an AWS account or a whole organization with read-only
access and estimates what Dormeto would save on the web workloads behind your Application Load Balancers (EC2, Auto
Scaling, ECS, EKS, and the RDS databases that nap with them): the workloads that could nap, how long each would sleep and
wake, and the monthly saving net of Dormeto's own cost. It gives a summary in the terminal and a Markdown and a JSON
report. It installs nothing in your accounts and sends nothing anywhere but to AWS.

`dormeto aws install` then installs the Dormeto manager in your AWS account with CodeBuild, after its checks and your
confirmation of the plan (see [Install the manager](#install-the-manager)). It is the only command that changes your
accounts itself.

Once the manager runs, every operation of its dashboard is a command too, with the same AWS profile: routers, workloads,
discovery, operations, users, the audit, the manager's update (see [Routers and workloads](#routers-and-workloads) and
the sections after it). These ask the manager, which acts as it does for the dashboard.

Every command above, with all of its subcommands and options, is under `dormeto aws`, as the Google Cloud ones are under
`dormeto gcp` (`dormeto --help` lists `aws`, `gcp` and `version`). Before the CLI that has `aws`, they were top-level
(`dormeto install`, `dormeto routers list`); those names are gone: `dormeto install` is an unknown command, and the
commands are `dormeto aws install`, `dormeto aws routers list`.

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
dormeto aws estimate --profile <profile> --uptime
```

`--uptime` also measures when each EC2 instance, ECS service and database actually ran, so the estimate does not count
the hours a schedule already stops them. The first run reads every account and region: minutes to an hour for a large
organization. A second run reads the [cache](#cache). `dormeto aws estimate --help` prints the options.

## Options

```
Usage: dormeto aws estimate --profile <profile>[,<profile>...] [--accounts 111111111111,222222222222]
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
cluster (the `AmazonEKSViewPolicy` access policy is enough). A cluster it cannot read is listed with the reason. A
cluster whose endpoint is private (private access on, public endpoint closed or limited to some addresses) is read by
the router through a proxy function in its VPC, so the estimate, which runs before any install, lists it with the reason
to install the router of its account and region; one with private access off and a limited public endpoint says to turn
private access on.

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

That is the estimate's promise, and it holds. `dormeto aws install` also downloads from `releases.dormeto.com`
(`latest.json`, then the release's `SHA256SUMS` and the release, checked against it). Its builds, in your account,
download Terraform from `releases.hashicorp.com` and the providers through the Terraform registry, with Terraform's
checkpoint (its version check) off. The CLI and the builds use only your credentials and your account; still no
telemetry.

## Install the manager

`dormeto aws install` installs the Dormeto manager in an AWS account, with CodeBuild: Terraform runs in your account, not
on your machine. It asks what it needs, checks what can fail before anything is created, shows the plan, applies it once
you confirm, and follows the build. It needs a manager release 0.11.0 or later (an account outside any organization
needs 0.11.0). In order:

1. **Where it runs.** It reads the profile's account and its organization, and suggests the mode: `organization` (the
   manager reads every account of the organization) in an organization with all features, else `account` (the manager
   reads its own account only, with why). A manager in the organization's management account is allowed after a warning.
2. **The questions.** The release (the latest by default), the region (the profile's, else `eu-west-1` is suggested),
   the environment (`prod`), the administrators' emails, the mode, the management account's profile, a custom domain
   and SES. Each has a flag; a question already answered by a flag or a previous run is not asked.
3. **The checks**, read-only (`dormeto aws install check` runs them alone): credentials, administrator rights (simulated),
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
   and `dormeto aws install status` follows it again. A failure prints the last lines of the log and where to read the rest.
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
dormeto aws install --profile <profile>
```

Without a terminal (a script, a pipeline), every answer comes from a flag. Answers that have a default take it: the
latest release, the suggested mode, no domain and no SES, the environment `prod`; only the administrators' emails are
required. The run stops after the plan, prints what it would do and the command that applies it:

```sh
dormeto aws install --profile <profile> --admin-emails you@example.com
dormeto aws install apply --profile <profile>
```

`dormeto aws install apply` is the confirmation when you run it yourself: it shows the last saved plan, applies it and
follows the build. It refuses a plan made for an uninstall. `--yes` takes the suggested answers; it never confirms a plan.

### Flags

`dormeto aws install check`, `apply` and `status` take the same flags, except `--adopt-state` and `--adopt-module`.
`dormeto aws install --help` prints them.

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
dormeto aws install --profile <manager-account-profile> --management-profile <management-profile>
```

- The CLI plans it read-only, shows it (the delegation policy as it would be, the accounts and the roles each gets),
  and after your confirmation one build in the management account applies it. That build's role and project
  (`dmt-<env>-organization`) are created by API calls for the run, and deleted when it ends; the bucket stays, with the
  reports. Detached, `dormeto aws install status` deletes them once the build ends.
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
with the procedure on the website's Access page, <https://dormeto.com/organization-access.html>, and check them with
`dormeto aws install check --management-profile <profile>`.

Without a terminal, `dormeto aws install apply --management-profile <profile>` deploys organization access: after the
manager's plan when there is one to apply, alone when there is none.

### Status, upgrade, uninstall

`dormeto aws install status` shows where the install stands: a running build, followed; the manager's version and the latest
release; the last plan and apply; the dashboard and the invitations; organization access.

An upgrade is `dormeto aws install` again (`--version <newer>` for a given release): the answers of the last run are reused,
nothing already answered is asked, then the plan is shown and confirmed as before. Organization access runs again when
you give the management profile (its script is idempotent); without it, the release's notes may ask for the roles to
be applied again.

`dormeto aws uninstall` removes the manager, in this order:

1. It refuses while routers are installed: offboard the workloads and uninstall the routers in the dashboard first. It
   asks you to confirm that event-driven refresh is off.
2. The deletion protection is lifted by a plan, shown, confirmed and applied.
3. The destroy plan is built, shown and confirmed; that saved plan is applied and followed.
4. If you say so, the install's bucket (its state and answers), role, log group and project are removed.

Organization access is kept: its roles keep working for a reinstall. To remove them and Dormeto's statement of the
delegation policy, see the website's Access page, <https://dormeto.com/organization-access.html>.

Without a terminal, `dormeto aws uninstall` stops after each plan: `dormeto aws install apply` applies the deletion protection's,
`dormeto aws uninstall apply` the destroy plan, which keeps the bootstrap (a run with a terminal removes it on request).
`dormeto aws install apply` refuses a destroy plan, and `dormeto aws uninstall apply` an install plan. `uninstall` takes
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
pass; `dormeto aws install status` shows the dashboard's builds as the last dashboard update. The CLI records its version
in `<env>/cli.json`, which the dashboard reads to know the install can be updated.

`--build-role` leaves it off: the rule, its role and the manager's access to the bucket are not created, and the
dashboard says the install cannot be updated there. `dormeto aws uninstall` removes the rule and its role. An install made
with an older CLI gets it by running `dormeto aws install` once more with this one.

### Adopting a manager installed with Terraform

A manager deployed with the Terraform module directly can become an install of the CLI without being recreated: its
users, its history and the registration of its routers stay. The CLI takes the manager module's part of that
configuration's state and makes it the install's own state; Terraform then plans the difference between the resources as
they are and the CLI's configuration.

```sh
terraform -chdir=<the configuration that installed it> state pull > manager.tfstate
dormeto aws install --profile <profile> --region <region> --env <env> --adopt-state manager.tfstate \
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
- Until the first apply, `dormeto aws uninstall` refuses: destroying would delete the adopted manager. To abandon the
  adoption instead, remove `<env>/install.json` and `<env>/manager.tfstate` from the install's bucket; the source state
  is untouched, and the bootstrap can stay or be removed by hand.

### Permissions to install

The install's profile needs administrator rights in the manager's account: the checks simulate the actions of the
bootstrap (the bucket, the role, the project, the log group, and the rule that starts the install build on a dashboard request with its own role) and `iam:PassRole`, and the install is refused when one is
denied. The build role the install creates gets the `AdministratorAccess` managed policy in that account only, since the
manager module creates IAM roles, Lambda functions, a user pool, a distribution and tables; it is trusted by CodeBuild
for the install's project only. `--build-role` gives your own role instead, and `dormeto aws uninstall` removes the one the
install created.

That role keeps `AdministratorAccess` until `dormeto aws uninstall` (it serves the upgrades). Anyone allowed
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

From CLI 0.4.0 and manager 0.15.0, `dormeto` calls the manager's API, with the AWS profile that `dormeto aws install` uses,
on its IAM-authorized `/iam/*` routes (see [Permission](#permission-to-call-the-manager)); nothing is installed. From
CLI 0.6.0, every operation of the manager is a command, a verb per action of the dashboard: this section has the
routers and the workloads, then come [Discovery and exclusions](#discovery-and-exclusions), [Operations](#operations),
[Users and groups](#users-and-groups), [Audit, savings and alerts](#audit-savings-and-alerts),
[The manager](#the-manager) and [Raw calls](#raw-calls). What they all share comes first: where the manager is, how
targets, confirmations, operations and output work, the exit codes and the permission.

```sh
dormeto aws whoami
dormeto aws routers list --env dev --outdated
dormeto aws workloads list --tag team=payments --state ASLEEP
dormeto aws workloads wake prod-eu/w-1 --wait
dormeto aws routers upgrade --all --dry-run
dormeto aws routers upgrade --all --yes --json > upgrade.json   # in CI
```

### Where the manager is

Every manager command takes `--profile` (the AWS profile; the SDK's default), `--region` (the manager's region; the
profile's by default), `--env` (the manager's environment, `prod` by default) and `--api-url`. The API is found by its
name, `dmt-<env>-manager-api`, in the manager's region; `--api-url` gives its URL and skips the lookup. When no API has
that name, the message says which environment and region were searched. `--api-url` is the API's execute-api endpoint
(the module's `api_endpoint` output, `https://<api id>.execute-api.<region>.amazonaws.com`), not the dashboard's URL.

```sh
dormeto aws whoami [--json]
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
- **Regions.** On the manager's commands, `--region` is the manager's region, where its API is. A router's region is
  `--router-region` (`routers list`, `upgrade`, `install`), a candidate's `--candidate-region` (`discovery candidates`).
  On a router's commands, `dormeto gcp routers` and `dormeto gcp workloads`, `--region` is the router's, a Google Cloud
  region such as `europe-west1`.
- **Clouds.** The manager's commands serve AWS and Google Cloud. `routers list`, `workloads list`, `discovery
  candidates` and `discovery access` take `--cloud aws|gcp` for the rows of one cloud (another value exits 2 before any
  call), the counts staying the manager's, of both clouds (`workloads list --json`'s `byState`, `discovery access`'s
  counts), and every command that takes `--account` takes `--project` too, the same flag under the name of a Google
  Cloud project (`routers install`, which installs in an AWS account only, takes `--account` alone). A Google Cloud
  router is named `dmtr-<project>-<region>`, a Google Cloud workload `<router>/<workload id>` as an AWS one, and its
  backends are `instance`, `asg`, `k8s` and `cloudsql-instance` in the text and in `--json` (the router's own values,
  `vm`, `mig`, `k8s` and `cloudsql`, stay on its wire).
- **Confirmation.** On a terminal, a write on several workloads or routers is confirmed, and these always are, even for
  one target: `routers uninstall`, `workloads offboard`, `workloads exclude`, `exclusions add`, `users disable`, `users
  role`, `groups delete`, `discovery config set` and `manager update`. The question names the targets and what happens;
  a workload write lists its targets first (`Offboard 1 workload?`). Without a terminal, `--yes` is required (exit 2). A
  declined confirmation sends nothing and exits 0. Every writing command of both subtrees takes `--yes`, a no-op where
  nothing is asked, and `--dry-run`, which asks nothing, says each request it would send, one per target (`dry run:
  <target>: would <method> <path>, nothing sent`, its body on the next line), reads what a request needs (a workload's
  tags for `workloads tags`, its backend for `workloads exclude`), sends nothing and exits 0. Four commands have no
  `--dry-run`: `aws install`, `aws uninstall` and their `apply`, whose plan, the step before `apply`, needs the
  bootstrap (a bucket, a role, a CodeBuild project) created first: the plan is the look before acting.
- **Operations.** Onboarding, offboarding, the install, upgrade and uninstall of routers, exclusions and their lifting,
  and the manager's update are operations of the manager. The CLI starts them and follows them, reading each every 5
  seconds: `started <type> <id> for <target>`, each step on stderr as it changes (`  … install router`,
  `  ✓ verify router (12s)`), then `✓ <target>: <type> succeeded in 3m12s` or
  `✗ <target>: <type> failed at <step>: <detail> (operation <id>)`. A start the manager refuses is printed with its
  message; the others go on, and the exit is 1. An operation that another one holds is refused with that one's id:
  `(operation <id>: dormeto aws operations watch <id>)`.
- **Following.** `--no-wait` prints `follow it: dormeto aws operations watch <id>` instead of following. `--timeout` (60m
  by default) and Ctrl-C stop following: the operations go on in the manager, and the CLI prints
  `<target>: still running in the manager: dormeto aws operations watch <id>` for each one (exit 1; 130 on Ctrl-C). A wake,
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
  progress, questions and a dry run's requests then go to stderr, so that stdout always parses:
  - a list prints the API's object, its rows under the API's key after the filters, every page in it and no page's
    cursor (`{users: […]}`, `{operations: […]}`, `{candidates: […]}`);
  - a command that starts operations prints them as an array when it takes several targets (`routers uninstall`,
    `workloads offboard` and `exclude`, `exclusions lift`), whatever their number, `[]` when none started, and as an
    object when it takes one (`routers install`, `workloads onboard`, `exclusions add`), `{}` when it did not start;
  - the other writes on several targets print the manager's answers by target (`<router>/<workload>`, or the router),
    `{}` when none succeeded;
  - a dry run prints `{"dryRun": true, "targets": […]}`, on both subtrees, each target as the command addresses it
    (`<router>/<workload>`, a router, an account, `organization` for the organization's own settings), and a declined
    confirmation `{"confirmed": false, "targets": […]}` (exit 0); `routers upgrade` and `manager update` print their own
    result instead. A dry run that has a context adds its own key beside them: `candidate` on `aws workloads onboard`
    (the candidate's row as discovery listed it), `eligibility` on `gcp workloads onboard` (the router's dry-run
    answer), `plan` on the commands whose dry run is a plan (`gcp routers install` and `uninstall`, `gcp connect` and
    `disconnect`);
  - `aws install check --json` and `aws install status --json` print their object once they have read what it holds
    (`check`: `{env, account, region, ok, checks}`), and nothing on stdout when they fail early (no install found, an
    AWS error): the words are on stderr, the exit 1.

### Exit codes

- 0: success (and a declined confirmation, which sends nothing).
- 1: AWS or the manager refused, an operation or an upgrade failed or did not start, a workload failed to wake, an
  operation or a wait was not followed to its end (`--timeout`), a `--tag` selection was incomplete, or `dormeto aws api`
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
| `types list`, `show`, `set`, `delete` | `GET /settings/types`, `PUT /settings/types` |
| `routers description` | `PUT /routers/{router}/description`, `DELETE` on the same path |
| `workloads description` | `PUT /workloads/{router}/{id}/description`, `DELETE` on the same path |
| `workloads waiting-page set`, `clear` | `PUT /workloads/{router}/{id}/waiting-page`, `DELETE` on the same path (**these give custom HTML on the application's address, see Waiting page**) |
| `routers waiting-page set`, `clear` | `PUT /routers/{router}/waiting-page`, `DELETE` on the same path (**gives custom HTML on the application's address too**) |
| `settings waiting-page set`, `clear` | `PUT /settings/waiting-page`, `DELETE` on the same path (**gives custom HTML on the application's address too**) |
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
schedules 0.31.0, hibernation settings 0.32.0 (and manager 0.19.0), schedule recurrences and joined windows 0.36.0. The manager refuses to demote or disable its last
enabled administrator from manager 0.18.0 (409, "the last administrator: give the admin role to another user first");
an older manager does not, and the CLI does not check it itself, so there `users role` and `users disable` can leave no
person able to administer the manager.

### routers list

```sh
dormeto aws routers list [--account <id>]... [--router-region <region>]... [--name <glob>] [--tag k=v]... [--outdated] [--json]
```

Lists each router's name, cloud (`aws` or `gcp`), account or project, region, source (`manager`, `terraform` or `cli`),
version (`0.29.0 → 0.30.0` when below the version available, `upgrading` while an upgrade runs), health and workloads
(the total and the count per state), then a last line with the version available and how many routers are outdated. A
router that does not answer is listed, its error on stderr. The manager serves both clouds: a Google Cloud router is
`dmtr-<project>-<region>`, its account column holds the project and its source is `cli` (a Google Cloud router's
installs, upgrades and uninstalls stay with `dormeto gcp routers install` and `uninstall`). `workloads list` and `show`,
`routers show` and `discovery access` print the same `CLOUD` column (`show` prints a `cloud` line, and `project` for a
Google Cloud row's account), and every `--json` carries the manager's `cloud` field.

- `--outdated`: only the routers below the version available.
- `--account <id>`: only the routers of these AWS accounts or Google Cloud projects.
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
dormeto aws routers upgrade [<router>...] [filters] [--all] [--concurrency <n>] [--timeout <d>] [--dry-run] [--yes] [--json]
```

```sh
dormeto aws routers upgrade --tag team=payments
dormeto aws routers upgrade --all --dry-run
```

Upgrades the routers named, narrowed by the filters, to the version available. Without a name or a filter, `--all` is
required (exit 2); `--all` takes neither names nor filters. An unknown name exits 2.

It prints the plan: each router to upgrade, from → to, then the routers skipped and why. A router is skipped when it is:

- written in Terraform (upgraded with its Terraform);
- up to date;
- newer than the version available;
- of unknown version (its error is given);
- busy with an operation (the operation's type and id are given);
- a Google Cloud router below the version available, which the line says to upgrade with `dormeto gcp routers install
  --project <p> --region <r> --version <available>`, as the manager and its dashboard say it.

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
dormeto aws routers show <router> [--json]
```

Shows one router as it tells its details: its version and build, its health and problems, the idle check (its schedule
and last run), the Lambda and queue figures, the workloads by state, the pauses, the EKS clusters (each says whether the
router reaches it directly or through its proxy function in the cluster's VPC, with the proxy's name and version; router
0.33.0 or later, and no version when the proxy did not answer the check's ping) and the IP lists, then each
configuration value with where it is set (`terraform`: the router's module; `manager`). Times are in the local
time zone. `--json` prints the manager's answer. A Google Cloud router (`cloud gcp`, `project <id>`) has no Lambda, queue,
concurrency or wake statistics metrics row, its configuration is set by `cli`, and its `hibernate` and `join windows` rows
inherit the organization level the manager pushes to it (`manager: off, read 2m ago`), the built-in value until one is
pushed.

### routers install and uninstall

```sh
dormeto aws routers install --account <12 digits> --router-region <region> [--no-wait] [--timeout <d>] [--json]
dormeto aws routers uninstall <router>... [--dry-run] [--yes] [--no-wait] [--timeout <d>] [--json]
```

```sh
dormeto aws routers install --account 111122223333 --router-region eu-west-3
dormeto aws routers uninstall staging-eu --yes --no-wait
```

- `install` starts the manager's install of a router in the account and region (an `install-router` operation) and
  follows it. The account needs the command role the manager assumes: without it, the manager refuses (`no_access`) and
  the CLI prints why.
- `install` of a Google Cloud project (`--account dormeto-dev`) is refused before any call, exit 2, with the command
  `dormeto gcp routers install --project dormeto-dev --region <region>`.
- `uninstall` skips each Google Cloud router with one line on stderr naming `dormeto gcp routers uninstall --project <p>
  --region <r>`, and goes on with the others. It starts an `uninstall-router` operation for each other router, 3 at once
  at most, and follows them. It is always confirmed on a terminal. The operation's "check empty" step refuses a router
  that still has workloads: offboard them first.
- `--json`: the operations, as the manager last said them: `install`'s as an object (`{}` when it did not start),
  `uninstall`'s as an array (`[]` when none started).

### routers pause and resume

```sh
dormeto aws routers pause <router>... --reason <text> [--for <duration> | --until <time>] [--keep-asleep] [--dry-run] [--yes] [--json]
dormeto aws routers resume <router>... [--dry-run] [--yes] [--json]
```

```sh
dormeto aws routers pause prod-eu --reason "database migration" --for 3h
dormeto aws routers resume prod-eu
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
dormeto aws workloads list [--state <state>]... [--router <name>] [--tag k=v]... [--search <text>] [--json]
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
dormeto aws workloads show        <router>/<workload> [--json]
dormeto aws workloads history     <router>/<workload> [--days 1..31] [--json]
dormeto aws workloads idle-window <router>/<workload> [--json]
```

- `show`: one workload as the manager's workload view shows it: its state and since when, name, URL, account and region,
  router version and tags, its settings, its pauses (who, why, until when), its schedule with the next events (in the
  schedule's time zone), the share of the last 7 days in each state, its idle hours, and what its naps saved, with the
  energy not used. A Google Cloud workload's savings are priced at Google's list prices when the manager has them.
- `history`: from the manager's store, never the router: the time in each state over `--days` (1 to 31, 7 by default),
  the wakes (how many, the median and the longest, the failed ones) and the savings estimate, then the latest 50
  changes, newest first, with what started each and how long a wake took. `--json` has every change.
- `idle-window`: the manager's advice on the idle window: the last 14 days of requests replayed against windows from 5
  to 120 minutes, each with its savings a month (hours asleep a day when nothing is priced), its wakes and wait a day,
  the current and the recommended window marked. It serves both clouds: for a Google Cloud workload the savings are
  priced at Google's list prices when the manager has them (less the discounts; else hours asleep) and the line
  `Google's request counts are estimates.` is printed (`estimated` in `--json`, `false` on AWS's advice). It names
  `dormeto aws workloads settings <router>/<workload> --idle-minutes <n>` to change the window, and for an AWS workload
  `idle_minutes` in its Terraform module; its notes name the workload's cloud's resources (the waker's target group or
  backend service), the cloud read from the router's row in `routers list` (else from `estimated`). The manager
  computes it once a day: the first read may take a while.

### Waking, napping, pausing, renaming and tagging workloads

```sh
dormeto aws workloads wake   <target>... | --tag k=v... [--wait] [--timeout <d>]
dormeto aws workloads nap    <target>... | --tag k=v...
dormeto aws workloads pause  <target>... | --tag k=v... --reason <text> [--for <duration> | --until <time>] [--keep-asleep]
dormeto aws workloads rename <router>/<workload> <name>
dormeto aws workloads tags   <target>... | --tag k=v... [--set k=v]... [--unset k]... [--clear]
```

```sh
dormeto aws workloads wake prod-eu/w-1 --wait
dormeto aws workloads nap --tag env=dev --yes
dormeto aws workloads pause --tag team=payments --reason "load test" --until 18:00 --yes
dormeto aws workloads rename prod-eu/w-1 "checkout api"
dormeto aws workloads tags prod-eu/w-1 --set team=payments --unset owner
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
  needed without one; `--dry-run` says each request and sends nothing. The other workloads go on when one is refused,
  and the exit is 1. `--json` prints the manager's answers of those that succeeded, by `<router>/<workload>` (`{}`
  when none did); the lines and failures go to stderr. For a workload `tags` leaves unchanged, the answer is its tags
  as they are, in the manager's shape: `{workloadId, tags}`.

### Keeping workloads awake and scheduling them

From CLI 0.5.0, `workloads` also keeps workloads awake for a while and sets their weekly schedules (awake and nap
windows, a maximum nap that wakes for maintenance). Targets are `<router>/<workload id>` arguments, or
`--tag key=value`, as for the other workload writes: more than one target is confirmed on a terminal, `--yes` skips it
and is needed without one, `--dry-run` says each request and sends nothing, and the other workloads go on when one is
refused (exit 1). From CLI 0.6.0, each of these commands has `--json`, as the other workload writes do.

```sh
dormeto aws workloads keep-awake <target>... | --tag k=v... (--for <duration> | --until <time>) [--reason <text>] [--wait] [--timeout <d>]
dormeto aws workloads resume     <target>... | --tag k=v...
dormeto aws workloads schedule show  <router>/<workload> [--json]
dormeto aws workloads schedule set   <target>... | --tag k=v... --file <path or ->
dormeto aws workloads schedule clear <target>... | --tag k=v...
```

```sh
dormeto aws workloads keep-awake prod-eu/w-1 --for 2h --wait          # in CI: wake, then wait until awake
dormeto aws workloads keep-awake --tag team=payments --until 18:30 --yes
dormeto aws workloads resume prod-eu/w-1                              # at the end of the CI job
dormeto aws workloads schedule set --tag team=payments --file schedule.json --yes
dormeto aws workloads schedule show prod-eu/w-1
dormeto aws workloads schedule show prod-eu/w-1 --json | dormeto aws workloads schedule set prod-eu/w-2 --file -
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
  or later"). Beyond weekly days, see [Schedule recurrences](#schedule-recurrences).
- `schedule show` prints the time zone, the windows (`awake mon–fri 08:30–19:00`, a recurrence in words), the
  exceptions, the maximum nap and the next events in the schedule's time zone, every time labelled with its zone
  (`Mon 26 Oct 08:00 CET`; UTC, with a note, when the zone is unknown to the CLI). From router 0.36.0 it also says how
  windows join (see [Joined windows](#joined-windows)). `--json` prints `{schedule, next, maintenanceDue}`: `schedule`
  is left out for a router before 0.31.0 (the text output then says "the router does not report schedules (upgrade
  it to 0.31.0 or later)") and is `null` when none. `schedule set --file -` takes that shape back (or a bare
  schedule) and sends its `schedule` member (`by` and `at` in it are accepted by the router); a file with no
  schedule (`null`, `{"schedule": null}`, or the output of a router before 0.31.0) is refused with exit 2.
- `schedule clear` removes the schedule.

### Schedule recurrences

From router 0.36.0, a window repeats on more than chosen weekdays, within a range of dates. There is no flag for it: write the
JSON and send it as any schedule, to one workload or by `--tag`. A window without the new fields reads as before.

```sh
dormeto aws workloads schedule set prod-eu/w-1 --file schedule.json
dormeto aws workloads schedule set --tag team=payments --file schedule.json --yes
dormeto aws workloads schedule show prod-eu/w-1 --json | dormeto aws workloads schedule set prod-eu/w-2 --file -
```

```json
{
  "timeZone": "Europe/Paris",
  "windows": [
    { "kind": "awake", "start": "08:00", "end": "18:00", "repeat": { "pattern": "monthly", "day": 15 } },
    { "kind": "nap", "start": "20:00", "end": "07:00", "repeat": { "pattern": "weekly", "interval": 2, "days": ["mon", "thu"] }, "from": "2027-01-04", "until": "2027-06-30", "wake": "header" }
  ]
}
```

Each pattern, as a window of `windows`, and what `schedule show` prints for it (after its kind, with its wake mode after a nap):

| Pattern | Window | `schedule show` |
|---|---|---|
| Do not repeat | `{ "kind": "awake", "start": "08:00", "end": "12:00", "repeat": { "pattern": "daily" }, "from": "2027-01-04", "until": "2027-01-04" }` | `Once on 2027-01-04, 08:00–12:00` |
| Daily | `{ "kind": "awake", "start": "08:00", "end": "12:00", "repeat": { "pattern": "daily", "interval": 3 }, "from": "2027-01-04" }` | `Every 3 days, 08:00–12:00, from 2027-01-04, no end date` |
| Weekly | `{ "kind": "nap", "start": "20:00", "end": "07:00", "repeat": { "pattern": "weekly", "interval": 2, "days": ["mon", "thu"] }, "from": "2027-01-04", "until": "2027-06-30" }` | `Every 2 weeks on Mon, Thu, 20:00–07:00 next day, from 2027-01-04, until 2027-06-30` |
| Monthly, on a day | `{ "kind": "awake", "start": "08:00", "end": "18:00", "repeat": { "pattern": "monthly", "day": 15 } }` | `Monthly on day 15, 08:00–18:00` |
| Monthly, on a weekday | `{ "kind": "awake", "start": "08:00", "end": "18:00", "repeat": { "pattern": "monthly", "interval": 3, "nth": 1, "weekday": "mon" }, "from": "2027-01-01" }` | `Every 3 months on the first Mon, 08:00–18:00, from 2027-01-01, no end date` |
| Yearly | `{ "kind": "nap", "start": "00:00", "end": "00:00", "repeat": { "pattern": "monthly", "interval": 12, "day": 25 }, "from": "2026-12-01" }` | `Yearly on 25 Dec, all day (24 hours), from 2026-12-01, no end date` |
| Cron | `{ "kind": "awake", "repeat": { "pattern": "cron", "cron": "30 8 * * 1-5", "minutes": 630 } }` | `Cron 30 8 * * 1-5, 10 h 30 min` |
| Weekly days, a range | `{ "kind": "awake", "days": ["mon", "fri"], "start": "08:30", "end": "19:00", "until": "2027-06-30" }` | `Weekly on Mon, Fri, 08:30–19:00, until 2027-06-30` |
| Dates of its own without it | `{ "kind": "awake", "days": ["mon"], "start": "08:00", "end": "12:00", "exceptions": ["2027-01-04"] }` | `Weekly on Mon, 08:00–12:00, except 2027-01-04` |

- **`from` and `until`** are local dates `YYYY-MM-DD` in the schedule's zone, both days included, each optional: with no
  `until` there is no end date. No occurrence starts outside them; one started on `until` runs to its end. `until` before
  `from` is refused.
- **A window's `exceptions`**, at most 100 local dates: that window does not start on them, the others do (the schedule's
  `exceptions` skip every window). The dashboard writes them when one occurrence of a series is dragged alone, with a window of
  that one date at the new times. A schedule takes 100 windows (20 before router 0.36.0).
- **`repeat`** replaces `days`: a window has one or the other. `interval` is 1 to 99, 1 by default. Above 1, `from` is
  required: daily counts days from it, weekly counts weeks (Monday to Sunday) from its week, monthly counts months from its
  month.
- **Monthly** takes `day` (1 to 31), or `nth` (1, 2, 3, 4, or -1 for the last) with `weekday`. A `day` past a month's end falls
  on that month's last day. **Yearly** is monthly with `interval` 12: `from` gives its month.
- **Cron** takes `cron` and `minutes`, and no `start`, `end` or `interval`. `minutes` is how long each occurrence lasts in wall
  time, a multiple of 5 from 5 to 10080 (7 days).
- **A cron expression** is crontab's five fields, `minute hour day-of-month month day-of-week`, at most 200 characters. Each
  field is `*`, a number, a range `a-b`, a step `*/s` or `a-b/s`, or a comma list of those. Months also take `JAN` to `DEC`,
  weekdays `SUN` to `SAT`, in any case; weekdays are 0 to 7, 0 and 7 both Sunday. `?` is `*`, as a whole day field. With both
  day fields restricted (anything but `*` or `?`), either one matches. A day field starting with `*`, such as `*/2`, goes
  alone: beside the other day field it is refused, because cron versions read that differently.
- **Every minute must be a multiple of 5**, as schedule times are: `*` is refused in the minute field, `*/5` and `0,30` are
  accepted. An expression that never matches (`0 8 31 2 *`) is refused; `0 8 29 2 *` matches in leap years. `cron(...)`, six
  fields, `L`, `W` and `#` are refused: EventBridge's weekday 1 is Sunday, so its expressions do not read the same here.
- **A start every 5 minutes is not refused**, but each one applies anew at each check: a manager's Nap or Wake then lasts 5
  minutes at most, and an awake one blocks naps.
- **The next events** reach 400 days with a `repeat` or a `from`, so they say their year; a rarer window can have none. Awake
  and nap windows may not overlap in those 400 days: the refusal gives the first date. Exception dates and the maximum nap
  apply as they do to weekly windows.

**It needs router 0.36.0.** The CLI checks none of this: the router does, and its refusal names the field
(`windows[1].repeat.cron: ...`). A router before 0.36.0 refuses the new fields as `unknown field`, and `schedule set` adds that
they need router 0.36.0. `dormeto gcp workloads schedule` takes the same JSON and prints the same words, and its `--file`
is the file `schedule show --json` prints under `schedule`.

### Workload settings and types

```sh
dormeto aws settings set --idle-minutes 30 --wake-mode inherit
dormeto aws routers settings prod-eu --wait-seconds 20 --no-wake-rules-file rules.json
dormeto aws types set api --tag kind=api --description "Public APIs" --wake-mode any
dormeto aws types set databases --idle-minutes 45
dormeto aws workloads settings prod-eu/w-1 --idle-minutes 10 --type inherit
dormeto aws workloads show prod-eu/w-1        # each value says where it comes from
```

From router 0.37.0, a workload's idle window, hold, drain, wake timeout, held requests, retry after a failed wake and
wake mode each come from the first level that sets them: the workload, its type, its router, the organization, then the
built-in value. Its no-wake and wake rules add up across the levels, the organization's first. A type is built in
(`instance`, `asg`, `ecs`, `k8s`, or `databases` for a workload with RDS or Aurora) or one of the organization's, picked
by `--type` on the workload, else by its tags. `inherit` removes a value on every level, the organization's and a type's
too. The
settings that Google Cloud fixes or limits say so in their flags' help, as each level has them: at a router's and a
workload's, which the Google Cloud router checks, it refuses a wake timeout past 1700 and any hold, held requests or
drain (they are 0; `inherit` is accepted); the organization's and the types' take any value, and a Google Cloud workload
takes 0, and at most 1700 for the wake timeout, whatever they set. The CLI refuses a value out of its range before any
call; a rules file is a JSON list, `-` for the standard input. A workload written by Terraform is refused: set it in its
workload module. A router before 0.37.0 answers that it must be upgraded.

### Joined windows

```sh
dormeto aws settings set --join-minutes 30
dormeto aws routers settings prod-eu --join-minutes 0
dormeto aws routers settings prod-eu --join-minutes inherit
dormeto aws workloads schedule set prod-eu/w-1 --file schedule.json   # with "joinMinutes": 0 in the file
```

From router 0.36.0, two occurrences of one kind separated by a short gap count as one, so a workload is not woken or napped
for the gap. This applies to existing schedules too: set 0 to keep a gap. `joinMinutes` is the largest gap that joins, both ends
included: a whole number from 0 to 120, a multiple of 5. 0 never joins. Two kinds never join, nor does a gap where the other kind
starts, or a start an exception date or a range removes.

The most specific level that sets it wins:

- **The workload:** `joinMinutes` at the top of its schedule's JSON, beside `maxNapDays`, in the file of `schedule set`, with
  `--tag` too. There is no flag and no `workloads settings`: leave it out of the file to inherit.
- **The router:** `routers settings --join-minutes <minutes>`, `inherit` to unset it (administrators; routers 0.36.0).
  `routers show` prints a `join windows` line.
- **The organization:** `settings set --join-minutes <minutes>`, `inherit` to unset it (administrators; manager 0.21.0 or later).
  `settings show` prints it. Routers apply it at their next check, within 5 minutes.
- **The default:** 15, also when a level cannot be read. On Google Cloud there is only the workload's, then the default.

`--hibernate` and `--join-minutes` go together or alone on `settings set` and `routers settings`; what is not given stays as it
was, and giving neither is refused (exit 2). A value out of range is refused before any call (exit 2).

`schedule show` and `workloads show` print the value in force and its level: `join windows up to 15 min apart (organization)`,
`windows never join (workload)`. An older router prints nothing new. `schedule show --json` adds `joinMinutes`, `joinFrom`
(`workload`, `router`, `organization` or `default`) and `joinInherited` (`{joinMinutes, joinFrom}`, what removing the workload's
own would give). `schedule set --file -` sends only the `schedule` member of that JSON, so the value in force beside it does
not become the workload's. A router before 0.36.0 refuses `joinMinutes` as unknown, and the CLI adds that it needs router 0.36.0.

### Ended windows

Routers 0.36.0 remove from a schedule a window with no valid occurrence within the last year and none to come (its pattern,
range and dates without a window decide), and the dates without a window as old, the schedule's and each window's; a schedule left with nothing is removed. The organization sets the delay:

```bash
dormeto aws settings set --ended-window-days 90
dormeto aws settings set --ended-window-days 0        # keep them
dormeto aws settings set --ended-window-days default  # a year
```

30 to 3650 days, 0 keeps them; a value out of range is refused before any call (exit 2). It goes with `--hibernate` and
`--join-minutes` or alone (administrators; manager 0.21.0 or later). `settings show` prints an `Ended windows` line. A router
removes nothing while it has never read the manager's settings, never while a window is active, and never over a schedule saved
since its check read it.

### Hibernation

```sh
dormeto aws settings show [--json]
dormeto aws settings set [--hibernate true|false] [--join-minutes <minutes>|inherit] [--json]
dormeto aws routers settings <router>... [--hibernate true|false|inherit] [--join-minutes <minutes>|inherit] [--dry-run] [--yes] [--json]
dormeto aws workloads settings <target>... | --tag k=v... --hibernate true|false|inherit [--dry-run] [--yes] [--json]
```

```sh
dormeto aws settings show
dormeto aws settings set --hibernate false
dormeto aws routers settings prod-eu --hibernate inherit
dormeto aws workloads settings --tag env=dev --hibernate true --yes
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
  (administrators; routers 0.32.0 and later, an older one is refused with the version to upgrade it to). Without a
  setting flag it shows each router's level (`Settings of router <name> (changed by <who> at <time>):`, in the words of
  `dormeto gcp routers settings`; `--json` prints the settings by router), and sends nothing, so `--dry-run` and
  `--yes` do nothing there.
- `workloads settings` sets a workload's own value, `inherit` to follow its router's (operators and administrators;
  routers 0.32.0 and later). A workload written by Terraform is refused (`set hibernate in its workload module`).
- Several routers or workloads are confirmed on a terminal; `--yes` skips it and is needed without one; `--dry-run`
  says each request and sends nothing. A refusal is printed for its target, the others go on (exit 1). `--json` prints
  the manager's answers (by router, or by `<router>/<workload>`).
- `routers show` prints a `hibernate` line in its configuration: its own value, or `inherit (manager: on, read 2m ago)`
  (`; last read failed: <error>` inside the parentheses when the last read failed), or `inherit (built-in default: on)`
  for a router that reads no manager settings. When the router has no value of the manager's yet (not read, or every
  read failed) it is `inherit (manager: unknown, not read yet: naps stop)` (the error in place of `not read yet` when
  there is one): the workloads are stopped, not hibernated, until it reads one.
- `workloads show` prints the value and where it comes from (`on, from the router`) and, when the router lists the
  instances and hibernation is on, `2 of 3 instances were launched with hibernation; i-0abc is stopped instead`. A
  workload with no standalone EC2 instance among its backends prints `does not apply (standalone EC2 instances only)`
  instead. While the router has not read the manager's settings it prints `off for now: the router has not read the
  manager's settings yet (naps stop)`; after 59 days hibernated it adds `the next nap stops the instances (a fresh boot
  after 59 days hibernated)`. Nothing is printed for a router before 0.32.0.

### Descriptions

```sh
dormeto aws workloads description <router>/<workload> [--set <text> | --file <path or -> | --clear] [--json]
dormeto aws routers description <router> [--set <text> | --file <path or -> | --clear] [--json]
```

```sh
dormeto aws workloads description prod-eu/w-1
dormeto aws workloads description prod-eu/w-1 --set "Shop front, owned by payments"
dormeto aws routers description prod-eu --file runbook.md
cat notes.md | dormeto aws routers description prod-eu --file -
dormeto aws workloads description prod-eu/w-1 --clear
```

From CLI 0.7.0 (manager 0.19.0), a workload or a router can carry a short description in Markdown: what it is, who owns
it, where its runbook is. The dashboard draws it; the CLI prints it as written.

- Without a flag, the command prints the text as written (raw Markdown), then `— ann@x.io, 2026-10-19 10:00 CEST`
  (who wrote it and when), or `no description`.
- `--set` gives the text, `--file` reads it from a file (`-` for standard input), `--clear` removes it; give at most one
  (more is a usage error, exit 2). The text is at most 4,000 characters and holds no control character other than line
  feed and tab; an empty `--set` text clears. A `--file` that is empty, not UTF-8 text or over 64 KiB is refused (exit 2:
  use `--clear` to remove), a leading byte order mark is dropped. The manager's refusal is printed (exit 1). Reading
  reads the manager's store (`GET .../description`), so it works when the router cannot be reached; with a manager
  before 0.19.0 it is `needs manager 0.19.0 or later (…)`, not `no description` (exit 1).
- A workload's description is written by operators and administrators, a router's by administrators. `--json` prints
  `{"description": …}`, as the manager answers a read or a write.
- `workloads show` and `routers show` print the description, indented, under their header lines; nothing for none, or
  for a manager before 0.19.0.

### Waiting page

```sh
dormeto aws workloads waiting-page show <router>/<workload> [--json]
dormeto aws workloads waiting-page set <router>/<workload> [--icon auto|none|<file>] [--title <text>] [--description <text>]
dormeto aws workloads waiting-page set <router>/<workload> --html <file or ->
dormeto aws workloads waiting-page clear <router>/<workload>
dormeto aws routers waiting-page show <router> [--json]
dormeto aws routers waiting-page set <router> [--icon auto|none|<file>] [--title <text>] [--description <text>] | --html <file or ->
dormeto aws routers waiting-page clear <router>
dormeto aws settings waiting-page show [--json]
dormeto aws settings waiting-page set [--icon auto|none|<file>] [--title <text>] [--description <text>] | --html <file or ->
dormeto aws settings waiting-page clear
```

```sh
dormeto aws settings waiting-page set --title "Acme portal" --icon ./logo.svg --description "Our test environments sleep at night."
dormeto aws workloads waiting-page set prod-eu/w-1 --title "Shop (staging)"
dormeto aws routers waiting-page set prod-eu --html ./waiting.html
cat waiting.html | dormeto aws workloads waiting-page set prod-eu/w-1 --html -
dormeto aws workloads waiting-page show prod-eu/w-1
dormeto aws workloads waiting-page clear prod-eu/w-1
```

The page a visitor sees while a workload wakes can carry your name, icon and words, or be your own HTML. One document is
kept per level: the workload, its router and the organization (`settings waiting-page`, for every router). The most
specific level with a document decides the mode; a template takes each of its icon, title and description from the most
specific template that has it. Needs routers 0.35.0 or later (an older router answers `upgrade the router to 0.35.0`,
exit 1) and a manager of 0.20.0 or later.

- `set` replaces that level's document. `--title` (at most 80 characters, one line), `--description` (at most 500
  characters) and `--icon` write a template. From router 0.36.0 both texts take Markdown: `**bold**`, `*italic*`, `` `code` ``
  and `[links](https://example.com)`, and in the description also headings, lists, quotes and code blocks, with line
  breaks kept; raw HTML shows as text. In a template, a field left out is inherited. `--icon auto` is the favicon the router reads
  from the application, `none` shows no icon, and a file is a PNG, JPEG, WebP, SVG or ICO of at most 64 KiB, its type
  taken from its bytes (a file named `auto` or `none` is given as `./auto`; `-` is not read for an icon). `--html` writes a custom page instead: a
  file or `-` for standard input, UTF-8, not blank, at most 256 KiB. `--html` with any of the template's flags, or `set`
  with none of them, is a usage error. The icon and the page are checked before any call (exit 2).
- A custom page runs scripts on the application's own address: it can read that site's cookies and call its API as the
  visitor. In the dashboard, setting, replacing or clearing one needs an administrator. **The CLI does not have that
  check: the manager's `/iam/` routes treat any principal of the manager's account that IAM allows to call them as an
  administrator, and IAM cannot tell a template from a custom page. Whoever may `PUT` or `DELETE` a `waiting-page` route
  can therefore set custom HTML that runs on that application's address.** Give the `waiting-page` write routes only to
  those you would trust with the application's users' sessions. Templates need an administrator for a router or the
  organization and an operator for a workload, in the dashboard. A workload written by Terraform is refused (exit 1):
  set `waiting_page` in its workload module. The page's warnings (no `data-dormeto="status"` element, a
  Content-Security-Policy meta tag) are printed after the write, which goes through. A write at the organization also
  prints when routers apply it (within about a minute).
- `show` prints the effective mode, icon, title and description with the level each comes from (at the organization, the
  `auto` icon when it sets none), for a workload the cached
  favicon (its type, size and date, or why there is none), who set the level and when, and the warnings. An icon or a
  page is printed as its type and size, never its content; `--json` prints the manager's whole answer. At the
  organization, `show` says `no waiting page set` apart from `the waiting page is not known: settings.json is unreadable`.
- `clear` removes the level's document, which then inherits.

### Onboarding, offboarding and excluding workloads

```sh
dormeto aws workloads onboard <candidate> [--name <name>] [--idle-minutes <5..1440>] [--wake-mode any|browser]
                          [--wake-timeout-seconds <60..3600>]
                          [--database <id>|cluster:<id>|<Cloud SQL name>]... [--fallback-type <type>]... [--set-tag k=v]...
                          [--router <router>] [--hibernate true|false]
dormeto aws workloads offboard <target>... | --tag k=v... [--concurrency <n>] [--dry-run] [--yes]
dormeto aws workloads exclude  <target>... | --tag k=v... --reason <text> [--for <duration> | --until <time>] [--dry-run] [--yes]
```

All three take `--no-wait`, `--timeout <d>`, `--yes`, `--dry-run` and `--json`: the operations, as the manager last said
them, `onboard`'s as an object, `offboard`'s and `exclude`'s as an array. `onboard --dry-run` says the candidate, its
eligibility as discovery listed it, and the request it would send.

```sh
dormeto aws discovery candidates
dormeto aws workloads onboard shop-staging --idle-minutes 30 --database cluster:shop-aurora --set-tag team=shop
dormeto aws workloads offboard prod-eu/w-1 --yes
dormeto aws workloads exclude prod-eu/w-2 --reason "under migration" --for 14d --yes
```

- `onboard` starts the onboarding of a candidate pending onboarding, given by its id (the target group's ARN) or the
  name of its target group, follows it, then prints the new workload as `onboarded <router>/<workload id>`. An unknown
  or ambiguous name exits 2 (its ARNs listed) and sends nothing. It sends what the dashboard's Onboard dialog sends:
  - `--name`: the workload's display name, up to 64 characters (the target group's name by default, or when empty);
  - `--idle-minutes`: the idle window, 5 to 1440 minutes. Not given, none is sent and the levels decide (the type's, the
    router's, the organization's, then 15 built in), as the dashboard's Inherit;
  - `--wake-mode`: `any` request that passes the filters, or `browser` (only the waiting page's script or the
    `x-dormeto-wake: 1` header wakes it). Not given, none is sent either: the levels decide (`any` built in; behind a
    global load balancer `browser`);
  - `--wake-timeout-seconds`: how long a wake may take, 60 to 3600 (60 to 1700 for a Google Cloud candidate); not
    given, the levels decide;
  - `--database`: a companion RDS instance identifier, or `cluster:<identifier>`, stopped after the application and
    started before it (repeatable);
  - `--fallback-type`: an EC2 instance type, such as `t3a.medium`, tried in order at a wake when AWS has no capacity
    for the instance's own type (at most 5; for the EC2 instances of a workload);
  - `--set-tag key=value`: a Dormeto tag of the new workload (repeatable). It is `--set-tag`, not `--tag`, which selects
    targets everywhere else;
  - `--hibernate true|false`: hibernation of its EC2 instances (see [Hibernation](#hibernation)); without it the
    workload inherits its router's setting, then the manager's. `inherit` is not a value here: leave the flag out.
- **A Google Cloud candidate** is a backend service named in a URL map, given by its id (the backend service's path,
  `projects/<project>/global/backendServices/<name>` or `projects/<project>/regions/<region>/backendServices/<name>`) or
  the name of its backend service, resolved as a target group's name is. It needs manager 0.24.0 and router 0.40.0. Three flags take the cloud's own values, checked
  before anything is sent (exit 2):
  - `--database` is a Cloud SQL instance name such as `orders-db` (sent as `{kind: cloudsql-instance, id}`); `cluster:` is
    AWS's;
  - `--fallback-type` is a Compute Engine machine type such as `e2-medium`, for the VMs of unmanaged instance groups and
    zonal network endpoint groups (at most 5, as on AWS);
  - `--router` names the router to onboard a global candidate on when its project has several (`dmtr-<project>-<region>`);
    a regional one goes on its region's router. It is refused for an AWS candidate.

  The manager decides whether the candidate is eligible, with the router's words, and the preview has the last word, as on
  AWS. `--hibernate` and the other flags are as on AWS. `dormeto gcp workloads onboard` stays the way for what the manager
  does not do on Google Cloud (a GKE workload, `--request-logs`).
- `offboard` starts the offboarding of each workload, at most `--concurrency` at once (1 to 20, 4 by default): the
  workload is woken, its application given back as it was, and it leaves Dormeto. It is always confirmed on a terminal.
  A Google Cloud workload is offboarded the same way, by `<router>/<workload id>`; the manager refuses, before any
  change, a GKE workload and one whose onboarding turned request logs on, with the command that offboards it,
  `dormeto gcp workloads offboard <id> --project <p> --region <r>`.
- `exclude` excludes each workload's target group (a Google Cloud workload's backend service) from discovery, with a reason: the manager pauses its managed
  workloads (an `exclude` operation). `--reason` is 1 to 246 characters; `--for` is up to 366 days, `--until` within
  366 days; without either, the exclusion lasts until `dormeto aws exclusions lift`. It is always confirmed on a terminal.
  For a Google Cloud workload it excludes its backend service, by the path the command builds from the workload's row
  (`projects/<project>/global/backendServices/<name>`, or `regions/<region>` for a regional or internal load balancer).
- A workload another operation holds is refused with that operation's id; the others go on (exit 1).

## Discovery and exclusions

From CLI 0.6.0, `discovery` shows what the manager's discovery found, runs it, dismisses and restores candidates and
replaces its configuration, and `exclusions` lists, adds and lifts the manual exclusions. Targets, confirmations,
operations and output are as in [What every manager command shares](#what-every-manager-command-shares).

```sh
dormeto aws discovery status [--json]
dormeto aws discovery run [--wait] [--timeout <d>] [--json]
dormeto aws discovery candidates [--status <status>] [--account <id> [--candidate-region <region>]] [--limit <n>] [--json]
dormeto aws discovery dismiss <candidate>... [--note <text>] [--json]
dormeto aws discovery restore <candidate>... [--json]
dormeto aws discovery access [--limit <n>] [--json]
dormeto aws discovery config show [--json]
dormeto aws discovery config set --file <path>|- [--yes] [--json]
dormeto aws exclusions list [--json]
dormeto aws exclusions add (--target-group <arn|path> | --load-balancer <arn|path> | --account <id>) --reason <text> [--for <d> | --until <time>]
dormeto aws exclusions lift <id>... [--no-wait] [--timeout <d>] [--json]
```

```sh
dormeto aws discovery run --wait
dormeto aws discovery candidates --account 111122223333 --candidate-region eu-west-1
dormeto aws discovery dismiss legacy-admin --note "decommissioned in December"
dormeto aws discovery config show > discovery.json
dormeto aws discovery config set --file discovery.json
dormeto aws exclusions add --account 444455556666 --reason "production account" --yes
dormeto aws exclusions lift 5f0c2b7e-1d4a-4e8b-9c3f-7a2d6e1b8c40
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
  `managed`, `ineligible`, `dismissed`, `excluded`, `out-of-scope`, `gone`): target group or backend service, `CLOUD`
  (`aws` or `gcp`), `ACCOUNT OR PROJECT`, region (`global` for a global backend service), hosts, idle
  hours and requests over 7 days, the workload or the operation running on it, and why it has that status. Every page
  is read unless `--limit` stops at that many (0, the default, reads all). `--account` keeps one AWS account or Google Cloud project,
  `--candidate-region` one region of it (`europe-west1` or `global` on Google Cloud) (it needs `--account`; `--region` is the manager's). `--json` prints the
  manager's answer, `{candidates}`, every page in it.
- `dismiss` takes candidates out of the pending onboarding, each given by its id (a target group's ARN or a backend
  service's path), sent as it is whatever the candidate's status, or the name of its target group, resolved among the candidates pending onboarding before anything
  is sent: an unknown name, or one that names the target groups of several accounts or regions (their ARNs are listed),
  exits 2 and nothing is dismissed. `--note` says why (cut at 256 characters).
- `restore` brings dismissed candidates back, each id or name resolved among the dismissed ones in the same way.
- `dismiss` and `restore` print, with `--json`, the candidates as the manager answers them.

### discovery access

`access` shows the organization access of the last run: how many accounts are ready, the delegation policy, the event
forwarders, and a table of the accounts that are not ready with what is missing (the `DISCOVERY` and `COMMAND` columns:
the discovery or the command role of an AWS account, the access of the manager's Google Cloud account to a project). With
both clouds listed, the counts say `without the discovery role or account` and `without the command role or account`.
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
  application load balancer (`--load-balancer <arn>`) or a whole account (`--account <12 digits>`); on Google Cloud, a
  backend service (`--target-group <path>`, `projects/<project>/global|regions/<region>/backendServices/<name>`), a URL
  map (`--load-balancer <path>`, `…/urlMaps/<name>`) or a project (`--account <project ID>`). Each flag takes either
  cloud's value, and anything else exits 2 before any call. The manager pauses its managed workloads (an `exclude`
  operation, followed), keeping its pause apart from an operator's on both clouds: lifting the exclusion leaves the
  operator's pause. `--reason` is 1 to 246 characters; `--for` is up to 366
  days, `--until` within 366 days; without either, the exclusion lasts until it is lifted. It is always confirmed on a
  terminal; `--dry-run` says the request and sends nothing. It takes `--no-wait`, `--timeout`, `--yes` and `--json` (its
  operation, an object).
- `exclusions lift <id>...` lifts each exclusion (its id is in `exclusions list`) and follows the operations: the
  workloads it paused are resumed, then the exclusion is deleted. One that stays listed (a workload could not be
  resumed) is lifted again by running the command again. An exclusion another operation holds is refused with that
  operation's id, the others go on (exit 1). `--json` prints the operations, an array.

## Operations

From CLI 0.6.0, `operations` lists the manager's operations, shows one and follows one, started from the CLI or the
dashboard.

```sh
dormeto aws operations list [--running] [--type <type>]... [--limit <n>] [--json]
dormeto aws operations show  <id> [--json]
dormeto aws operations watch <id> [--timeout <d>] [--json]
```

```sh
dormeto aws operations list --running
dormeto aws operations list --type install-router,uninstall-router --limit 0
dormeto aws operations watch 5f0c2b7e-1d4a-4e8b-9c3f-7a2d6e1b8c40
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
dormeto aws users list [--json]
dormeto aws users invite <email> --role <member|viewer|operator|admin> [--group <name>]... [--json]
dormeto aws users role <email> <member|viewer|operator|admin> [--dry-run] [--yes] [--json]
dormeto aws users groups <email> [<name>...] [--json]
dormeto aws users disable <email> [--dry-run] [--yes] [--json]
dormeto aws users enable <email> [--json]
dormeto aws users resend <email> [--json]
dormeto aws users reset-mfa <email> [--json]
dormeto aws groups list [--json]
dormeto aws groups show <name> [--json]
dormeto aws groups create <name> [--description <text>] [--grant <tags>:<permissions>]... [--json]
dormeto aws groups update <name> [--description <text>] [--grant <tags>:<permissions>]... [--no-grants] [--json]
dormeto aws groups delete <name> [--dry-run] [--yes] [--json]
```

```sh
dormeto aws groups create payments-dev --description "Payments, dev" --grant team=payments,env=dev:read,wake
dormeto aws users invite ana@example.com --role member --group payments-dev
dormeto aws users role ana@example.com operator
dormeto aws users groups ana@example.com          # no group: clears them
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
dormeto aws overview [--json]
dormeto aws audit [--since <duration|time|date>] [--action <action>]... [--who <text>] [--target <text>] [--limit <n>] [--json]
dormeto aws savings show [--json]
dormeto aws savings discounts --compute <0..90> --databases <0..90> [--json]
dormeto aws alerts list [--json]
dormeto aws ip-lists list [--json]
```

```sh
dormeto aws overview
dormeto aws audit --since 2d --action pause,resume
dormeto aws audit --who ci-deploy --limit 0 --json > audit.json
dormeto aws savings discounts --compute 20 --databases 15
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
- `savings show` shows the estimate of the daily job as the Savings card does, for both clouds: the last 30 days,
  this month, since the first day computed, Dormeto's own cost and the net, the top savers, what is not priced
  (how many workloads; a Google Cloud item without its Google list price counts there, never at $0; `--json` has their
  hours), and
  the discounts for commitments the prices are reduced by.
- `savings discounts` sets those discounts, in whole percent from 0 to 90, off the list prices of compute (EC2,
  Fargate, EKS nodes, Compute Engine VMs; never off Spot) and of databases (RDS, Aurora; Cloud SQL), where Google Cloud's
  committed use and sustained use discounts are entered. Both are required (what is not given would be reset); they
  apply from the next day the job computes.
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
dormeto aws manager status [--json]
dormeto aws manager check (--enable [--alert | --no-alert] | --disable [--no-alert]) [--json]
dormeto aws manager changes [--to <version>] [--json]
dormeto aws manager update [--version <version>] [--dry-run] [--yes] [--no-wait] [--timeout <d>] [--json]
```

```sh
dormeto aws manager status
dormeto aws manager changes
dormeto aws manager update --dry-run
dormeto aws manager update --version 0.18.0
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

From CLI 0.6.0, `dormeto aws api` calls any route of the manager's API, signed with the profile, and prints its answer: the
escape hatch for what a later manager adds before the CLI has a command for it.

```sh
dormeto aws api <method> <path> [--data <json> | --data @<file> | --data -] [-q key=value]...
```

```sh
dormeto aws api GET /workloads -q state=ASLEEP -q limit=10
dormeto aws api GET /routers/prod-eu
dormeto aws api POST /routers/prod-eu/pause --data '{"reason":"maintenance"}'
dormeto aws api PUT /discovery/config --data @discovery.json
```

- The method is `GET`, `POST`, `PUT` or `DELETE`; the path starts with `/` and is the route's under `/iam` (an `/iam`
  prefix is dropped), percent-encoded where a segment needs it. `-q key=value` adds a query parameter (repeatable).
- `--data` is the body, sent as it is: JSON, `@<file>`, or `-` for standard input.
- The answer's body is printed on stdout, indented when it is JSON. A `GET` answered 429 or 5xx is tried up to 3 times
  in all. Any status but 2xx prints `HTTP <status>` on stderr (the body still on stdout) and exits 1.
- `--json` changes nothing: the body is the API's answer. It is accepted as every command accepts it.

## Google Cloud

From CLI 0.8.0, `dormeto gcp` installs and runs a Dormeto router in a Google Cloud project, with the account of your
`gcloud` (its active configuration, or `--gcloud-configuration <name>`); nothing else is needed on your machine. The
router naps the backends of a global, regional or classic external Application Load Balancer, or of an internal one, and a
visitor's first request wakes them: Compute Engine VMs and managed instance groups, VMs in zonal network endpoint groups, GKE pods behind
standalone network endpoint groups, and Cloud SQL instances as their companions. Everything but VMs and groups behind a
global load balancer comes with router 0.37.0 and CLI 0.11.0, released on 2026-11-05, which carry what the measures of
2026-11-04 changed and were accepted the same day ([how far each part has been tried](#how-far-google-cloud-has-been-tried)).
Every command takes `--project` (required) and, but `check`, `connect`, `disconnect` and `routers show`, `--region`
(required: the router is `dmtr-<region>`, and `dmtr-<project>-<region>` in the manager). Exit codes: 0; 1 when a command
lacks a permission, a check fails or Google refuses; 2 for a bad flag or a missing answer (an `--yes` without a
terminal); 128 and the signal's number when stopped.

The tree is `gcp check`, `gcp routers install|uninstall|show|settings|pause|resume|waiting-page`, `gcp workloads …`,
`gcp connect` and `gcp disconnect`; the manager's own commands (`routers list`, `workloads list`, `savings`, `users`…)
serve both clouds and stay under `dormeto aws`, where `--cloud gcp` and `--project` choose the rows. The router's
commands are `gcp routers install`, `uninstall` and `show`; `gcp install`, `gcp uninstall` and `gcp status`, their names
until CLI 0.11.0, are gone, as are the flags named otherwise on AWS: it is `--database`, `refresh_hours=` in `--ip-list` and
`--wake-timeout-seconds`. Every writing command takes `--yes` (accepted
and ignored where it asks nothing) and `--dry-run` (says each request it would send, and sends nothing), as the
manager's commands do, and prints AWS's lines, `<router>/<workload>: <what happened>`, the router named as the manager
names it. A refused nap, wake, pause, resume or `schedule set|clear` prints the same head, `<router>/<workload>:
<refusal>`, on stderr, exit 1. The router's two names each have their outputs: the manager's, `dmtr-<project>-<region>`,
is in what both subtrees share (the lines `<router>/<workload>: …` and `<router>: …`, the keys of `--json`'s maps, its
`targets` and a workload row's `router`); the project's, `dmtr-<region>` (its Cloud Run services' prefix), is in the
text of `gcp routers install`, `uninstall` and `show`, in `gcp routers show --json`'s `router` (as 0.11.0 printed it),
and where a message names the router to install or upgrade.

```sh
dormeto gcp check --project my-project --region europe-west1
dormeto gcp routers install --project my-project --region europe-west1
dormeto gcp workloads onboard --project my-project --region europe-west1 \
  --url-map shop-map --backend-service shop --idle-minutes 30 --name "The shop" --set-tag team=web
dormeto gcp workloads list --project my-project --region europe-west1
dormeto gcp routers uninstall --project my-project --region europe-west1
```

- **`gcp check`** is read-only: for each command, ✓ when you hold every permission it needs, ✗ with the missing ones
  otherwise, then the project's parent, the APIs already on and the warnings (the Compute Engine default service
  account's Editor grant). It also checks that the project is linked to an open billing account, and exits 1 without one:
  Google refuses to turn APIs on otherwise. `--image <image>` also tests what a router image of your own needs (see
  [the router's image](#the-routers-image)). `--with <features>` also tests the [optional access](#optional-access-cloud-sql-and-gke) of
  those features; with `--region`, those of the install record are tested too. `connect` and `disconnect` are tested for
  what they do in a project; the bindings they change on folders and organizations are tested there by the commands
  themselves. `--json` prints the report.
- **`gcp routers install`** checks your permissions first (a ✗ stops it with nothing changed), prints the plan (the APIs
  to turn on, every IAM binding to add with its role, member, resource and condition, every resource to create), asks
  for confirmation (`--yes` without a terminal, `--dry-run` prints the plan only), applies it, prints the IAM policy
  difference after each step and verifies. It adds, in this order: the APIs; the Firestore database, so that the install
  record is there before anything else is created; the router's [log view](#request-logs); the custom roles
  `dormetoRouter` and `dormetoRouterBackends`; the service account `dmtr-<region>`; the secure tag `dormeto=managed`
  (the router stops only VMs that carry it); the project's bindings, its grants among them; and the Cloud Tasks queue
  `dmtr-<region>`. It records all of it, and the bindings Google adds by itself, in the document `install/record` of the
  database. A run that stops at any step leaves what it created listed in the record. Unless you pass `--access-only`,
  it also installs [the router](#the-routers-image): the Cloud Run services `dmtr-<region>-controller`, with the
  controller's invoker binding (`roles/run.invoker`, to the router's service account only), and `dmtr-<region>-waker`,
  with the waker's invoker check off (its ingress admits only the load balancer), the waker's network endpoint group and
  the check job `dmtr-<region>-check` (every 5 minutes). Run again, it plans only the differences, and shows the
  project's bindings its record does not explain, recording those of Google's service agents. It never changes or
  removes a binding it did not add. `--with` adds the [optional access](#optional-access-cloud-sql-and-gke), `--ip-list`
  the router's [IP lists](#ip-lists-on-google-cloud), and `--custom-pages` turns on [custom waiting
  pages](#the-waiting-page-on-google-cloud). A router installed before the fallback machine types needs a run of `gcp
  routers install` again: it adds `compute.instances.setMachineType` to the role `dormetoRouterBackends`.
- **`gcp routers show`** (read-only, `--json`) shows the router's record and live state: version, services, controller
  health, queue, job, database and grants, and the record's features, IP lists, custom pages switch (`custom pages: on` or
  `off`) and Kubernetes access. Without `--region` it shows every router of the project. Exit 1 when something differs from
  the record, such as `DMT_IP_LISTS` or `DMT_CUSTOM_PAGES` on a service after an interrupted run.
- **`gcp routers uninstall`** removes exactly what install added, in reverse order, from the record. It refuses while
  workloads are onboarded (`--force` offboards each first, tag bindings included, and names each VM it leaves on a
  fallback machine type), keeps the roles and the tag while another router of the project lists them, turns no API off,
  and lists the APIs and Google's bindings as left in place. It removes every feature the record lists (their roles and
  grants; their APIs are left too), and lists the Kubernetes access no offboarding removed as left in its cluster. It
  prints every IAM change and warns of what the project's policy holds at the end beyond its bindings from before the
  install and Google's. `--with <features>` is only validated, and with `--force` it tests those features' offboard
  permissions too: the uninstall always removes every feature the record lists. When an install stopped while a database
  or a tag was still being created, the record holds the creation, and the uninstall waits for it to end before it
  deletes the resource. A tag Google refuses to delete, still bound, is left and the record kept: run the same command
  again to finish. To reinstall in the same region, wait: Google may keep a deleted queue's name blocked for up to 3
  days (a create can appear to succeed in that window, and `gcp routers install` then says so), and a deleted database's
  ID for about 5 minutes.
- **`gcp workloads`** call the router's controller with your identity token, so you need `run.routes.invoke` on it (`gcp
  check` says whether you hold it). `gcp routers install` does not give it to you: it grants the role to the router's
  service account only, so ask for `roles/run.invoker` on the controller if you lack it. If Terraform writes the load
  balancer, the VMs, the databases or the Deployments, it must ignore what the router changes: the fields are on the
  website's [Workloads page](https://dormeto.com/workloads.html#terraform-gcp).

### Connecting the manager

`dormeto gcp connect` lets the Dormeto manager, which runs in AWS, reach Google Cloud. It sets up workload identity
federation from the manager's AWS roles to two service accounts of a host project, and grants them what the manager needs
at the scopes and on the routers. `dormeto gcp disconnect` removes it. The manager itself never changes a Google IAM policy.

```sh
dormeto gcp connect --project my-host --profile ops --env prod
dormeto gcp connect --project my-host --scope folders/123 --scope projects/shop --profile ops --env prod
dormeto gcp disconnect --project my-host --profile ops --env prod
```

- **Where the manager is:** `--profile`, `--env` and `--api-url`, as for the [manager's commands](#where-the-manager-is),
  and `--manager-region` for their `--region`, which every `dormeto gcp` command uses for a Google Cloud region. The CLI
  reads the manager first: its version, which must be 0.24.0 or later (a dev build of 0.24.0 counts; an older one is refused
  with the version found), its AWS account (the caller's: the manager's API admits only callers of its own account) and
  its environment (`GET /iam/update`). These name the manager's roles `dmt-<env>-manager-<x>` that the trust admits, with no
  typing. `--gcloud-configuration` picks gcloud's configuration, as for the other `gcp` commands.
- **What connect sets up**, from the manifest's `manager` section (`router/gcp/access.json`, embedded in the CLI):
  - in the host project, the APIs `iam`, `iamcredentials`, `sts`, `cloudresourcemanager` and `cloudbilling`;
  - the workload identity pool `dmt-<env>` and its provider `aws` for the manager's account, with the mapping
    `google.subject=assertion.arn`, `attribute.aws_role=assertion.arn.extract('assumed-role/{role}/')` and the condition
    `assertion.arn.startsWith('arn:aws:sts::<account>:assumed-role/dmt-<env>-manager-')`;
  - the service accounts `dmt-<env>-discovery` and `dmt-<env>-command`, each granting `roles/iam.workloadIdentityUser`
    to the principal set of the manager roles that act as it: discovery to `dmt-<env>-manager-discovery`, `-api` and
    `-savings`, command to `dmt-<env>-manager-operations`, `-api` and `-fleet`;
  - at each scope, the custom role `dormetoDiscovery` for the discovery account (read only). It is defined in a project
    scope's project, and in the organization of a folder or organization scope, where it also holds
    `resourcemanager.folders.get|list` and `resourcemanager.organizations.get`;
  - for each router at the scopes (a Cloud Run service `dmtr-<region>-controller`), for the command account:
    `roles/run.invoker` on the controller, `roles/resourcemanager.tagUser` on the tag value `<project>/dormeto/managed`,
    and the custom role `dormetoCommand`, defined in the router's project and granted there.
- **`--scope`** is `organizations/<n>`, `folders/<n>` or `projects/<id>`, repeatable; the default is the host project. A
  later run keeps the recorded scopes and adds the ones given: to drop a scope, disconnect, then connect again. A folder's
  organization is found from the ancestry of a project in it. A folder in which you see no project stops the run: give its
  organization as a scope instead.
- **Regions Cloud Run does not list:** Cloud Run answers some regions as `unreachable` (regions the project can't use). Connect
  and disconnect say so once per project (`Cloud Run did not list the services of project <p> in <regions>: a router there, if
  any, isn't seen by this run`, on stderr, and in `--json`'s `warnings`) and go on with the regions it listed. A router the
  record holds in such a region is neither dropped nor granted again: its grants and its entry stay.
- **As `gcp routers install`:** your permissions are tested at the host and at each scope, each for the steps that act
  on it: a folder for its bindings, an organization for its role and bindings, a router project for its grants (a ✗
  stops the run with nothing changed). A router project's tag value is read only then: Google answers one you may not
  read as one that does not exist. Then the plan is printed and confirmed (`--yes` without a terminal, `--dry-run`
  prints it only). It is applied in the order apis, roles, federation, serviceAccounts, impersonation, projectBindings,
  folderBindings, organizationBindings, controller, tag, routersGone, every IAM change printed (Dormeto's and Google's),
  and verified. The connection's record is written in the manager (`PUT /iam/discovery/google`): the host, its number,
  the pool, the provider, the scopes, the two accounts, the routers with their controller's URL and version, the
  bindings Google added, and who connected with which CLI. A first connect writes it before its first step, without its
  routers, so that a run stopped partway leaves what it made for `gcp disconnect`; then whole at the end, once verified.
  A later connect that adds scopes writes them first too, with the routers already recorded, so that what it makes at a
  new scope is disconnect's to remove. A run stopped after its steps says what the record holds then. The manager
  registers the record's routers at once. Run again, connect plans only the differences (a router installed since gets
  its grants), or says `up to date`. A router of the record no longer found is dropped from it. When its project holds
  no router any more at the scopes (the router uninstalled, or the project moved out of a folder scope), the command
  account's grants there are taken back (the routersGone step: on the project, its tag value and the recorded routers'
  controllers), and its role `dormetoCommand` is left, granted to no one, for disconnect to delete (at the scopes). A
  record made for another environment (its pool and accounts not `dmt-<env>`'s) is refused, exit 2. The commands a run
  tells you to run name the manager as you did (`--profile`, `--env`, `--manager-region`, `--api-url`,
  `--gcloud-configuration`). `--json` prints the record on stdout (`{"connection": …}`; a dry run's `{"dryRun": true,
  "targets": [<host project>], "plan": […]}`) and the words on stderr.
- **What Google keeps:** a connect after a disconnect undeletes the pool and its provider (Google keeps them 30 days) and
  the custom roles (7 days), then updates them. A custom role deleted more than 7 days ago can't be undeleted, and Google
  blocks its ID until its permanent deletion ends, at the latest 44 days after the deletion. Connect then stops with
  `Google blocks the ID of role <id> in <where>: …; Google frees it by <date>`, the date counted from the manager's last
  disconnect in its audit. A deleted service account is created again: its name is free at once.
- **`gcp disconnect`** reads the record back, under its names: a record whose pool and accounts are not `dmt-<env>`'s is
  refused (exit 2, the record kept). It tests the host for its bindings and roles too. It removes every binding of the
  two accounts on the recorded routers' controllers and tag values, on the projects (the project scopes, the router
  projects and the host), and on the folders and organizations of the scopes. A project at the scopes, or the host, that
  holds a `dormetoCommand` without a router of the record (a router there once, or a first connect that stopped before
  naming its routers) is a router project too: its command grants go, on its project, its tag value and the controllers
  it holds. Then it deletes the two custom roles where connect defines them (one still granted to another member is
  kept, and said), the two accounts with their impersonation grants, the provider and the pool. It verifies that no
  binding of the accounts is left, then clears the record (`DELETE /iam/discovery/google`), which unregisters the
  manager's Google Cloud routers (`the manager's Google Cloud routers unregistered`). It turns no API off, and lists the
  APIs and the bindings Google added as left in place. A project that has left the connection's scopes is not read:
  after a disconnect it keeps the role `dormetoCommand` a connect left there, granted to no one, which you delete with
  `gcloud iam roles delete dormetoCommand --project <id>`.
- **Exit codes** as `gcp routers install`: 0; 1 when the manager or a check refuses, a permission is missing or Google
  refuses; 2 for a bad flag (`--project` not a project ID, a `--scope` of another form) or `--yes` missing without a
  terminal; 128 and the signal's number when stopped.

### The router's image

`gcp routers install` installs Dormeto's public image by default,
`<europe|us|asia>-docker.pkg.dev/dormeto-releases/router/router:<version>`. The multi-region follows `--region`:
`europe-*` uses `europe`; `us-*`, `northamerica-*` and `southamerica-*` use `us`; every other region uses `asia`. The CLI
reads the image without a token and deploys it by its digest (`<name>@sha256:…`), which the plan shows.

- **The default version** is the router version this CLI's access describes. A first install takes it, and a later run
  upgrades an older router of Dormeto's to it. Unlike `dormeto aws install`, whose default is the latest manager release
  because that release carries its own access, `dormeto gcp routers install` writes the router's IAM from the manifest
  this CLI embeds, so its default is the router version of that manifest.
- **`--version <v>`** installs another version, up to the default. A newer one is refused before any change: `router <v>
  needs a newer CLI: brew upgrade dormeto`.
- **A router of Dormeto's newer than the CLI**, installed by a newer CLI, stops every run before any change, with `router
  <v> is newer than this CLI knows (<default>): brew upgrade dormeto`. Upgrade the CLI first.
- **`--access-only`** installs the access without the router. The record says so (`accessOnly`), and later runs keep it
  until one with `--version` or `--image`. It says `the router isn't installed: dormeto gcp routers install --version
  <default>`.
- **`--image <image>`** installs a router image of your own instead, such as a copy inside a VPC Service Controls
  perimeter. Its tag is its version. Only an image of your own needs Artifact Registry's API in the project and your
  permissions on the image: `gcp check --image <image>` tests them before a first install. An image in another project
  needs Artifact Registry Reader for this project's Cloud Run service agent, which you give yourself. A later run keeps
  your image and says which public version is available: `router <v> is available: dormeto gcp routers install --version
  <v>`.
- `--image`, `--version` and `--access-only` exclude each other.

### Request logs

By default the idle check counts every request the backend service answers, scanners included. With the router's request
logs, it counts only the requests the waker would not ignore: not scanners, not `HEAD` requests, not an IP list's
addresses, not what a no-wake rule of any level matches. A logged request holds its method, path, user agent and address, and its
only headers are `user-agent` and `x-forwarded-for` (the address): a rule's `headers` condition on either can match it, and one on any
other header never does, so the request counts.

- **The logs decide.** On a backend service that logs every request, any entry in the window makes the logs decide
  alone, whatever Monitoring counts. Its `request_count` is sampled and batched, and cannot tell whether the logs are
  complete. The count decides when the logs hold no entry, when a read fails, and past 1,000 entries (or 10 pages).
  An empty log also catches a log excluded upstream, such as a `_Default` sink excluding the load balancers' logs. A log
  lost only in part is not caught.
- **Scanners with a browser's user agent count.** One that sends such an agent to the application's own paths (`/`,
  `/favicon.ico`) looks like a visitor to these filters, and keeps the workload awake. IP lists catch known scanner
  networks.
- **A regional load balancer's logging** begins 2 to 5 minutes after `--request-logs` (measured). A request in those
  minutes is not in the logs. The count decides while the window's logs are empty. Once they hold an entry, the first
  idle window after the onboarding does not see that request.

- **The install** creates the log view `dmtr-<region>-lb` on the project's `_Default` bucket, filtered to the load
  balancers' request logs. It grants the router `roles/logging.viewAccessor` on the condition that the resource is that
  view, never the project's other logs. The plan shows both, and `uninstall` removes the grant, then the view.
- **`workloads onboard --request-logs`** sets the backend service's `logConfig` to every request (enabled, sample rate
  1.0, its other fields kept), as you, before the onboarding. A backend service that already logs every request is left
  as it is. Without the flag, an onboarding of a backend service that does not log every request ends with `! the idle
  check counts every request, scanners included: --request-logs reads the requests instead (Cloud Logging's price past
  its free tier)`.
- **The router's version:** the request logs need router 0.40.0 or later, which the onboarding reads from the controller
  before any change. On an older router, `--request-logs` stops with `--request-logs needs router 0.40.0 or later: dormeto
  gcp routers install --version <default>`, and an onboarding without it says nothing of the request logs.
- **The offboarding** sets the `logConfig` back when it is still what Dormeto set, and says so. If you changed it since,
  it is left, with a notice naming the backend service.
- **`workloads show`** says what the idle check reads: `idle check  request logs` or `idle check  request count`. It also says why the last nap was given up, in the local time, `nap given up  backends-changed, 2026-11-08 04:55 UTC`, until a nap succeeds (`--json` carries it as `napAborted`).
- **The cost** is Cloud Logging's: its ingestion is charged past 50 GiB a month and project, about 1 KB per request.
- **Terraform:** if you write the backend service in Terraform, keep this block in it, so an apply does not turn the logs
  off:

  ```hcl
  log_config {
    enable      = true
    sample_rate = 1.0
  }
  ```
- **The limit:** the view is created in the `global` location. In a project whose `_Default` bucket is elsewhere (an
  organization's default storage location), the install says `! the router's log view isn't created: the project's
  _Default log bucket isn't in global; the idle check counts every request` and goes on without the view and its grant.
  The record says so, `gcp routers show` shows the same line, the uninstall has neither to remove, and a later run tries
  the view again. The request count decides there.

### Optional access: Cloud SQL and GKE

Turning on the Cloud SQL Admin API or the Kubernetes Engine API makes Google add service agents of its own, and a project
that uses neither should not get them. So you ask for each feature by name:

```sh
dormeto gcp routers install --project my-project --region europe-west1 --with cloud-sql,gke
```

- `cloud-sql` turns on `sqladmin.googleapis.com` and adds two custom roles, granted to the router on the project:
  `dormetoRouterDatabasesRead` (`cloudsql.instances.get`, `cloudsql.instances.list`) and `dormetoRouterDatabases`
  (`cloudsql.instances.update`, on the condition that the instance carries the tag `dormeto=managed`). The router holds
  no other database permission and never connects to a database.
- `gke` turns on `container.googleapis.com` and adds `dormetoRouterKubernetes` (`container.clusters.get`,
  `container.clusters.connect`), which grants nothing inside a cluster: each GKE workload gets its own
  [Kubernetes access](#kubernetes-access-of-gke-workloads).
- The install record keeps the features. A later run with more adds them, a run without `--with` keeps the record's, and
  only `uninstall` removes them. A feature joins the record once its API is on. An unknown feature exits 2.
- `workloads onboard --database` needs the feature `cloud-sql` of the router's install, and `--gke-cluster` its feature
  `gke`.

What Google adds, as seen on a real project on 2026-11-04. The CLI prints each binding Google adds as Google's, after the
step it appeared after, and records it; one that appears later, from a resource you create, is shown and recorded at the
next run. `uninstall` leaves them, and the APIs, in place.

| Feature | After | What Google adds |
|---|---|---|
| `gke` | `container.googleapis.com` turned on | `roles/container.serviceAgent` to `service-<project number>@container-engine-robot.iam.gserviceaccount.com` and `roles/file.serviceAgent` to `service-<project number>@cloud-filer.iam.gserviceaccount.com`; six APIs turned on with it, `.googleapis.com` each: `autoscaling`, `containerfilesystem`, `dns`, `file`, `gkebackup`, `networkconnectivity` |
| `gke` | a GKE cluster created (yours: the CLI creates none) | `roles/container.defaultNodeServiceAgent` to `service-<project number>@gcp-sa-gkenode.iam.gserviceaccount.com` |
| `cloud-sql` | `sqladmin.googleapis.com` turned on, a Cloud SQL instance created | no binding and no other API; each instance has a service account of its own, `p<project number>-…@gcp-sa-cloud-sql.iam.gserviceaccount.com`, with no binding on the project |

### IP lists on Google Cloud

```sh
dormeto gcp routers install --project my-project --region europe-west1 \
  --ip-list name=scanners,url=https://lists.example.com/scanners.txt,refresh_hours=12
dormeto gcp routers install --project my-project --region europe-west1 --no-ip-lists
```

With `--ip-list name=<n>,url=<u>[,refresh_hours=<hours>]`, repeatable, the router downloads each list over HTTP(S),
every 24 hours unless `refresh_hours` says otherwise, and a visit from an address on one
never wakes a workload. Only the action `ignore` exists on Google Cloud, and `action=` is refused: a listed address's
`GET /_dormeto/wake` (the waiting page's probe) never wakes, but a listed address's `POST /_dormeto/wake` still wakes,
as an API client's explicit wake. A list stops browsers and scanners on those addresses, not a client that means to
wake.

- A run with `--ip-list` replaces the set, `--no-ip-lists` clears it, and a run with neither keeps it, a later `--image`
  included.
- The lists go in `DMT_IP_LISTS` on both Cloud Run services and in the install record, so either flag needs the router:
  without `--image` the recorded image is reused, and with neither the run is refused before any call (exit 2). A router
  below 0.35.0 does not read the lists, and the run is refused, naming `--image`.
- A name is lowercase letters, digits and `-`, 1 to 40 characters. A URL starts with `http://` or `https://` (`s3://` is
  refused), and one with credentials (`user:password@`) is refused. Its query string is printed in the plan and stored
  in the record and the services' environment: keep tokens out of it. A comma in a URL is written `%2C`.
- The waker runs with 512 MiB of memory when a list is set, else 256 MiB.

### Google Cloud workloads

| Command | What it does |
|---|---|
| `workloads list` | the router's workloads and their states, AWS's table (`ROUTER`, `WORKLOAD`, `NAME`, `CLOUD`, `STATE`, `TAGS`, `CHANGED`) and its count (`2 workloads (1 ASLEEP, 1 AWAKE)`); `--state`, `--search` and `--tag` filter them as AWS's (`--tag` needs router 0.40.0, which answers the tags), `--json` prints AWS's list object `{workloads, total, byState, cursor, errors}` |
| `workloads show <id>` | one workload, as AWS's `workloads show` lays it out: state, name, tags, then Google Cloud's own rows (load balancer, URL map, backend service, waker backend service, hosts, backends: VMs, groups, Cloud SQL instances, pods; the last wake, what the idle check reads, why the last nap was given up, `nap given up  <reason>, <time>`, until a nap succeeds, and a Cloud SQL instance that no longer exists), its settings and its pauses, in the local time |
| `workloads onboard --url-map <m> --backend-service <b>` | check the eligibility, tag the VMs, groups and databases, write a GKE workload's Kubernetes access, onboard the workload awake |
| `workloads offboard <id> [--force]` | the controller wakes it if needed and restores every reference, then the tag bindings and the Kubernetes access are removed; a workload that cannot wake is refused, and `--force` offboards it anyway, leaving its backends down (listed). It is confirmed on a terminal, as AWS's (`Offboard 1 workload?`; without one, `--yes` is needed: exit 2, nothing changed; declined: `not confirmed: nothing sent`, exit 0); a rerun that only finishes the untag and the access cleanup, the controller no longer holding the workload, is confirmed too |
| `workloads nap <id>`, `workloads wake <id> [--wait] [--timeout <d>]` | ask for a nap or a wake now: the controller answers at once and runs it through Cloud Tasks, so the state printed is the current one; `wake --wait` follows it until the workload is awake (`<router>/<id>: awake`; 15 minutes by default, `failed to wake` and `timeout` exit 1), or follow it with `workloads show` (a wake asked during a nap is kept and runs once the nap ends). A nap of a workload asleep already says `not awake (ASLEEP)`, as AWS words its refusal, but exits 0, since the controller does not refuse it (AWS exits 1) |
| `workloads schedule show`, `set` or `clear <id>` | its nap and awake windows ([schedules and pauses](#google-cloud-schedules-and-pauses)) |
| `workloads pause <id> --reason <text> [--for <duration> \| --until <time>]`, `workloads resume <id>` | hold its naps, then let them run again |
| `workloads access show`, `grant` or `revoke` | a GKE workload's [Kubernetes access](#kubernetes-access-of-gke-workloads), alone |
| `workloads settings <id>...`, `routers settings` | the [settings](#google-cloud-settings-and-their-levels) of a workload, and of the router's level |
| `workloads waiting-page show`, `set` or `clear <id>`, `routers waiting-page show`, `set` or `clear` | the [waiting page](#the-waiting-page-on-google-cloud) of a workload and of the router |

The text is AWS's: a pause in the local time (`paused by <you> on <time>: <reason>, until <time>, keeps asleep`), a
workload's and a router's pause line alike (`<target>: paused until resumed` or `until <time>`, `, keeps asleep` with
`--keep-asleep`, `; woken <workloads>`),
the list's table and `show`'s labelled block, in which what Google Cloud does not have is left out. `--json` prints
AWS's shapes, on stdout alone: a write's answer by target, `{"<router>/<workload>": answer}` (`{}` when
it is refused, an onboarding or an offboarding too; the router's writes `{"<router>": answer}`), the list's object, and
a workload as a manager row has it, whatever the router's version: `router`, `account` (the project), `region`, `cloud:
"gcp"`, `workloadId` (`id` before CLI 0.14.0) and `pauses`, each with its `scope` (`paused` before), and its backends as
`instance`, `asg`, `k8s` and `cloudsql-instance`. A router before 0.40.0 is read with its older names and sent the
requests it takes. `onboard` first asks the controller for a dry run: a workload that is not eligible is refused with
the reasons and nothing is changed (`--dry-run` stops there). Then it binds the tag `dormeto=managed` to each VM,
managed group and Cloud SQL instance as you (the router itself holds no permission to tag, or it could tag any VM and
stop it), then onboards. `--id` is the workload's ID (lowercase letters, digits and dashes, at most 40 characters); it
defaults to the backend service's name when that is a valid ID, else it is asked on a terminal and exit 2 without one.
The settings are optional: `--idle-minutes` (5 to 1440) and `--hibernate true|false` (suspend the VMs that can be
suspended instead of stopping them); a value out of its range exits 2 before any call. A binding of the
`dormeto=managed` tag that was on a VM or group before the onboarding is treated as Dormeto's and removed at the
offboarding. The permissions of `onboard` and `offboard` (the tag bindings, and a feature's own) are listed by `gcp
check`.

The onboarding's other flags:

- `--load-balancer global|regional|classic|internal|cross-region-internal`: the load balancer's kind, read from Google by default (the URL map found
  globally or in the router's region, its forwarding rules' scheme telling global from classic). A map of that name in
  both places is refused, asking for the flag, and a regional load balancer must be in the router's region. Regional and
  classic load balancers have no custom error response, so a nap swaps the map's references from the application to the
  waker, and a wake swaps them back once a VM is healthy. The onboarding adds a route `/_dormeto/backend/*` to the
  application's service for good: it keeps that service health checked through a swap, and the application sees that
  path. A wake includes the map's change: allow about 2.5 minutes on regional and 5 on classic (measured: visitors reached
  the application 62 s after a `POST /_dormeto/wake` on regional and 2 min 16 s after one on classic; the router reads the
  workload `AWAKE` 123 to 188 s and 287 s after the wake, its unswap's wait included). Google advises moving a classic load balancer to the global external one. `internal` is an internal Application Load Balancer on a regional map in the router's region, `cross-region-internal` one on a global map ([internal load balancers](#internal-load-balancers)).
- `--wake-timeout-seconds <seconds>`: how long a wake may take, 60 to 1700 (else exit 2 before any call; `gcp workloads settings` and `gcp routers settings` take the same range). The router's default is 600 s for every kind of workload but one with a Cloud SQL companion, which is 1700 s ([the settings'
  levels](#google-cloud-settings-and-their-levels)). Behind a regional, classic or internal load balancer, a value of the swap's wait + 60 s or less (160 s
  regional, 310 s classic, 90 s internal, 210 s cross-region internal) is refused, as a wake would always fail; if you set it, give a regional
  load balancer about 250 s or more, and a classic one about 340 s or more.
- `--wake-mode any|browser`: what wakes the workload, as AWS's; not given, the router's level decides, and behind the global load balancer's policy it is `browser` ([the wake mode](#google-cloud-settings-and-their-levels)). An empty `--wake-mode` is refused (exit 2), as an empty `--load-balancer` or `--gke-cluster` is: a variable with no value would onboard something else.
- `--fallback-type <machine type>`, repeatable, at most 5 (such as `e2-medium`): the machine types a VM of an unmanaged instance group or of a zonal network endpoint group may run on when its own is out of capacity, as AWS's `--fallback-type`. The router checks them against each VM's zone ([capacity and fallback machine types](#capacity-and-fallback-machine-types)). At the offboarding a VM that still runs on a fallback type is named, one line each (`VM web-1 is on e2-medium, its own is e2-small: it keeps e2-medium until you change it by hand`), as soon as the router answers the offboarding, before the tag bindings go (on stderr under `--json`), and `--json` carries the router's `fallbacks`.
- `--request-logs`: turns the backend service's request logs on, for the idle check to read ([request logs](#request-logs)).
  Its warning, the offboarding's set-back and the cost are there.
- `--database <instance>`, repeatable, as on AWS: a Cloud SQL instance of the project that naps with the workload,
  stopped after the application and started before it. The tag is bound to it as you, and unbound at the offboarding.
- `--gke-cluster <location>/<name>`: the workload's pods are in this GKE cluster, behind standalone network endpoint groups
  (the Service's `cloud.google.com/neg` annotation) on a load balancer you own. No tag is bound for pods. The CLI reads the
  Service's owner in the cluster as you, then prints, asks and applies its Kubernetes access before the onboarding; a
  refused onboarding removes what this run added. `--yes` applies it without asking, and is needed without a terminal and
  with `--json`.

`offboard` restores the load balancer, then unbinds the tag from the workload's VMs, groups and databases and removes its
Role and RoleBinding, as you. `--force` passes only a failed wake: a workload still `NAPPING` or `WAKING` is refused as busy,
with or without it, until that ends or the router's check fails it. With `--force`, a tag or an access it cannot remove
does not stop the rest: it names each and exits 1, and running the same command again finishes. What the router could not
give back, `notGivenBack` in its answer, is printed with a `!` and the command exits 1: it is yours to change, as the
workload is offboarded and no rerun reaches it. A GKE workload asleep keeps its owner at 0 replicas, and `notGivenBack`
names a paused `ScaledObject` and the annotation to remove. When the controller does not answer, it names what is left and
removes nothing.

### Google Cloud settings and their levels

```sh
dormeto gcp workloads settings shop --project my-project --region europe-west1 --idle-minutes 10 --wake-mode browser
dormeto gcp workloads settings shop shop-eu --project my-project --region europe-west1 --idle-minutes inherit --type inherit
dormeto gcp routers settings --project my-project --region europe-west1 --idle-minutes 30 --no-wake-rules-file rules.json
dormeto gcp routers settings --project my-project --region europe-west1
dormeto gcp workloads show shop --project my-project --region europe-west1     # each setting says where it comes from
```

A Google Cloud router has AWS's settings, on AWS's names, flags and lines. A workload's idle window, wake timeout,
retry after a failed wake and wake mode each come from the first level that sets them: the workload, its type, its router,
the organization, then the built-in value. `inherit` gives a value back to the levels. The no-wake and wake rules add up
across the levels, the most general first. The organization level and the custom types are the manager's: a manager
connected with [`dormeto gcp connect`](#connecting-the-manager) pushes its settings to the router when they change and at
its polls, and the router reads them as an AWS router reads the manager's. Without a connected manager the organization
level reads as unset, and only the built-in types exist. `routers settings` with no flag shows the router's level. A router without the levels answers 404, which the CLI says as `router <version> doesn't have the settings
levels: dormeto gcp routers install --version <this CLI's>`.

| Setting | Flag | Range | Built-in |
|---|---|---|---|
| idle window | `--idle-minutes` | 5 to 1440 | 15 |
| wake timeout | `--wake-timeout-seconds` | 60 to 1700 | 600 |
| retry after a failed wake | `--failed-retry-seconds` | 10 to 600 | 60 |
| wake mode | `--wake-mode` | `any` or `browser` | `any` |
| hibernation | `--hibernate` | `true` or `false` | off with fallback machine types, else the router's, else on |
| no-wake and wake rules | `--no-wake-rules-file`, `--wake-rules-file` | a JSON list each, at most 20 rules per list and level | none |
| type | `--type` | a name | from the backends |

`--join-minutes` is the router command's alone; `--dry-run` and `--yes` are on both, as on AWS. Under `--json`, a dry run prints
`{"dryRun":true,"targets":[…]}` and a declined question `{"confirmed":false,"targets":[…]}` on stdout, and the words on stderr, as on
AWS. A rules file is a JSON list (`-` for the standard input), which the CLI checks before any call (exit 2). The router checks each
rule, with AWS's words: a workload whose rules it refuses gets nothing written, its problems are printed, the others go on, and the
exit is 1.

- **The types** keep AWS's names. `instance` is a VM of an unmanaged instance group or of a zonal network endpoint group,
  `asg` a managed instance group, `k8s` GKE pods and `databases` a Cloud SQL companion (on AWS: an EC2 instance, an Auto
  Scaling group, Kubernetes on EKS, and RDS or Aurora). A workload takes its type from its primary backends, databases first;
  `--type` names another. The `databases` type's built-in is AWS's 1800 s, brought to Google Cloud's cap of 1700 s and shown `fixed`.
- **What Google Cloud fixes.** Some values can't be chosen here. The router resolves them to the value Google imposes, and
  `show` says so: each setting is printed with the level it comes from, `set on this workload`, `type <name>`, `router`,
  `organization`, `built-in` or `fixed on Google Cloud` (`--json`: `workload`, `type`, `router`, `organization`, `default`
  or `fixed`).

  | Setting | On Google Cloud | Why |
  |---|---|---|
  | `--wait-seconds` | 0 | The waker holds no request: it answers the waiting page at once, and the page waits for the wake. |
  | `--max-holders` | 0 | No request is held. |
  | `--drain-seconds` | 0 | The nap's switch is a URL map change, and the backend service's own connection draining applies. |
  | `--wake-mode` | `browser` behind the global load balancer's policy | The error fetch carries neither the visitor's path nor its method: waking on it would let every scanner wake the workload. |
  | `--wake-timeout-seconds` | at most 1700 | One controller request lasts at most 1800 s. |
  | `--wake-timeout-seconds` | more than the swap's wait + 60 s behind a swap | The wake waits for the map's change to spread: more than 160 s on a regional load balancer, more than 310 s on a classic one, more than 90 s on an internal one, more than 210 s on a cross-region internal one. |

  An explicit workload value outside these bounds is refused; an inherited one is brought into them and shown as `fixed`, so a
  router's value never makes a workload unwakeable. `--wait-seconds`, `--max-holders` and `--drain-seconds` are refused before
  any call (exit 2) with the reason, and `inherit` is accepted. `--wake-mode any` on a workload behind the global load
  balancer is refused by the router: `the global load balancer's waiting page can't see the visitor's request, so only the page's probe,
  an explicit wake or a wake rule wakes it (wake mode browser)`. At the router's level `any` is accepted: it applies to the
  workloads behind a swap.
- **The wake mode, by load balancer.** Behind a swap (a regional, classic or internal load balancer), the wake mode is `any`
  unless you set `browser`: a request the filters let through wakes the workload and is answered with the waiting page at once, as on
  AWS. In `browser`, only the page's probe, an explicit wake or a wake rule wakes it. Behind the global load balancer's policy
  the waker receives only the error fetch, so the wake mode is `browser`, whatever the levels say: the page's probe, an explicit
  `POST /_dormeto/wake`, the wake header `x-dormeto-wake: 1` or a wake rule wakes it, and any other request counts in the stats as
  `deferred:browser mode`, scanners included. The waker wakes on the wake header there as on AWS, and Google's error fetch passes a
  visitor's header of its own (measured on 2026-11-09: a browser's `GET /` with `x-dormeto-wake: 1` woke an asleep workload). A visitor on a `.php` URL or on a path a no-wake rule keeps gets a page whose probe
  never wakes the workload, as on AWS.
- **The rules, as on AWS.** A rule has a name and one or more conditions (`sourceIps`, `userAgentPattern`, `headers`,
  `pathPrefixes`, `pathPattern`), and matches when all of them do. There are at most 20 rules per list and level; the levels add up, the
  most general first, and the first match decides; a level's list replaces that level's list as a whole. The no-wake rules come
  first, then `HEAD`, declared bots and scanner paths, the wake header, the wake rules, the IP lists, the probe and the wake
  mode. What a condition sees depends on the request the waker gets:

  | The request | `sourceIps` | `userAgentPattern`, `headers` | `pathPrefixes`, `pathPattern` |
  |---|---|---|---|
  | a visitor's, behind a swap | the client's address | the request's own | the visitor's path |
  | the page's probe | the client's address | the browser's request | the visitor's path, which the page sends in `x-dormeto-path` |
  | the global load balancer's error fetch | the client's address | the headers the fetch carries, `accept-language`, `cookie`, `referer` and `user-agent` among them | none: a path condition never matches |
  | an explicit `POST /_dormeto/wake` | the client's address | the request's own | `/_dormeto/wake` |

  The error fetch carries the visitor's query string and address, and these headers (measured on 2026-11-08; `referer` in an
  earlier measure): `accept`, `accept-language`, `cookie`, `forwarded`, `host`, `referer`, `traceparent`, `user-agent`, `via`,
  `x-cloud-trace-context`, `x-dormeto-workload`, `x-forwarded-for` and `x-forwarded-proto`. So a `headers` rule on `cookie` or
  `accept-language` can match it. The client's address is read as the IP lists read it ([IP lists](#ip-lists-on-google-cloud)). A forged `x-dormeto-path` bypasses
  nothing a client couldn't bypass by asking for another path. Router-wide rules go at the router's level: there is no Terraform level.
- **`workloads show`** prints the type, each setting with its level, the hibernation, the fallback machine types, and the no-wake
  and wake rules of every level, each with its level. `--json` prints the controller's answer, with `settings`, `settingsFrom`,
  `type`, `noWakeRules` and `wakeRules`.

### The waiting page on Google Cloud

```sh
dormeto gcp routers waiting-page set --project my-project --region europe-west1 --title "Acme portal" --icon ./logo.svg
dormeto gcp workloads waiting-page set shop --project my-project --region europe-west1 --description "Our test environments sleep at night."
dormeto gcp workloads waiting-page show shop --project my-project --region europe-west1
dormeto gcp workloads waiting-page clear shop --project my-project --region europe-west1
```

The page a visitor sees while a workload wakes can carry your name, icon and words, or be your own HTML, with AWS's flags
(`--icon auto|none|<file>`, `--title`, `--description`, `--html <file or ->`, `--json`), checks and limits (an icon of 64 KiB at
most, a title of 80 characters, a description of 500, HTML of 256 KiB; the checks run before any call, exit 2) and lines:
[Waiting page](#waiting-page) has them. One document is kept per level, the workload's and the router's; the organization's is
the manager's page, which a [connected](#connecting-the-manager) manager pushes to the router with its settings. The most specific level with a document decides the mode, and a template takes each field from the most
specific template that has it. The page is in the visitor's language, English or French, and its progress bar is filled from the
median of the workload's last 10 wakes. A router without the page says so, with the version to install.

- **Custom pages are off.** Custom HTML runs on the application's address, and on Google Cloud anyone who can invoke the
  controller sets a page. So a router serves custom pages only when an administrator's install turns them on: `dormeto
  gcp routers install --custom-pages`, which sets `DMT_CUSTOM_PAGES=1` on both Cloud Run services and `customPages` in
  the install record, and a run without the flag keeps the record's. `--custom-pages=false` turns them off. Setting one
  while they are off is refused with the router's words, `custom pages are off on this router: an administrator turns
  them on with dormeto gcp routers install --custom-pages`. A custom page stored while they are off is shown as `off`,
  with the router's warning, and never served: the template levels or the built-in page show instead. It stays stored
  until you `clear` it or the switch comes back, so turning the switch off stops customer HTML at once.
- **The icon `auto`** is the favicon the router reads from the application at a nap, at most once a day, in 3 seconds, while the
  workload is awake. It reads the host of the URL map's first host rule that sends the whole of `/` to the workload's backend
  service; a map with no such host (a wildcard host, or a `/` that goes elsewhere or depends on more than the path) gives no
  icon, and the internal load balancers get none.

### Capacity and fallback machine types

A start or a resume that Compute Engine refuses for lack of capacity (`ZONE_RESOURCE_POOL_EXHAUSTED`, `POOL_CAPACITY_INSUFFICIENT`)
is tried again within the wake: after 30 s, 60 s, then every 120 s until the wake's deadline, for VMs and managed groups.
A quota error is never retried this way.

`--fallback-type <machine type>` at the onboarding gives a VM more to try, as on AWS: each VM starts on its own type, and on a
capacity error the router sets the next type of its own and the list on the stopped VM and starts it again. A suspended VM only
resumes, as Google changes no type while a VM is suspended. The next stop gives each VM its own type back.

- **Only for VMs of unmanaged instance groups and zonal network endpoint groups.** A managed group is refused,
  `fallback machine types are for VMs of unmanaged instance groups: a managed group's machine type is its template's`, and so
  are the pods of a GKE workload. A list turns hibernation off unless the workload says `true`.
- **The onboarding checks each type** against each VM: at most 5, none of them the VM's own, each in the VM's zone, with no GPU
  and no local SSD, and of the VM's series or between first and second generation series (N1, F1, G1, N2, N2D, E2, C2, C2D, M1, M2, T2D): any
  other is refused with Google's rule. The list is set at the onboarding and can't be changed later.
- **The permission.** The router changes a type with `compute.instances.setMachineType`, which `dormeto gcp routers
  install` adds to the role `dormetoRouterBackends`, under its `dormeto=managed` condition: run it again on a router
  installed before. Without it a wake that needs a fallback fails with `the router's role lacks
  compute.instances.setMachineType: run dormeto gcp routers install again`.
- **What you see.** `workloads show` lists the fallback types and the VMs running on one (`running on a fallback type  web-1 e2-medium
  (was e2-small)`); the log says `InstanceTypeFallback`, and `InstanceTypeNotReverted` when a VM could not be given its own type back
  (the next stop tries again). An offboarding names each VM that keeps a fallback type.

### Internal load balancers

`--load-balancer internal` is an internal Application Load Balancer on a regional map in the router's region (forwarding rules
`INTERNAL_MANAGED`); `--load-balancer cross-region-internal` is one on a global map (rules `INTERNAL_MANAGED` too), which any router
can onboard, as it can a global map. The router tells them from Google by the rules' scheme, as it tells the other kinds. Both are
napped by the swap, like a regional one: Google documents no custom error response on them. `INTERNAL_SELF_MANAGED` (Cloud
Service Mesh) is still refused.

- **The idle check** reads `loadbalancing.googleapis.com/https/internal/request_count`, and with `--request-logs` the logs of the
  resource `internal_http_lb_rule`: `gcp routers install` updates a log view filtered before to include it.
- **The limits.** The visitors' addresses are private, so an IP list is for private ranges. A project's regional serverless
  network endpoint groups share 5,000 queries a second (Google's documentation). A cross-region internal load balancer's waker is in one region. The
  swap's wait is 30 s on an internal load balancer and 150 s on a cross-region one (set from the spreads measured on 2026-11-08: ten times the internal kind's worst, 3 s, and one and a quarter times the cross-region kind's worst, 120 s), so a wake timeout of 90 s or
  less, or 210 s or less, is refused. The page has no favicon.

### Google Cloud schedules and pauses

```sh
dormeto gcp workloads schedule show shop --project my-project --region europe-west1
dormeto gcp workloads schedule set shop --project my-project --region europe-west1 --file schedule.json
dormeto gcp workloads schedule clear shop --project my-project --region europe-west1
dormeto gcp workloads pause shop --project my-project --region europe-west1 \
  --reason "load test" --until 2026-12-01T08:00:00+01:00
dormeto gcp workloads resume shop --project my-project --region europe-west1
dormeto gcp routers pause --project my-project --region europe-west1 --reason "release freeze" --for 3h
dormeto gcp routers resume --project my-project --region europe-west1
```

- `routers pause` pauses the whole router, as `dormeto aws routers pause` does: `--reason` (1 to 256 characters),
  `--for` (a duration up to 366 days: `90m`, `2h`, `3d`) or `--until` (an RFC 3339 time, or `HH:MM` today), not both
  (exit 2, before any call), `--keep-asleep`, `--dry-run` (prints the router and the body, sends nothing), `--yes`
  (accepted: one router asks nothing) and `--json` (the controller's answer by router, `{"<router>": …}`, as AWS's).
  Without `--for` or `--until` it lasts until `routers resume`. The workloads asleep are woken and printed unless
  `--keep-asleep`. A workload's own pause stays when the router is resumed. Routers 0.40.0 and later; an older one
  answers `router <version> doesn't have the router's pause: dormeto gcp routers install --version <this CLI's>`.
- `workloads onboard --name <text> --set-tag k=v` set the workload's name for people (up to 64 characters, default its
  ID) and its Dormeto tags, as on AWS: `--set-tag` is repeatable, at most 50, a key of 1 to 128 characters, a value of
  up to 256, letters, digits, spaces and `_ . : / = + - @`, no key starting with `dormeto:`; a bad one is refused (exit
  2) before any call. `workloads show` prints `name` and `tags`. Routers 0.40.0 and later: with either flag the router's
  version is read after the dry run, and an older router, which drops them, is refused before any change with the
  install to run, as `Router <router> is <version>, older than 0.40.0, which reads --name. Install a newer router first:
  dormeto gcp routers install …` (exit 1).
- `schedule set --file` (`-` for the standard input; a `schedule show --json` output is read as well) takes the
  schedule's JSON, the same as on AWS ([Keeping workloads awake and scheduling
  them](#keeping-workloads-awake-and-scheduling-them), and [Schedule recurrences](#schedule-recurrences) from router
  0.36.0): `timeZone`, `windows`, `exceptions` and `maxNapDays`. The controller checks it and a refused one prints what
  is wrong, exit 1. `schedule show` prints AWS's words: the time zone, the windows (a recurrence in words), `except`,
  `maximum nap`, the join and the next events in the schedule's zone; `--json` prints AWS's `{schedule, next,
  joinMinutes, joinFrom, joinInherited}`, the file under `schedule` (with who set it and when, which the text, AWS's,
  does not say), without AWS's `maintenanceDue`, which the router's answer does not have. `schedule set` prints AWS's
  lines, `<target>: schedule set` and the next events; `set --json` and `clear --json` print each answer by target in
  `show`'s shape (`next`). `schedule clear` leaves the workload to its idle window alone.
- During a nap window, the waiting page tells a visitor when the window ends, and the visitor's browser does not wake
  the workload. Google Cloud has no maintenance wake of its own, so `maxNapDays` is only yours.
- The router's check asks at most 10 scheduled naps each run, every 5 minutes, oldest first: 30 workloads in one nap
  window nap within 15 minutes. Wakes are never held back. A scheduled nap's occurrence is spent once its task is
  accepted: a nap that a pause refuses, or whose task is lost, does not come back when the pause ends.
- Ten workloads on one URL map in one nap window were accepted on 2026-11-08, global and regional. The naps of
  one map share its updates, and a router runs at most 20 naps at once. Ten naps on one regional map were all asleep
  within 283 s, none failed. A nap that finds the map busy gives up and tries again at a later check (the router's
  Compute Engine scenario page says why).
- `pause <id> --reason <text>` (required, 1 to 256 characters) stops every nap, by the idle window, the schedule or
  `nap`, until `resume`, `--for` (a duration, as `routers pause`'s) or `--until` (RFC 3339 or `HH:MM` today, at most 366
  days away). A workload asleep is woken. With `--keep-asleep`
  it is not, but a visitor still wakes it: only scheduled and maintenance wakes wait for the pause to end.
- Two windows of one kind with a short gap between them are one, as on AWS: the gap that joins is the schedule's own
  `joinMinutes`, else the router's (`gcp routers settings --join-minutes`), else 15. `schedule show` says which, as in
  `join windows up to 15 min apart (router)`.
- A router below 0.35.0 has no route for these commands, and the CLI says so; `repeat`, `from`, `until` and
  `joinMinutes` need router 0.36.0.

### Kubernetes access of GKE workloads

The router's IAM role grants nothing inside a cluster. For each GKE workload, the CLI writes, as you, a `Role` and a
`RoleBinding` named `dormeto-<workload>` in the workload's namespace, for the router's service account, labelled
`app.kubernetes.io/managed-by: dormeto`: nothing cluster-wide. Their rules are exactly the router's calls:

- `get` on `deployments` and `statefulsets`;
- `list` on `replicasets`, `pods`, `events` and `scaledobjects`;
- `patch` on `deployments/scale`, `statefulsets/scale` and `scaledobjects`.

`onboard --gke-cluster` runs in this order: the controller's dry run, which reads Google only (the load balancer, its
network endpoint groups, the cluster's DNS endpoint); the CLI's reading of the cluster (the Service's selector, then its
owner) and the access, printed, asked and applied; the onboarding, where the controller checks each rule and reads the
owner. Refused there, the CLI removes the access it just added. `offboard` removes it too. The same steps run alone:

```sh
dormeto gcp workloads access show --project my-project --region europe-west1 \
  --gke-cluster europe-west1-b/shop --namespace shop --workload shop
dormeto gcp workloads access grant --project my-project --region europe-west1 \
  --gke-cluster europe-west1-b/shop --namespace shop --workload shop
dormeto gcp workloads access revoke --project my-project --region europe-west1 \
  --gke-cluster europe-west1-b/shop --namespace shop --workload shop
```

- `access show` is read-only: what is in the cluster, and what `grant` would write.
- `access grant` prints the objects, asks (`--yes` without a terminal; on stderr under `--json`), lists them in the
  install record, then writes what differs: run again, it writes nothing. A Role or RoleBinding of that name without the
  label is refused, nothing changed. The binding's other subjects stay; another router's service account (`dmtr-*@…`)
  among them is refused. Kubernetes lets no one grant rights they do not hold: without them, you need
  `container.roles.escalate` (Google lists it as testing in custom roles) and `container.roles.bind`.
- `access revoke` prints what it removes and asks (`--yes` without a terminal; on stderr under `--json`), then removes
  the RoleBinding and the Role that are Dormeto's and takes them out of the record; one of that name without the label
  is left and said. While another subject holds the binding, only the router's subject goes, and both objects stay. It
  works after `dormeto gcp routers uninstall`, which lists what it leaves in a cluster.
- The CLI and the router reach the cluster on its DNS endpoint, which must be open to traffic from outside its network:
  the router calls it from Cloud Run with its own token, with no VPC. A cluster with no DNS endpoint is refused, saying
  what to turn on; a private cluster is reached by its DNS endpoint only.

### Google Cloud limits

- **A pause with `--keep-asleep` does not keep visitors out**: a visitor still wakes it. Only scheduled and maintenance
  wakes wait.
- **An IP list does not stop an explicit wake**: a listed address's `POST /_dormeto/wake` still wakes the workload.
- **Under a swap** (regional, classic and the internal kinds), the waker gets every request while the workload sleeps and
  answers the waiting page with a `503` and `Retry-After`, whatever the method: an API client reads it as asleep, sends
  `POST /_dormeto/wake`, then retries. In the wake mode `any`, the default, a plain request (a `curl`, a webhook) the filters
  let through wakes the workload too, as on AWS, and still gets the page; set `browser` to keep the waker from waking on it.
  A visitor at a nap's start may wait up to twice the swap's wait with the VMs still running, and a nap that dies while
  swapped holds visitors at the waker only for minutes: a nap that stops beating is called stuck by the router's check (22
  minutes at the latest under a swap). Right after a classic
  map's change first routes a path to the waker, Cloud Run answered plain HTTP with a `302` to `https://` for about 40 s
  (measured): an HTTP-only classic front end gets that error for that moment.
- **A zonal network endpoint group's VMs**: a nap acts only on what still is the group. A membership changed, or a group
  gone, aborts every nap until you offboard and onboard the workload again; a wake still starts what was recorded. A
  nap also checks an unmanaged instance group's VMs and the backend service's backends against the onboarding
  (`backends-changed`). A VM
  of a managed group, an empty group and the other types are refused at the onboarding.
- **Two VMs of one name in two zones**, in a backend service's unmanaged groups or zonal network endpoint groups, are
  refused at the onboarding, as the router records a workload's VMs by name: `backend service <service> has two VMs named
  <name> (zones <zone> and <zone>): Dormeto records a workload's VMs by name; rename one`.
- **A database belongs to one router**: two routers on one instance flip it at each check, and offboarding one unbinds
  the tag the other needs.
- **Read replicas keep running**: Google stops only a primary. A primary with read replicas is taken, and the onboarding
  warns that its replicas keep running, and billing, while the workload sleeps; a read replica itself is refused.
- **Cloud SQL is slow to start**: on a `db-f1-micro` instance, a stop took 104 s and a start 562 s, 9.4 minutes (one
  measure each), and a workload's wake through the router, a VM with such a companion, 551 s. A wake starts the database
  first, so a visitor waits for it. A stopped instance still bills its storage
  and IP addresses.
- **The tag on an instance**: with its binding removed, the workload naps but its database keeps running, and billing,
  logged `DatabaseNotFollowed` at each check, until the tag is bound again. A companion that no longer exists aborts every
  nap; a wake starts the application anyway, and `workloads show` says to offboard and onboard the workload again. An
  offboarding of an awake workload whose database is stopped leaves it stopped.
- **A GKE Standard cluster's nodes are paid all the same**: scaling pods saves money only on Autopilot, or with the node
  autoscaler freeing a node. The onboarding warns of a Standard cluster.
- **GKE's own load balancers are refused**: a URL map named `k8s2-`, `k8s-um-`, `gkegw1-` or `gkemcg1-`, or `mci-` and a
  six-character hash, belongs to an Ingress, a Gateway or a Multi Cluster Ingress, whose controller writes it back, and so is a
  map whose description names `k8sResource` or whose proxy or forwarding rule carries `kubernetes.io/ingress-name`, whatever
  its name (read on a Gateway's and an Ingress's load balancers on 2026-11-09). One measured case:
  GKE removed a policy written on a Gateway's map 48 s after its routes changed, without a word. Use a standalone network
  endpoint group on a load balancer you own.
- **KEDA at zero is refused**: an owner whose ScaledObject has `minReplicaCount: 0`, set or left to KEDA's default, is
  refused. Set it to 1 or more.
- **A classic load balancer on the Standard network tier** is found by the onboarding, and Google accepts it for the swap
  (measured on 2026-11-08, at classic's times).
- **Many naps on one regional map**: the naps share the map's updates, and one that finds it busy gives up (`map-busy`)
  and leaves the workload awake. A router has at most 20 naps in flight; the others nap at a later check.
- **Stats** (wakes, ignored requests) are kept 35 days, and an offboarding deletes its workload's.
- **GKE pods**: a workload at 0 replicas cannot be onboarded. A nap needs the owner's pods behind the network endpoint
  group, and may wait a window for a new pod's endpoint; one aborted for an empty group or a foreign endpoint shows only
  in the router's logs. A wake with no recorded replicas, after a scale to 0 by hand, brings 1 replica. An onboarding
  refused because the new Role was not applied yet passes when run again.

### How far Google Cloud has been tried

- *Accepted on a real project* (2026-10-30): `gcp check`, `install`, `uninstall`, `status` and `workloads`, and Compute
  Engine VMs (unmanaged and managed groups, suspended when possible) behind a global external Application Load Balancer.
- *Accepted on a real project on 2026-11-04*, with router 0.37.0 and CLI 0.11.0, both released on 2026-11-05: regional
  and classic load balancers (`--load-balancer`), zonal network endpoint groups, schedules and pauses (`workloads
  schedule`, `pause`, `resume`), IP lists (`--ip-list`), Cloud SQL companions (`--with cloud-sql`, `--cloud-sql`, now `--database`) and
  GKE (`--with gke`, `--gke-cluster`, `workloads access`), installed by `gcp routers install --with cloud-sql,gke` and
  removed by `gcp routers uninstall`. Not run: an operator holding exactly the permissions `gcp check` lists for
  `onboard`, and a Cloud SQL start refused for lack of capacity.
- *Accepted on a real project on 2026-11-08*, with router image 0.38.0-dev.2 and the CLI built from `main`: the swap of
  a Standard-tier classic load balancer. Ten workloads on one URL map, global and regional: ten naps asked at once, all
  asleep, none failed, within 509 s global and 283 s regional. A check racing an offboarding, skipped. A VM added to an
  unmanaged group, its nap given up as `backends-changed`. A request during a nap's wait, the nap given up as `traffic`.
  A lost wake task, sent again by the waker after 102 s. The request logs, which kept a workload awake for a browser and
  napped it for scanners alone. The waker reading the client's address behind global, regional and classic load
  balancers. A nap through a new revision of the router. The uninstall, which left the policy as before. Not run: an
  install interrupted while its database is being created (unit-tested).
- *Accepted in `dormeto-dev` on 2026-11-09 and 2026-11-10, not released*: `gcp connect` and `gcp disconnect`, and the
  manager reaching a router through the connection (its lists, the onboarding of a Google Cloud candidate, the savings
  and the idle window), with dev builds of manager 0.24.0 and router 0.40.0.
- *Accepted in `dormeto-dev` on 2026-11-10, not released*: `gcp routers pause` and `resume`, run against router
  0.40.0-dev.6 (the router's pause and resume, `--json` keyed by the manager's name).

The public router image, the request logs and this section's other changes since CLI 0.11.0 come with router 0.40.0 and
CLI 0.14.0, which are not released yet. Their acceptance passed on 2026-11-08. Dormeto's public router image is the default. To build and push your own for
`--image`, see `scripts/gcp-image.sh` in the router's repository and the website's [Router page](https://dormeto.com/router.html#image). The settings levels, the waiting page, the fallback machine types and the internal load balancers
are newer still, and not released. Measured on Google in `dormeto-dev` on 2026-11-08 and 2026-11-09: what the global load
balancer's error fetch carries, and that the page's language, its policy, a 256 KiB page and a 59 KB icon reach the browser;
a stopped VM's machine type changed within its series and across to N2D, and a start refused for quota leaving the VM stopped;
Google accepting the internal kinds' waker, their swap waits, the client's address read through them, and their logs and
metric; the names and descriptions GKE gives a Gateway's and an Ingress's load balancers. The router's new features were accepted in `dormeto-dev` on 2026-11-08 and 2026-11-09, on dev builds of `main` (router 0.39.0-dev.2, then 0.39.0-dev.3 with the final review's fixes): the settings by level, the wake mode and rules, the waiting page, the fallback's machine type change through
the router's own role, both internal kinds (measured, napped by the idle check, woken by the page's probe) and GKE's refusal.
They are not released. The manager reaches Google Cloud routers once
[connected](#connecting-the-manager): its commands and its dashboard list them, and it pushes the organization level of the
settings and of the waiting page, and its custom types, to them.

## Building from source

Go 1.26 or later; Node.js for the release scripts' tests; GoReleaser 2 for `make snapshot` and a release. `make` lists the targets:

```
make build                                          # bin/dormeto, as a release builds it
make run ARGS="aws estimate --profile <profile> --uptime"
make test                                           # go vet, the Go tests (shared cases, end to end), the scripts' tests
make install                                        # into ~/.local/bin (INSTALL_DIR), then which dormeto runs
make uninstall                                      # removes it
make snapshot                                       # dist/: the release's archives, checksums and cask
make parity                                         # the shared cases exported again from ../manager
make dry-run VERSION=0.1.0                          # a release's checks, nothing published
make release VERSION=0.1.0                          # publishes it (Releasing)
make after-release VERSION=0.1.0                    # the checks after, safe to run again
make brew-linux                                     # the Homebrew cask on Linux, with podman
```

## Releasing

For maintainers. A release, from `main`:

```sh
make dry-run VERSION=0.1.0    # the checks and a snapshot in dist/, nothing published
make release VERSION=0.1.0    # the release commit, the tag, the GitHub release, the Homebrew cask, the next iteration
```

The notes are what `CHANGELOG.md` has under a leading `## Unreleased`. Once the checks pass, the release turns that heading
into `## 0.1.0` and commits and pushes it (`chore(release): cli 0.1.0`), then tags that commit. A section already written
by hand as `## 0.1.0` is released from the commit it is in; notes in both places are refused. After the publication, the
next iteration puts an empty `## Unreleased` back at the top, committed and pushed, unless one is already there. The
version itself comes from the tag, so there is nothing else to bump. With that section empty, a release stops: nothing to
release.

The release pushes `main` itself, once the checks pass: commits not yet on `origin` go with it, and are published in the
release. A working tree that isn't clean (modified or untracked files) stops it, and so does a `main` behind `origin/main`
or diverged from it, with nothing pushed. A dry run pushes nothing and says how many commits it would.

When the manager is released too, the CLI comes after it, since the checks refuse shared cases older than the manager's
latest release, and before the website, so that the website never describes a CLI that isn't out yet
(`tools/release/dormeto-release` in the vision repository drives the three; its `cli` command is this same release).

`make release` is `scripts/release.sh <version>` (for example `0.1.0`, without a `v`), which checks the repository, tags,
publishes the GitHub release and the Homebrew tap with GoReleaser, and checks the result; `make dry-run` is the same with
`--dry-run`, which checks and builds without changing the changelog, publishing, tagging or reading the token. It stops
with what was done and the commands that finish it if something fails after the release commit. The checks are: on
`main`, clean, not behind `origin/main`, no such tag, the notes in [CHANGELOG.md](CHANGELOG.md), `go vet` and `go test`,
the shared cases not older than the manager's latest release, the Keychain item below, `go mod tidy` and
`goreleaser check`. npm is not a channel: 0.1.0's packages (`@dormeto/cli`) are deprecated, and no other version is
published there.

The checks after it, `make after-release VERSION=<version>` (`scripts/after-release.sh`, safe to run again), install the
version through each channel and run it: the release's archives against `checksums.txt`, the Homebrew cask (installed, or upgraded when it is), the
install script and `go install`. The install script is `https://dormeto.com/install.sh`, or, while that does not answer
(the website not deployed yet), the website checkout's `site/install.sh`: `DORMETO_WEBSITE_DIR`, by default
`../website` beside this repository.

`make brew-linux [ARCHS="arm64 amd64"]` (`scripts/test-brew-linux.sh`) is an opt-in check of the Homebrew cask on Linux, with podman: it builds a
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
