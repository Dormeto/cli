# Shared cases

Manager 0.17.0, router 0.31.0, manager commit `40a3d02639eab9a7fe7cef1f36cfb2eaf5160abf`.

Written by the manager's `scripts/parity/export.mjs`: the product's Node functions run over the corpus of
`scripts/parity/cases/`, each case's input and output as JSON. Do not edit these files: export them again in the
manager (`npm ci`, then `node scripts/parity/export.mjs --out ../cli/testdata/parity`) and commit them here.

## Files

- `<topic>.json`: `{"manager", "router", "commit", "cases": [{"name", "input", "output"}]}`, one per topic below
  (`internal/parity` `Load`); `estimate.json` is frozen, see below.
- `versions.json`: `{"manager", "router", "commit"}` (`dormeto version`).
- `cache/`: frozen, no longer exported (manager 0.10.0, commit 580867f; see `estimate` below): the cache directory the
  Node estimate wrote for the first `estimate` case (its `cacheRoot`, same layout:
  `<organization>/regions/<account>.json`, `discovery/<account>-<region>.json`, `requests/…`, `uptime/…`,
  `prices.json`; each file but `prices.json` is `{"writtenAt": <ms>, "value": …}`).

## Conventions

- JSON as Node writes it: dates as `toISOString()` (`2026-10-12T08:00:00.000Z`), fields that are undefined absent (absent and `null`
  differ).
- Every case runs at `2026-10-12T08:00:00.000Z`: `input.now` where the function takes a time.
- Outputs are compared with `internal/parity` `Equal`: numbers to a relative 1e-9, object keys in any order,
  everything else exactly (array order included).
- Where a topic has several functions, `input.function` names the Node function; the Go one is given beside it.

## Recorded answers

`input.answers`: `{"<port>.<method>": [{"input": …, "output": …} or {"input": …, "error": …}]}`, the AWS and
Kubernetes calls the Node code made in the case, each input once, in the order of their first call.

- A call is answered by the first entry whose input equals its own: both as JSON, object keys sorted, compared
  exactly. A call without one fails the case (the Node export fails on it, even where the code would catch it).
