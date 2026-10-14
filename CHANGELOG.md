# Changelog

## Unreleased

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
