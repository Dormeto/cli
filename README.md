<div align="center">

<a href="https://dormeto.com"><img src="https://dormeto.com/img/og.png" alt="Dormeto: your idle web apps nap, the first visitor wakes them" width="760"></a>

# Dormeto CLI

**Estimate what Dormeto would save on your AWS accounts, from your laptop, before anything is installed.**

[Website](https://dormeto.com) · [Get started](https://dormeto.com/get-started.html) · [Product](https://dormeto.com/product.html) · [Security](https://dormeto.com/security.html) · [Manager](https://github.com/Dormeto/manager) · [Router](https://github.com/Dormeto/router)

![License](https://img.shields.io/badge/license-MIT-3546c4)
![Go](https://img.shields.io/badge/Go-single_binary-00ADD8?logo=go&logoColor=white)
![AWS](https://img.shields.io/badge/AWS-read--only-FF9900?logo=amazonwebservices&logoColor=white)

</div>

`dormeto` is the Dormeto command line. Its first command, `dormeto estimate`, reads an AWS account or a whole
organization with read-only access and estimates what Dormeto would save on the web workloads behind your Application
Load Balancers (EC2, Auto Scaling, ECS, EKS, and the RDS databases that nap with them): the workloads that could nap,
how long each would sleep and wake, and the monthly saving net of Dormeto's own cost. It gives a summary in the
terminal and a Markdown and a JSON report. It installs nothing in your accounts and sends nothing anywhere but to AWS.

## Install

Pick one channel. They all install the same binary, `dormeto`, with no runtime to install.

### Homebrew (macOS, Linux)

```sh
brew install dormeto/tap/dormeto
```

### npm

```sh
npx @dormeto/cli estimate --profile <profile> --uptime
npm install -g @dormeto/cli
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

### Release archives

Each release has an archive per system (macOS, Linux and Windows, on Intel/AMD and ARM) and a `checksums.txt` on the
[releases page](https://github.com/Dormeto/cli/releases). Unpack the archive and put `dormeto` in your `PATH`.

An archive downloaded with a browser is blocked by the system the first time:

- macOS: remove the quarantine mark, `xattr -d com.apple.quarantine ./dormeto`.
- Windows: in SmartScreen's warning, choose "More info" and then "Run anyway".

(`curl`, Homebrew, npm and `go install` do not need this.)

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
  [--refresh discovery|metrics] [--window 60] [--max-wakes 3|none]
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
- `--ec2-uptime`: measures when each EC2 instance without an Auto Scaling group ran over the 14 days (its
  `CPUUtilization`, which EC2 publishes every 5 minutes while an instance runs): its actual cost counts the hours it
  ran, its savings only the hours Dormeto would nap it while it ran (an instance stopped at night by a schedule saves
  less). An instance found stopped is estimated from the hours it ran (without the option, it is left out). Instances
  Dormeto already manages and Auto Scaling groups are not measured.
- `--ecs-uptime`: measures when each ECS service had a task over the 14 days (its `CPUUtilization`, which ECS publishes
  every minute while the service has a task): its actual cost and its savings count only those hours, at its desired
  count when discovered (a service scaled to 0 at night saves less). A service found at 0 tasks is estimated as one
  task from the hours it ran (without the option, it is left out). When it had a task is measured, not how many: a
  service scaled from 4 tasks to 1 at night stays priced at its count when discovered. Services Dormeto already manages
  are not measured.
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
- `--window <minutes>`: the idle window the report's figures use (60 by default, 5 to 1440). The report also gives each
  workload's recommended window, the one a customer would set.
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
describes it), requests (each workload's `RequestCount` per minute over 14 days, five metrics per `GetMetricData`
call), and the estimate (Dormeto's replay priced with the product's price book, at `--window` and at each workload's
recommended window, net of Dormeto's cost). In a terminal, each phase shows one progress line; piped or redirected, it
prints a line per account and region.

## Permissions

Read-only: the estimate calls `Describe*`, `List*` and `Get*` operations, plus `sts:AssumeRole` to reach member
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
each account's regions for a day, the requests while their 14 days end on the same UTC day, and the prices. An
interrupted run resumes where it stopped; a cache file that cannot be read is read again, never an error.
`--refresh discovery` reads discovery and the requests again, `--refresh metrics` the requests only. To start over,
delete the directory.

## Privacy

`dormeto` sends nothing but its calls to AWS and to your EKS clusters' APIs, with your credentials, from your machine.
There is no telemetry and no update check. What it reads stays in the cache and the reports you choose to share.

## Releasing

For maintainers. `scripts/release.sh <version>` (for example `0.1.0`, without a `v`) checks the repository, tags,
publishes the GitHub release and the Homebrew tap with GoReleaser, publishes the npm packages and checks the result;
`--dry-run` checks and builds without publishing, tagging or reading a token. The version needs a `## <version>` section
in [CHANGELOG.md](CHANGELOG.md).

The checks after it, `scripts/after-release.sh <version>` (safe to run again), install the version through each channel
and run it: the release's archives against `checksums.txt`, the Homebrew cask (installed, or upgraded when it is),
`npx`, the install script and `go install`. The install script is `https://dormeto.com/install.sh`, or, while that does
not answer (the website not deployed yet), the website checkout's `site/install.sh`: `DORMETO_WEBSITE_DIR`, by default
`../website` beside this repository.

`scripts/test-brew-linux.sh [arm64] [amd64]` is an opt-in check of the Homebrew cask on Linux, with podman: it builds a
snapshot, checks the cask's Linux entries against `checksums.txt`, serves `dist/` locally, and in a `homebrew/brew`
container per architecture (by default the machine's own; Homebrew hangs under amd64 emulation on an ARM Mac) installs
the generated cask from a local tap and runs `dormeto version`. It removes its containers and server on every exit.

It reads two tokens from the macOS Keychain, items named for the account `$USER`: `npm-dormeto` and `github-dormeto`.
They are never written to a repository, to `~/.npmrc` or to a log. Create each, then store it with `security`, which
asks for the value (`-w` last, with no value) so it does not appear in a command line or the shell's history:

- `npm-dormeto`: on npmjs.com, Access Tokens, Generate New Token, Granular Access Token, with Read and write on the
  `@dormeto` scope, "Bypass two-factor authentication" checked, and an expiration of 90 days; renew it every quarter.
- `github-dormeto`: on GitHub, Settings, Developer settings, Fine-grained personal access tokens, with the Dormeto
  organization as resource owner, limited to the `cli` and `homebrew-tap` repositories, and the Contents permission at
  Read and write.

```sh
security add-generic-password -U -a "$USER" -s npm-dormeto -w
security add-generic-password -U -a "$USER" -s github-dormeto -w
```

The release checks both tokens before it tags anything; an expired or revoked one stops it with a message. Those
checks prove only that each token is valid (`npm whoami` answers any valid token, and both repositories are public):
their write grants are proven by the publish itself, and a refusal there stops the release with the state report, what
was done and the commands that finish it.

## License

[MIT](LICENSE)