- An input is the method's argument as the Node code passes it, fields absent when undefined (`{}` when it passes
  none, or only undefined ones, such as a first page). Shapes beyond one argument:
  - `spot.history`: `{"region", "input"}` (the price book's `history(region, input)`).
  - `kubernetes.of`: `{"cluster"}`, the client of a cluster made (`kubernetesOf(cluster)`; for real: the
    cluster's endpoint and certificate authority from `DescribeCluster`, an EKS token), output `{}` or an error.
  - `kubernetes.get`: `{"cluster", "path"}`, output the object; `kubernetes.list`: `{"cluster", "path"}` and
    `"query"` when the caller passes one (`{"labelSelector": …}`), output the items of every page, as the router's
    client returns them.
  - In the `estimate` topic, every input is `{"account", "region", "input": <the shape above>}`: the account whose
    credentials make the call and the client's region (`us-east-1` for STS, Organizations, the Price List and the
    enabled regions; the region asked for Spot price history, from the profile's account).
- An output is the API's answer as the Node ports return it: the SDK's JSON (PascalCase; ECS and EKS camelCase), dates
  as ISO strings, Price List products as JSON strings.
- An `error` is `{"name", "message"}`, with `"status"` and `"reason"` for a Kubernetes one (`KubernetesError`):
  the call fails with it (Node throws an `Error` with these fields; the Go fake returns an error carrying them, its
  message the `message`).
- Port methods:
  - `elbv2.describeLoadBalancers`, `elbv2.describeListeners`, `elbv2.describeRules`, `elbv2.describeTags`, `elbv2.describeTargetGroups`, `elbv2.describeTargetHealth`
  - `autoscaling.describeAutoScalingGroups`
  - `ec2.describeInstances`, `ec2.describeSecurityGroups`, `ec2.describeRegions`
  - `ecs.listClusters`, `ecs.listServices`, `ecs.describeServices`, `ecs.describeTaskDefinition`
  - `rds.describeDBInstances`, `rds.describeDBClusters`
  - `cloudwatch.getMetricData`
  - `eks.listClusters`, `eks.describeCluster`
  - `kubernetes.of`, `kubernetes.get`, `kubernetes.list`
  - `pricing.getProducts`
  - `spot.history`
  - `sts.getCallerIdentity`, `sts.assumeRole`
  - `organizations.describeOrganization`, `organizations.listRoots`, `organizations.listOrganizationalUnitsForParent`, `organizations.listAccountsForParent`, `organizations.listTagsForResource`

## `replay`

The fleet's idle window replay (`packages/fleet/src/idle.ts`, Go `internal/replay`). `input.function` names
the function:

- `idleWindowAdvice` (Go `replay.Advice`): `input` holds its argument's fields as Node takes them — `requests`
  (`{from, to, minutes: [[minute, count]]}`, minutes from `from`), `current`, `checkMinutes`, `wakeMs` (or null),
  `startAsleep`, `actualWakes`, `actualFrom` (or null), `stopped` (the items, or null), `baseline`
  (`{kind: "always"}` or `{kind: "business-hours", timeZone}`), `wakerTargetGroup`, `running` (periods, or null) —
  with `now` an ISO date (Node takes milliseconds) and `prices` in place of the `hourly` callback: `[{item, usd}]`,
  the hourly price of each item it is asked (`usd` null: no price); an item is found by equal JSON (sorted keys), one
  missing is an error. Output: the `IdleWindowAdvice` (`computedAt` is `now`).
- `replayIdle` (Go `replay.Replay`): `{requests, windows, checkMinutes, wakeSeconds, startAsleep}`. Output: one
  `{minutes, asleep: [{from, to}], wakes, wokeAt}` per window.
- `recommendWindow` (Go `replay.Recommend`): `{rows: [{minutes, wakes}]}`. Output: the window, in minutes.
- `windowsFor` (Go `replay.Windows`): `{current}`. Output: the windows tried.
- `checkMinutesOf` (Go `replay.CheckMinutes`): `{schedule}` (a string or null). Output: minutes.

No recorded answers.

Cases (43):

- replayIdle: one request a day, awake at the start, a wake of 60 s
- replayIdle: a request every 10 minutes for an hour, at 5 and 15 minutes
- replayIdle: a silence just under the window keeps it awake, one of the window wakes it again
- replayIdle: naps only at a check: a window of 7 minutes
- replayIdle: requests during a wake are activity, not wakes
- replayIdle: stays awake a full window after a wake
- replayIdle: asleep from the start without any request
- replayIdle: a request starting at the check that naps comes after the nap
- replayIdle: a window of a day over a request every 6 hours: never asleep
- replayIdle: a request in the last minute wakes it
- replayIdle: minutes out of order or repeated are added up
- replayIdle: every window tried with a current one of 25, checked every hour
- recommendWindow: the shortest window avoiding 90% of the wakes the longest avoids
- recommendWindow: the same wakes for every window: the shortest
- recommendWindow: no wake at all: the shortest
- recommendWindow: wakes rising again for a longer window: the first under the threshold
- recommendWindow: only the nine windows are candidates, not a current 25 or 1,440
- recommendWindow: no row: the shortest of the nine
- windowsFor: a current window of 15
- windowsFor: a current window of 25
- windowsFor: a current window of 1440
- checkMinutesOf: rate(5 minutes)
- checkMinutesOf: rate(1 minute)
- checkMinutesOf: rate(1 hour)
- checkMinutesOf: rate(2 hours)
- checkMinutesOf: cron(0/5 * * * ? *)
- checkMinutesOf: no schedule
- idleWindowAdvice: each window priced, asleep hours, wakes and wait; the knee; the real wakes
- idleWindowAdvice: within the business-hours baseline
- idleWindowAdvice: an item without a price: nothing priced, never a partial figure
- idleWindowAdvice: no nap described yet: nothing priced
- idleWindowAdvice: a backend running 08:00 to 20:00 UTC: saves only while it ran
- idleWindowAdvice: a backend measured running all the time
- idleWindowAdvice: a backend measured never running: nothing saved
- idleWindowAdvice: no wake measured yet: the wait unknown
- idleWindowAdvice: clearly more wakes replayed than real ones: noisy
- idleWindowAdvice: a few more wakes replayed than real ones: not noisy
- idleWindowAdvice: 4 replayed wakes for 2 real ones: not noisy (at least 3 more)
- idleWindowAdvice: compared over the time the history knows: onboarded on day 5
- idleWindowAdvice: a history that knows none of the period: no comparison
- idleWindowAdvice: a current window of a day is shown, never recommended
- idleWindowAdvice: Spot capacity and pods on nodes, in Paris business hours
- idleWindowAdvice: as the estimate calls it: a 60-minute window, no history, a Fargate service on Fargate and Fargate Spot

## `prices`

The product's prices (`packages/fleet/src/prices.ts`, `savings.ts`, `packages/api/src/spot.ts`; Go
`internal/prices`). `input.function` names the function:

- `hourly` (Go `prices.NewBook(pricing, spot, cache, now)` then `(*Book).Hourly`): one price book made at
  `input.now` over the recorded answers and a cache holding `input.cache` (`{key: usd or null}`: prices already
  there, fresh), then asked each of `input.lookups` in order (`{region, item, discounts}`, `discounts` as
  `{compute, databases}` percentages: `hourly(region, item, discounts)`). Output: `{prices, cache}` — the price of
  each lookup (USD an hour, or null), and every price the book put in the cache (`{key: {usd, days}}`, `days` as
  the book passes it: 30 for a list price, 1 for Spot; the cache itself keeps a null a day).
- `baselineHours` (Go `prices.BaselineHours`): `{from, to, baseline}`. Output: hours.
- `savingsOf` (Go `prices.SavingsOf`): `{phases, baseline, prices}`, `prices` in place of the `hourly` callback as
  in `replay` (`[{item, usd}]`). Output: the `Savings` (`usd`, `hours`, `items`, `unpriced`).
- `DORMETO_COST` (Go `prices.DormetoCost`): no input. Output: Dormeto's cost a month by part.

Recorded answers used: `pricing.getProducts`, `spot.history`.

Cases (25):

- hourly: EC2 by type, region and OS, times the count; asked once, then from the book
- hourly: Fargate by vCPU and GB hours, x86 or ARM, times the count
- hourly: RDS by class, engine and deployment, Aurora Serverless v2 by ACU
- hourly: nothing priced: another OS, Oracle, an unknown item, a type the API does not return (cached as such)
- hourly: a throttled API prices nothing for that item, without caching it
- hourly: several products of different prices are no price; of one price, that price
- hourly: Multi-AZ RDS asked by its usage type
- hourly: Spot: the latest price of each zone averaged, times the count; cached a day
- hourly: Spot: Windows asked as Windows; another OS, or no answer, is no Spot price (none cached a day)
- hourly: Spot in a region the account has not enabled: nothing priced, nothing cached
- hourly: Fargate Spot at 30% of the on-demand price
- hourly: discounts: compute off on-demand EC2 and Fargate, databases off RDS and Aurora, none off Spot
- hourly: pods on nodes: their share of the node times its price and the pods; Spot pods at the Spot price
- hourly: pods on nodes no autoscaler removes save nothing; without the node's type or allocatable, no price
- hourly: prices already in the cache, one of them none (null): read, never asked
- baselineHours: always, every hour
- baselineHours: business hours across a weekend in Paris
- baselineHours: business hours after Paris goes back to UTC+1
- baselineHours: business hours the Monday before, at UTC+2
- baselineHours: business hours over 14 days in New York
- baselineHours: an empty period
- savingsOf: each asleep phase's hours by what it stopped, the items summed
- savingsOf: what it could not price, never as nothing saved
- savingsOf: only the baseline's hours
- DORMETO_COST: Dormeto's cost a month

## `scan`

Discovery's scan of a region (`packages/discovery/src/scan.ts`, `kubernetes.ts`, `filters.ts`, `org.ts`;
Go `internal/discover`). `input.function` names the function:

