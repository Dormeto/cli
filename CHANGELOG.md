# Changelog

## Unreleased

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