- `scanRegion` (Go `discover.ScanRegion`): `{where: {account, region}, now, ports, reader?}`. `ports` lists the
  ports the scan is given: `elbv2` always, `autoscaling`, `ecs`, `cloudwatch`, `ec2`, `kubernetes` when listed
  (one not listed is absent from the Node `ScanPorts`: the scan reads nothing of it — no Auto Scaling groups, ECS
  services, idle hours, nodes or clusters). `kubernetes` is `eks` and the clusters' API (`kubernetes.of`, `get`,
  `list`); `reader` names who reads the clusters in the reason of one refused (`DmtDiscoveryRole` when absent). Output: `{rules, clusters}` — the `ScannedRule`s in the scan's order, and the
  region's clusters as read (`{name, reason}`, `reason` null for one read); `{error: {name, message}}` when the scan
  fails.
- `clustersOf`: `{reader?}`. Output: `[{name, reason}]`.
- `kubernetesBackend`: `{groups, reader?}`: the clusters read once (`clustersOf`), then each group
  (`{arn, type, vpcId, tags, targets}`) traced in turn. Output: one result per group — the
  `{kind: "k8s", id, kubernetes}` backend, the reason it cannot be napped, or null (not Kubernetes).
- `matches`: `{criterion, account, rule, inventory}`. Output: a boolean.
- `decide`: `{config, account, rule, inventory}`. Output: `{scope, reason}`.
- `scanAccounts`: `{config, inventory}`; `targetPairs`: `{config, inventory, enabledRegions}`. Output: the plans.
- `resourceTagsMatch`: `{wanted, tags}`. Output: a boolean.
- `loadInventory` (`org.ts`, the organization's inventory): `{tags}` and `organizations.*` answers. Output:
  `{accounts, ous}`.

Recorded answers used: `autoscaling.describeAutoScalingGroups`, `cloudwatch.getMetricData`, `ec2.describeInstances`, `ecs.describeServices`, `ecs.listClusters`, `ecs.listServices`, `eks.describeCluster`, `eks.listClusters`, `elbv2.describeListeners`, `elbv2.describeLoadBalancers`, `elbv2.describeRules`, `elbv2.describeTags`, `elbv2.describeTargetGroups`, `elbv2.describeTargetHealth`, `kubernetes.get`, `kubernetes.list`, `kubernetes.of`, `organizations.listAccountsForParent`, `organizations.listOrganizationalUnitsForParent`, `organizations.listRoots`, `organizations.listTagsForResource`.

Cases (93):

- scanRegion: the region of the router discovery cases: one candidate per application target group, one per rule forwarding to none
- scanRegion: the idle hours of the last 7 days, for eligible and managed target groups only
- scanRegion: a managed rule that no longer forwards to its application
- scanRegion: a sleeping managed workload: its application target group, not the waker
- scanRegion: an empty region
- scanRegion: a region with a network load balancer only, and clusters none can read
- scanRegion: load balancers refused: the region fails
- scanRegion: IP targets traced to their Kubernetes owner
- scanRegion: a cluster it cannot read leaves its target groups ineligible, the rest scanned
- scanRegion: a discovery role without EKS rights scans the region as before
- scanRegion: the clusters as read, who reads them named in the reason of one refused
- scanRegion: instance targets that are a cluster's nodes, and a node group's Auto Scaling group, never offered to nap
- scanRegion: an onboarded Ingress workload, awake: managed by its waker target group, the customer rule it shadows with it
- scanRegion: an onboarded Ingress workload, asleep
- scanRegion: a lone Ingress patched: its own rule forwards to its target group and the waker
- scanRegion: a customer rule with other conditions is not the workload's
- clustersOf: the region's clusters, those it cannot reach or read with the reason
- kubernetesBackend: an Ingress in a group: its Deployment, the switch through Dormeto's Ingress, HPA, Argo CD, Karpenter
- kubernetesBackend: a lone Ingress (instance targets): its StatefulSet on Fargate, the switch by patching it, Flux
- kubernetesBackend: KEDA: none in a cluster without its API
- kubernetesBackend: KEDA: unknown (null) when the cluster refuses to show its ScaledObjects
- kubernetesBackend: a target group no Ingress claims: a TargetGroupBinding; IP targets no pod has, instance targets: not Kubernetes
- kubernetesBackend: what cannot be napped as one owner, with each reason
- kubernetesBackend: a cluster it cannot reach: its target groups ineligible with the cluster's reason
- kubernetesBackend: pods gone (a workload asleep): its owner cannot be told
- matches: an empty criterion matches everything
- matches: account IDs, values being alternatives
- matches: another account ID
- matches: an OU, five levels deep
- matches: the deepest OU
- matches: an OU the account is not under
- matches: the root matches every account
- matches: account tags with given values
- matches: account tags with another value
- matches: an account tag with any value
- matches: an account tag the account lacks
- matches: every dimension it sets
- matches: a region it does not set
- matches: a load balancer name pattern
- matches: a load balancer name
- matches: another load balancer name
- matches: a load balancer ARN
- matches: another load balancer ARN
- matches: a host pattern
- matches: a host in another case
- matches: another host pattern
- matches: a rule without a host condition never matches a host
- matches: a resource tag value
- matches: a resource tag with any value
- matches: another resource tag value
- matches: a tag value of any of the three resources
- matches: a tag named as an object property
- matches: backend kinds
- matches: another backend kind
- matches: backend kinds never match an ineligible rule
- matches: a target group name pattern
- matches: a target group ARN
- matches: a target group pattern and a rule without a target group
- decide: a global exclude wins over any filter, its note in the reason
- decide: a global exclude with a label
- decide: a filter's exclude applies to that filter only
- decide: every including filter excludes it: the filter and exclude numbers
- decide: no filter includes it: out of scope
- decide: the include that brought it in scope
- scanAccounts: nothing scanned without filters
- targetPairs: nothing scanned without filters
- scanAccounts: an account no filter includes
- targetPairs: an account no filter includes
- scanAccounts: a globally excluded account, by ID
- targetPairs: a globally excluded account, by ID
- scanAccounts: a globally excluded account, through its OU
- targetPairs: a globally excluded account, through its OU
- scanAccounts: an account whose includable regions are all globally excluded
- targetPairs: an account whose includable regions are all globally excluded
- scanAccounts: the same with every region
- targetPairs: the same with every region
- scanAccounts: an account and both its regions globally excluded
- targetPairs: an account and both its regions globally excluded
- scanAccounts: every region, one globally excluded
- targetPairs: every region, one globally excluded
- scanAccounts: a region globally excluded in every account
- targetPairs: a region globally excluded in every account
- scanAccounts: a global exclude with a resource dimension keeps the pair
- targetPairs: a global exclude with a resource dimension keeps the pair
- scanAccounts: regions no include allows
- targetPairs: regions no include allows
- scanAccounts: each account's enabled regions with every region
- targetPairs: each account's enabled regions with every region
- resourceTagsMatch: every key, one of its values
- resourceTagsMatch: a key missing
- resourceTagsMatch: no criterion
- loadInventory: the OU tree walked, pages followed, each active account with its OU path from the root
- loadInventory: account tags read when asked

## `stopped`

What a nap of each backend stops, as the router describes it (router `lambda/src/lib/stopped.mjs` and
`kubernetes.mjs`'s `k8sItems`; Go `internal/stopped`). `input.function` names the function:

- `describeStopped` (Go `stopped.Describe`): `input.backends` as the estimate passes them (`{kind: "instance", id}`,
  `{kind: "asg", name, desired, instances}`, `{kind: "ecs", id, cluster, desired}`, `{kind: "k8s", id, cluster,
  namespace, owner: {kind, name}, autoscaler}`; the router's own `{kind: "rds-instance" | "rds-cluster", id}` too),
  the ports' answers in `input.answers` (`ec2`, `ecs`, `rds`; a Kubernetes owner through `kubernetes.of`, then its
  pods by the owner's label selector, its ReplicaSets, its nodes). Output: the items (`StoppedItem`s; a backend that
  cannot be described is an `unknown` item with the error's message).
- `strategyCounts`: `{strategy, desired}` (a capacity provider strategy). Output: `{provider: count}`.

Recorded answers used: `ec2.describeInstances`, `ecs.describeServices`, `ecs.describeTaskDefinition`, `kubernetes.get`, `kubernetes.list`, `kubernetes.of`, `rds.describeDBClusters`, `rds.describeDBInstances`.

Cases (29):

- describeStopped: EC2 instances by type and OS; an Auto Scaling group at its desired capacity by the types of its instances
- describeStopped: an OS neither Linux nor Windows is said as it is
- describeStopped: Windows with its own license or with SQL Server is said as it is
- describeStopped: a Fargate service, ARM: its task size times its desired count
- describeStopped: a service on the FARGATE capacity provider
- describeStopped: a service on EC2 capacity stops nothing Dormeto counts
- describeStopped: a Windows Fargate service
- describeStopped: a service at a desired count of 0 stops nothing
- describeStopped: a task size the boundary refuses is an unknown item
- describeStopped: RDS instances (Multi-AZ said), an Aurora cluster's provisioned instances, Aurora Serverless v2 at its minimum
- describeStopped: an instance on Spot is said so; a group's on-demand and Spot instances of one type are two items
- describeStopped: a group whose desired capacity was not all described: the rest added to its first item
- describeStopped: a group at 0 stops nothing; one without an instance to read is an unknown item
- describeStopped: Fargate and Fargate Spot: the base first, then the weights, an item each
- describeStopped: Fargate Spot only: one Spot item
- describeStopped: a share on EC2 capacity not counted
- describeStopped: Kubernetes pods on Fargate: the size given (CapacityProvisioned), else the requests plus 256 MB rounded up
- describeStopped: Kubernetes pods on EC2 nodes: requests, the node's type, allocatable and capacity type, its autoscaler (Karpenter; the Cluster Autoscaler found by discovery)
- describeStopped: Kubernetes pods without CPU and memory requests are said, not priced
- describeStopped: a Kubernetes cluster that cannot be reached: an unknown item
- describeStopped: a Kubernetes owner gone: an unknown item with the API's reason
- describeStopped: several backends in order: an instance, a Fargate service and pods on a node
- strategyCounts: the base first, then the weights
- strategyCounts: equal weights over an odd count: the largest remainder, the first provider on a tie
- strategyCounts: a base larger than the count
- strategyCounts: no weight: the base's provider takes the rest
- strategyCounts: an EC2 capacity provider and Fargate Spot
- strategyCounts: three providers, weights 1, 1, 1 over 5
- strategyCounts: a desired count of 0

## `estimate`

Frozen: this section, `estimate.json` and `cache/` were written by the manager's last export with the Node estimate (manager 0.10.0, commit 580867f), are kept in Dormeto/cli as they are and are no longer exported (the Node estimate is replaced by the Dormeto CLI). The Go tests still read them: their format stays documented here.

The Node estimate end to end (`packages/estimate/src/cli.ts` `main`; Go `cli.Main` and the packages under it),
on a recorded world: organization `o-a1b2c3d4e5` (management account `111111111111`, the profile's; member `222222222222` reached
through the role; member `333333333333` refusing it), regions `eu-west-1` and `us-east-1` in each reached account (the
management's `us-east-1` empty, the member's failing).

Input: `argv` (the command's arguments after `estimate`), `now`, `profiles` (`{profile: account}`: the account a
profile's calls are made from, as `sts.getCallerIdentity` answers it), `answers`. The run's dependencies, as `bin.ts`
wires them and the Go command must: `now`; a fresh empty cache directory as `cacheRoot`; no terminal (no
`status`: the log gets a line per account and per region); reports captured, not written. Each profile's AWS:

- the accounts: `sts.getCallerIdentity({})`, `organizations.describeOrganization({})`, the inventory
  (`organizations.listRoots`, `listAccountsForParent`, `listOrganizationalUnitsForParent`), and
  `sts.assumeRole({RoleArn: "arn:aws:iam::<account>:role/<role>", RoleSessionName: "dormeto-estimate",
  DurationSeconds: 3600})` for a member account, all in the profile's account and `us-east-1`;
- an account's enabled regions: `ec2.describeRegions({})` in that account and `us-east-1`;
- a region's ports in that account and region: discovery's `scanRegion` (run at `now`, where `bin.ts` lets it read
  the clock; given `elbv2`,
  `autoscaling`, `ec2`, `ecs`, `cloudwatch` and the EKS clusters with reader "the profile"), then
  `autoscaling`, `elbv2.describeRules`, `ec2` (instances, security groups), `rds`, `ecs` and the clusters' API
  (`kubernetes.of`, `get`, `list`) for what the workloads stop and their companion databases; `cloudwatch` for the
  requests and the uptime;
- the prices: the product's price book made at `now` over the estimate's cache, `pricing.getProducts` in the
  profile's account and `us-east-1`, `spot.history` from the profile's account in the item's region (the first
  refused lookup kept, as `bin.ts` does).

Output: `{exitCode, report, markdown, written, log, errors}` — the exit code; the JSON report parsed, without
`generatedAt` (null when none was written); the Markdown report with its time replaced (`estimated <generatedAt> UTC`
in place of `estimated YYYY-MM-DD HH:MM UTC`; null when none was written); the report paths written (relative to the
working directory: `--out reports`); the lines logged and the lines written as errors, the cache directory replaced by
`<cacheRoot>`. The cases reading several accounts or regions run one at a time (`--parallel 1`), so the log lines
come in an order Go can give too (concurrent reads log in the order they end). The first case's cache directory is
`cache/`.

Where the Go command differs by design (the plan's global constraints): its JSON report adds
`"tool": {"name": "dormeto", "version": …}` (remove it before comparing), and its usage names `dormeto estimate`
where the Node one says `npm run estimate --` (the usage error case's `errors`).

Recorded answers used: `autoscaling.describeAutoScalingGroups`, `cloudwatch.getMetricData`, `ec2.describeInstances`, `ec2.describeRegions`, `ec2.describeSecurityGroups`, `ecs.describeServices`, `ecs.describeTaskDefinition`, `ecs.listClusters`, `ecs.listServices`, `eks.describeCluster`, `eks.listClusters`, `elbv2.describeListeners`, `elbv2.describeLoadBalancers`, `elbv2.describeRules`, `elbv2.describeTags`, `elbv2.describeTargetGroups`, `elbv2.describeTargetHealth`, `kubernetes.get`, `kubernetes.list`, `kubernetes.of`, `organizations.describeOrganization`, `organizations.listAccountsForParent`, `organizations.listOrganizationalUnitsForParent`, `organizations.listRoots`, `pricing.getProducts`, `rds.describeDBClusters`, `rds.describeDBInstances`, `spot.history`, `sts.assumeRole`, `sts.getCallerIdentity`.

Cases (5):

- the world: every account and region, --uptime, --exclude-tags env=dev (its cache is cache/)
- the world without uptime: business hours in Paris, discounts, a 15-minute window, --max-wakes, --min-saving and --max-wakes-rds none, one account at a time
- --accounts (one of them not in the organization), --regions, --role, --max-wakes 1, --min-saving 5
- a bad option: its message and the usage, exit code 2
- credentials that do not work: AWS's message, exit code 1
