# Changelog

## Unreleased

`dormeto gcp connect` and `dormeto gcp routers install` wait for Google to show what they just created: where their verification reads a service account, a role, the pool or the provider that the same run created, a 404 is tried again every 5 seconds for up to 2 minutes (once, on stderr: `Google has not shown what this run created yet`), where it failed at once before (measured live: `google: 404 NOT_FOUND: Service account … does not exist` on an account created seconds earlier). A 404 after 2 minutes, or of something the run did not create, fails as before.

The router's commands are under `dormeto gcp routers`: `gcp routers install`, `uninstall` and `show` (`gcp install`, `gcp uninstall` and `gcp status` of 0.11.0 are gone: unknown commands, with no alias and no notice; so are the top-level AWS names, `dormeto install` or `dormeto routers list`, which are `dormeto aws install` and `dormeto aws routers list`). `--region` is the manager's on the manager's commands and the router's on `dormeto gcp routers` and `dormeto gcp workloads`, said in the help of both subtrees. The old flags are gone too, unknown ones: `--cloud-sql` is `--database`, `--ip-list`'s `refresh=` is `refresh_hours=` and `--wake-timeout` is `--wake-timeout-seconds`; and `default`, the old word of `inherit` at the organization's and the types', is no word any more.

`routers list`, `workloads list`, `discovery candidates` and `discovery access` of `dormeto aws` take `--cloud aws|gcp` (another value exits 2 before any call; `discovery access`'s counts stay the whole report's, and `workloads list --json`'s `byState` the manager's, of both clouds), and every manager command that takes `--account` takes `--project` too, the same flag (but `routers install`, an AWS account only). Their help says they serve both clouds.

Every writing command of both subtrees takes `--yes` (a no-op where nothing is asked) and `--dry-run` (it says each request it would send, `dry run: <target>: would <method> <path>, nothing sent`, its body on the next line, sends nothing and exits 0), but `dormeto aws install`, `uninstall` and their `apply`, whose plan is the step before `apply` and needs the bootstrap first. A dry run's `--json` is `{"dryRun": true, "targets": […]}` on both, with its own key beside them where it has a context: `candidate` (`aws workloads onboard`), `eligibility` (`gcp workloads onboard`), `plan`. The `dormeto aws` commands that had `--dry-run` in 0.11.0 (`workloads nap`, `wake`, `pause`, `resume`, `keep-awake`, `rename`, `tags`, `settings`, `schedule set|clear`, `offboard` and `exclude`, `routers pause`, `resume`, `settings` and `uninstall`, `users role` and `disable`, `groups delete`, `exclusions add`) printed what they would act on, `dry run: nap 2 workloads, nothing sent` and the targets below it: they now say each request in that form too, in the order of the targets, and read first what a request needs (a workload's tags for `tags`, its backend for `exclude`, a read refused exits 1); their `--json` is unchanged. `dormeto gcp workloads offboard` now confirms as AWS's does: on a terminal `Offboard 1 workload?`, and without one it exits 2 with `confirmation needed: pass --yes to act on 1 workload without a terminal` and changes nothing (`--json` too; a script that offboarded without a terminal needs `--yes`); a rerun that only finishes the untag and the access cleanup, because the controller no longer holds the workload, needs it too. A refused `gcp` nap, wake, pause, resume or `schedule set|clear` prints AWS's head, `<router>/<workload>: <refusal>`, on stderr, exit 1.

`dormeto gcp workloads wake --wait [--timeout <d>]` follows the wake until the workload is awake (`<router>/<workload>: awake`; `failed to wake` and `timeout of 15m0s reached: not awake` exit 1), as `dormeto aws workloads wake --wait`. `gcp workloads list` takes `--state`, `--search` and `--tag` (a router before 0.40.0, which answers no tags, is refused for `--tag` with the version to install). `gcp workloads pause` takes `--for` and `--until` (an RFC 3339 time or `HH:MM` today), as `gcp routers pause` does, and `--reason` is 1 to 256 characters. `gcp workloads schedule set --file` reads `-` and the output of `schedule show --json`. A `gcp workloads nap` of a workload asleep already says `<router>/<workload>: not awake (ASLEEP)`, AWS's refusal words, as an answer: it exits 0, as 0.11.0 did, since the controller does not refuse it (AWS exits 1).

`dormeto aws routers settings <router>...` without a setting flag shows each router's level (it was a usage error, exit 2), in the words of `gcp routers settings`, `--json` by router. `inherit` unsets a setting on every level, the organization's (`aws settings set`) and a type's (`aws types set`) too. The flags of the four commands that set a level (`aws workloads settings`, `aws routers settings`, `aws settings set`, `aws types set`) name Google Cloud's limits as their level has them: at a router's and a workload's, which the Google Cloud router checks, a wake timeout of 60 to 1700 and a hold, held requests and a drain of 0 (any value refused, `inherit` accepted); at the organization's and the types', which the manager takes for both clouds and refuses none of, a Google Cloud workload takes 0, and at most 1700 for the wake timeout, whatever they set. The shared flags of both onboards (`--name`, `--set-tag`, `--idle-minutes`, `--wake-mode`, `--wake-timeout-seconds`, `--hibernate`, `--fallback-type`, `--database`) have one description each, in one order, with the levels' defaults (the idle window's 15 is built in, no longer "the router's").

`dormeto aws workloads keep-awake --reason` is checked as every pause's reason: 1 to 256 characters once trimmed, before any call, one out of range exiting 2 (0.11.0 sent it, and the manager's refusal exited 1).

A router the manager can't use, which the manager answers `409` with its message and the command that fixes it (`no_access`, `router_too_old`, `router_unknown`; it was a `502` `router_error` with a generic message), is printed with that message at once, where 0.11.0 tried a read three times, by every command of `dormeto aws` that reaches it (a Google Cloud router too; manager 0.24.0).

`dormeto aws workloads onboard` no longer pins 15 minutes and `any`: it sends `--idle-minutes` and `--wake-mode` only when given, and the levels decide otherwise (their built-ins are 15 minutes and `any`, so a workload onboarded without the flags behaves as before unless a level says else). It takes `--wake-timeout-seconds` (60 to 3600, 1700 on a Google Cloud candidate). `dormeto gcp workloads onboard` takes `--database` (a Cloud SQL instance, repeatable, sent as `cloudSql`); `--cloud-sql` goes on, hidden, saying `--cloud-sql is now --database`. Both onboards list their shared flags in one order and wording.

`dormeto gcp`'s output changes once, to AWS's. Every workload write prints AWS's line `<router>/<workload>: <what happened>`, the router by its name in the manager (`dmtr-<project>-<region>/vm-demo: nap requested`; `wake requested (<state>)`, the state awake or napping too, `not awake (ASLEEP)` for a nap of a workload asleep already, `paused until resumed` or `until <local time>`, `; woken <workloads>` when the router's answer names them, `resumed`, `schedule set`, `schedule cleared`, `onboarded (<state>)`, `offboarded`, `waiting page set`), and the router's writes `<router>: …` (`dmtr-<project>-<region>: paused`). Under `--json` a write's line goes to stderr and its stdout is AWS's map `{"<router>/<workload>": answer}` (`{}` when the write is refused, an onboarding or an offboarding too), but `waiting-page set` and `clear`, which print the controller's answer as it is, and nothing when refused, as AWS's `waiting-page set` and `clear` do; the router's `{"<router>": answer}` (`routers pause`, `resume` and `settings`, its level by router with no flag too), and `workloads list --json` AWS's list object `{workloads, total, byState, cursor, errors}` (it was the controller's array). A workload in them, and in `workloads show --json`, is a row as the manager's are, whatever the router's version: `router` (`dmtr-<project>-<region>`), `account` (the project), `region`, `cloud: "gcp"`, `workloadId` (was `id`), `pauses`, each with its `scope` (was `paused`), and its backends' kinds `instance`, `asg`, `k8s` and `cloudsql-instance` (the router's `vm`, `mig`, `k8s` and `cloudsql`, which stay on the wire), which the text says too (`backend: instance web-1 (europe-west1-b)`); `workloads offboard --json`'s answer names it `workloadId` (was `id`). A dry run says each request alike on both subtrees, `dry run: <target>: would <method> <path>, nothing sent` and its body on the next line, and under `--json` prints the released `{"dryRun": true, "targets": […]}`, each target as the command addresses it (`<router>/<workload>`, a router, an account, `organization` for the organization's own settings): `gcp workloads onboard --dry-run --json` puts the controller's answer in `eligibility` beside them (it was that answer alone), and the dry runs that are a plan, `gcp routers install`, `routers uninstall` (which take `--json`), `connect` and `disconnect`, their `plan`. The confirmations of `gcp routers install` and `uninstall`, `connect`, `disconnect`, `workloads access` and a GKE `workloads onboard` say AWS's lines: `confirmation needed: pass --yes to act on 1 router` (or `connection`, `workload`) `without a terminal`, on stderr (was `--yes: needed`, exit 2 as before), and `not confirmed: nothing sent` (was `Nothing was changed.`), with `{"confirmed": false, "targets": […]}` (and the `plan`) under `--json`, whose questions go to stderr; every `--yes` has AWS's help. `workloads access grant` and `revoke` and a GKE `workloads onboard` needed `--yes` with `--json`, exit 2 even on a terminal: on a terminal they now ask there too, on stderr after what they would write, a decline printing `{"confirmed": false, "targets": ["<namespace>/dormeto-<workload>"]}`. Every `--json` of `dormeto gcp`, `gcp check` and `gcp routers show` too, is indented as AWS's, by two spaces (it was one line).

`dormeto aws types show`, `set` and `delete`, `aws install check` and `aws install status` take `--json`: `types show` prints the type as the manager holds it and `builtIn`, `set` and `delete` the manager's answer `{types, builtIn, note}`; `install check` prints `{env, account, region, ok, checks: [{name, detail, verdict}]}` and `install status` `{env, account, region, version, latest, upgradeAvailable, tags, builds: {plan, apply, dashboardUpdate}, checks, ok}`, their words on stderr.

`dormeto aws workloads idle-window` prints `Google's request counts are estimates.` when the manager marks the advice `estimated` (a Google Cloud workload's; AWS's now says `estimated: false`, a manager before this field sends none), says `the waker backend service's requests were not found` (not `target group`) for such a workload, and `idle-window` names `dormeto aws workloads settings <router>/<workload> --idle-minutes <n>` to change the window (an AWS workload onboarded with Terraform: `idle_minutes` in its module; a Google Cloud workload's note has no such clause; the workload's cloud is its router's in `routers list`, read for the text, else the advice's `estimated`), as `idle-window --help` names the estimate line. `dormeto aws savings show` and `dormeto aws workloads idle-window` serve both clouds, as the README says (Google's list prices, Google's request counts estimated). The `savings discounts` help names Compute Engine and Cloud SQL (`never off Spot` after the whole compute list), and `savings show`'s not-priced text says `the list prices` and names what the router could not describe (GKE pods among them), not `the cloud's list prices`. Needs manager 0.24.0.

Three fixes the live test on Google Cloud found. `dormeto aws workloads onboard` of a Google Cloud candidate sends no `wakeMode` unless `--wake-mode` is given (the levels decide, as the dashboard's Inherit: every global candidate was refused at preview with the default `any`); so does an AWS candidate's, as `dormeto aws workloads onboard` sends neither unless given (above). `dormeto aws discovery access` prints `DISCOVERY` and `COMMAND` columns (it was `DISCOVERY ROLE` and `COMMAND ROLE`), takes a row's cloud from the account's form when the manager sends none (a project prints `gcp`), and its counts say `without the discovery role or account` and `without the command role or account` when both clouds are listed (AWS only: unchanged). `dormeto gcp routers install` lists the grants `dormeto gcp connect` made on the project (`dormetoDiscovery` and `dormetoCommand` to the manager's `dmt-<env>-discovery` and `dmt-<env>-command`) among the IAM changes since the last run as `(the manager's connection: dormeto gcp connect)`, not as a change of a stranger's.

`dormeto aws workloads onboard <candidate>` takes a Google Cloud candidate (a backend service named in a URL map), by its id, the backend service's path `projects/<project>/global|regions/<region>/backendServices/<name>`, or by the name of its backend service. For it, `--database` is a Cloud SQL instance name (sent as `{kind: cloudsql-instance, id}`), `--fallback-type` a Compute Engine machine type such as `e2-medium` (at most 5), and the new `--router` names the router to onboard a global candidate on when its project has several (a regional one goes on its region's router; refused for an AWS candidate); a `--fallback-type` of the other cloud, a `--database` that isn't a Cloud SQL name, or `--router` for an AWS candidate exits 2 before anything is sent. A repeated machine type or Cloud SQL instance exits 2 too, a `--fallback-type` of neither cloud's form is refused before the manager is read, `discovery dismiss` takes a backend service's path as an id (sent as given, a candidate of any status), `--candidate-region` is checked against the account's cloud, and `exclusions add --account <project>` and `exclusions list` word a project as one. `workloads exclude` of a Google Cloud workload excludes its backend service, whose path the command builds from the workload (`regions/<region>` for a regional or internal load balancer, else `global`), and `workloads offboard` takes it by `<router>/<workload id>`, the manager refusing a GKE workload and one whose onboarding turned request logs on with `dormeto gcp workloads offboard`. `dormeto aws exclusions add` takes either cloud's value: `--target-group` an ARN or a backend service's path, `--load-balancer` an ARN or a URL map's path, `--account` 12 digits or a project ID. `dormeto aws discovery candidates` prints `TARGET GROUP OR BACKEND SERVICE`, `CLOUD` and `ACCOUNT OR PROJECT` columns, and `--account` and `--candidate-region` take a project and a Google Cloud region (or `global`). These need router 0.40.0 on Google Cloud; the manager's own limits (GKE, request logs) stay with `dormeto gcp workloads onboard`.

The manager's commands print both clouds. `dormeto aws routers list|show`, `workloads list|show` and `discovery access` gain a `CLOUD` column (`aws` or `gcp`; `show` a `cloud` line) and the account column's header becomes `ACCOUNT OR PROJECT` (`routers show` and `workloads show` print `project` for a Google Cloud row); `--json` passes the manager's `cloud` field through. A Google Cloud router is `dmtr-<project>-<region>`, source `cli`. `routers install --account <project>` is refused before any call (exit 2) with `dormeto gcp routers install --project <p> --region <r>`; `routers upgrade` (also `--all`) skips a Google Cloud router below the version available with one line naming `dormeto gcp routers install --project <p> --region <r> --version <available>`, as the manager and its dashboard do (one up to date, newer or of unknown version is skipped as an AWS one is), `routers uninstall` skips each Google Cloud router with one line naming `dormeto gcp routers uninstall`, and both go on with the AWS ones. `--account` selects Google Cloud projects too. `discovery access` lists `NAME`, `CLOUD`, `ACCOUNT OR PROJECT`, the routers list's order. `routers show` of a Google Cloud router prints no Lambda, queue, concurrency or wake statistics metrics row, and its `hibernate` and `join windows` rows inherit the organization level the manager pushes to it (its `managerSettings.url` is always null), the built-in value until one is pushed. AWS rows print as before, but for the new column.

`dormeto gcp connect --project <host> [--scope organizations/<n>|folders/<n>|projects/<id>]... --profile <aws profile> --env <env> [--manager-region <aws region>] [--api-url <url>] [--gcloud-configuration <name>] [--dry-run] [--yes] [--json]` lets the Dormeto manager reach Google Cloud. It reads the manager first: version 0.24.0 or later (a dev build of 0.24.0 counts), its AWS account and its environment. Then, as `dormeto gcp routers install`, it tests your permissions at the host and at each scope for the steps that act there, finds the routers at the scopes (`dmtr-<region>-controller`), prints the plan, asks, applies, verifies and writes the manager's record (`PUT /iam/discovery/google`, the routers with their controller's URL and version), which registers the routers at once. The plan is, from the manifest's new `manager` section: the APIs `iam`, `iamcredentials`, `sts`, `cloudresourcemanager` and `cloudbilling` in the host; the custom roles `dormetoDiscovery` (in each project scope, or in a folder's or an organization's organization with its organization permissions) and `dormetoCommand` (in each router project); the workload identity pool `dmt-<env>` and its provider `aws`, condition `assertion.arn.startsWith('arn:aws:sts::<account>:assumed-role/dmt-<env>-manager-')`; the service accounts `dmt-<env>-discovery` and `dmt-<env>-command`, each impersonated (`roles/iam.workloadIdentityUser`) by its three manager roles' principal sets; `dormetoDiscovery` at each scope; and, per router, `dormetoCommand` on its project, `roles/run.invoker` on its controller and `roles/resourcemanager.tagUser` on `<project>/dormeto/managed`. A first connect writes the record before its first step, without its routers, so that a run stopped partway leaves what it made for `disconnect`, then whole at the end; a later connect that adds scopes writes them first too, with the routers already recorded; a run stopped after its steps says what the record holds. A second run says `up to date` or plans only a new router's grants, and takes back the command account's grants in a project left without a router at the scopes (`routersGone`): on the project, its tag value and, for a project moved out of the scopes, its recorded routers' controllers (`roles/run.invoker`). The router projects are tested for the tag step before their tag values are read, as Google answers a value you may not read as one that does not exist. The commands a run names keep `--manager-region` and `--api-url`. A custom role's description, `Dormeto <version>: …`, takes the version's core when a dev build's long version would pass Google's 256 characters (`gcp routers install`'s roles too). A record made for another environment than `--env`'s (its pool and accounts) is refused, exit 2, nothing changed. The pool, the provider and the roles that Google keeps after a deletion are undeleted; a role ID Google blocks stops the run with the date it frees (at the latest 44 days after the deletion, counted from the manager's last disconnect in its audit). `dormeto gcp disconnect` removes, from the record and under its names, every binding of the two accounts at the recorded scopes and router projects, and at every project of the scopes, or the host, that holds a `dormetoCommand` (a former router project, or a stopped first connect's), the two roles, the accounts, the provider and the pool, verifies, clears the record (`the manager's Google Cloud routers unregistered`: the manager's `DELETE` unregisters all of them), and lists the APIs and Google's bindings as left in place. It tests the host for its bindings and roles before any change. A project that has left the connection's scopes is not read: it keeps the `dormetoCommand` a connect left there, granted to no one. `--manager-region` is the fleet commands' `--region`: every `dormeto gcp` command's `--region` is a Google Cloud region. The CLI embeds router 228bd0c's `gcp/access.json`; `dormeto gcp check` lists `connect` and `disconnect` too, tested for what they do in a project, their folders' and organizations' bindings tested there by the commands.

`dormeto gcp connect` and `disconnect` go on past the regions Cloud Run answers as `unreachable` (regions the project can't use): they warn once per project, on stderr and in `--json`'s `warnings` (`Cloud Run did not list the services of project <p> in <regions>: a router there, if any, isn't seen by this run`), and a router the record holds there keeps its grants and its entry, granted nothing again.

`dormeto gcp workloads show` prints why the last nap was given up, `nap given up: <reason>, <time>` (`backends-changed`, `traffic`, `traffic-unread`, `map-busy`, …), until a nap succeeds; only `--json` carried it (`napAborted`).

`dormeto gcp routers install` installs the router from Dormeto's public image, `<europe|us|asia>-docker.pkg.dev/dormeto-releases/router/router:<version>` in the multi-region of `--region`, read without a token and deployed by its digest (`<name>@sha256:…`), which the plan shows: a first install takes the router version this CLI's access describes, a later run upgrades an older router of Dormeto's to it and keeps an image of `--image` (saying which public version is available), and a router newer than this CLI's stops the install before any change (`brew upgrade dormeto`). `--version` installs another version, up to that one. `--access-only` installs the access without the router, as a run without `--image` did: the record says so (`accessOnly`), later runs keep it, and an older CLI's install without an image is read as such. `--image` stays, for an image of your own, and only it needs Artifact Registry's API and the image's permissions: `gcp check --image` tests them before a first install with it.

`dormeto gcp routers uninstall` no longer orphans a database or a tag whose creation was still running when an install stopped: the install records the creation's operation, and the uninstall waits for it before it deletes the resource.

`dormeto gcp routers install` creates the router's database right after the APIs, so the install record is in it before the roles, the tag, the service account and the grants: a run that stops at any of them leaves them listed in the record, and another region's uninstall keeps the roles and the tag they share. A rerun that finds the database without the record (a run stopped during the database's creation) writes the record there at its first write.

The idle check can read the load balancer's request logs, with router 0.40.0, and ignore what the waker ignores (scanners, `HEAD` requests, IP lists) instead of counting every request. On a backend service that logs every request, any entry in the window makes the logs decide alone. Monitoring's count, which is sampled, decides when the logs hold no entry, when a read fails, and past 1,000 entries or 10 pages. An empty log also catches a log excluded upstream, not one lost only in part. A scanner that sends a browser's user agent to the application's own paths counts as a visitor; IP lists catch known scanner networks. A regional load balancer's logging begins 2 to 5 minutes after `--request-logs` (measured on 2026-11-08). A classic load balancer on the Standard network tier is accepted for the swap (measured on 2026-11-08). `dormeto gcp routers install` creates the router's log view `dmtr-<region>-lb` on the `_Default` bucket, filtered to the load balancers' resource types (`http_load_balancer`, `http_external_regional_lb_rule`), before the project's bindings, and grants the router `roles/logging.viewAccessor` on the condition that the resource is that view: never the project's other logs. The plan shows both, a view of another filter is updated, the verification reads its filter, and the controller gets `DMT_LOG_VIEW`. `dormeto gcp routers uninstall` removes the grant, then the view. In a project whose `_Default` bucket is not in the global location, the view's creation answers 404: the install says `! the router's log view isn't created: the project's _Default log bucket isn't in global; the idle check counts every request` and goes on without the view, its grant and `DMT_LOG_VIEW`; the record says so (`noLogView`), `gcp routers show` shows the same line, the uninstall has neither to remove, and a later run tries the view again. `gcp check` tests `logging.views.create`, `get`, `update` and `delete`, and the onboarding's and offboarding's `compute.backendServices.get|update`, `compute.regionBackendServices.get|update`, `compute.globalOperations.get` and `compute.regionOperations.get`: its onboard and offboard lists are what `--request-logs` needs; an onboarding without it asks only `compute.backendServices.get` and `compute.regionBackendServices.get`, and an offboarding asks them, the updates and the operations' reads only for a workload whose onboarding turned its request logs on (`uninstall --force` asks them all).

`dormeto gcp workloads onboard --request-logs` sets the backend service's `logConfig` to every request (`enable`, sample rate 1.0, its other fields kept), as you, after the dry run and before the onboarding, which records what it found (`requestLogs.before`), read again just before the update; a refused or interrupted onboarding sets it back. What it found waits in the cache from before the update until the onboarding answers: a rerun after a stop between the two (a lost answer, a killed run, a set-back that failed) finds the logs on and sends it. A backend service logging every request already is left as it is. Without the flag, an onboarding of a backend service that does not log every request ends with `! the idle check counts every request, scanners included: --request-logs reads the requests instead (Cloud Logging's price past its free tier)`, on stderr under `--json`; a backend service it cannot read is said (`! the request logs of backend service <name> are not read: …`) and the onboarding goes on, where `--request-logs` stops before any change. The request logs need router 0.40.0 or later, which the onboarding reads from the controller's `/v1/health` before any change: on an older router, which drops `requestLogs`, `--request-logs` stops with `--request-logs needs router 0.40.0 or later: dormeto gcp routers install --version <default>` (exit 2), and an onboarding without it prints no warning. `workloads offboard` (and `uninstall --force`) sets the `logConfig` back after the controller's delete when it is still what Dormeto set, and says so (`restored:`); changed since, it is left, with a notice naming the backend service. A rerun after a stop finishes it from the cache. `workloads show` says what the idle check reads: `idle check: request logs` or `idle check: request count`.

The README's Google Cloud section documents what it missed: `gcp check` also checks the billing account, `gcp routers show` without `--region` shows every router of the project, `gcp routers uninstall --with`, and `gcp workloads access revoke` asking, with `--yes`. It also describes the public router image and the request logs, and names the website's page Access, not Organization access.

`dormeto gcp workloads settings <id>... --project P --region R` and `dormeto gcp routers settings --project P --region R` set the settings levels of the Google Cloud router, on the flags and with the lines of `dormeto aws workloads settings` and `dormeto aws routers settings`: `--idle-minutes`, `--wake-timeout-seconds`, `--failed-retry-seconds`, `--wake-mode`, `--no-wake-rules-file`, `--wake-rules-file`, `--hibernate`, `--type`, `--dry-run`, `--yes` and `--json` on a workload, the same but `--type`, plus `--join-minutes`, on the router, `inherit` giving a value back to the levels. A dry run says each workload's request, `dry run: <router>/<workload>: would PUT /v1/workloads/<workload>/settings, nothing sent` and its body, and under `--json` a dry run prints `{"dryRun":true,"targets":[…]}` and a declined question `{"confirmed":false,"targets":[…]}` on stdout, the words on stderr, as AWS's commands do. A rules file must be a JSON list, checked before any call (exit 2). The router checks a rules file's rules, with AWS's words: a workload whose rules it refuses gets nothing written and its problems printed, the others go on, and the command exits 1. `--wait-seconds`, `--max-holders` and `--drain-seconds` are 0 on Google Cloud: any value is refused before any call (exit 2) with the router's reason, `inherit` is accepted. `--wake-mode any` is passed on, the router deciding by the workload's load balancer. `dormeto gcp routers settings` with no flag shows the router's level. `dormeto gcp workloads show` prints each setting with the level it comes from (`set on this workload`, `type <name>`, `router`, `built-in` or `fixed on Google Cloud`; `--json` says `workload`, `type`, `router`, `default` or `fixed`), then the type, the hibernation and the no-wake and wake rules of every level. A router without the levels answers 404, which the CLI says as `router <version> doesn't have the settings levels: dormeto gcp routers install --version <this CLI's>`.

`dormeto gcp workloads onboard` takes AWS's flags: `--wake-timeout-seconds`, `--wake-mode any|browser` and `--fallback-type`, repeatable, at most 5 (a machine type such as `e2-medium`, for the VMs of unmanaged instance groups and zonal network endpoint groups; the router checks them against each VM's zone). An empty `--wake-mode` is refused (exit 2), as an empty `--load-balancer` or `--gke-cluster` is. `--load-balancer` takes `internal` (a regional map) and `cross-region-internal` (a global one). With `--request-logs`, the onboarding turns on the logs of the backend service in the map's scope: global, or regional in the router's region. `dormeto gcp routers install` filters the router's log view to `internal_http_lb_rule` too, so a view installed before is updated. `dormeto gcp workloads offboard` names each VM that keeps a fallback machine type, `VM <name> is on <type>, its own is <type>: it keeps <type> until you change it by hand`, as soon as the router answers, before the tag bindings go (on stderr under `--json`), and `--json` carries the router's `fallbacks`; `dormeto gcp routers uninstall --force` names them too, for each workload it offboards. `workloads show` reads `fallbacks` (AWS's shape) for its `running on a fallback type:` line. The wake timeout's range is 60 to 1700 on every Google Cloud command.

`dormeto gcp workloads waiting-page show|set|clear <workload>` and `dormeto gcp routers waiting-page show|set|clear` show, set and clear the branded waiting page of a workload and of the Google Cloud router, with AWS's flags (`--icon`, `--title`, `--description`, `--html`), checks (`--html` with a template flag, an icon over 64 KiB or of another type, a page over 256 KiB: exit 2, before any call) and lines. A custom page stored while the router's custom pages are off is shown as `off`, with the router's warning; setting one is refused with the router's words (`dormeto gcp routers install --custom-pages`). A router without the waiting page says so, with the version to install.

`dormeto gcp routers install --custom-pages` turns the router's custom waiting pages on. Custom HTML runs on the application's address, and on Google Cloud anyone who can invoke the controller sets a page, so the pages are off unless an administrator's install turns them on. The flag sets `DMT_CUSTOM_PAGES=1` on both Cloud Run services and `customPages` in the install record. The plan shows it: `~ service <name>: environment DMT_CUSTOM_PAGES` for each service, then `custom pages on: a custom waiting page's HTML is served on its application's address`. `--custom-pages=false` turns them off, and the plan says `custom pages off: a custom waiting page is refused, and one already stored is not served` whenever a run takes the variable from either service. Turning them off is recorded before either service is written, so a run stopped between the two is finished by the next one, with or without the flag; turning them on is recorded after both, so a stopped one stays off. A run without the flag keeps the record's. The flag changes the two services, so it needs the router: with `--access-only`, or on an install of the access only without `--version` or `--image`, it is refused before any change (exit 2), as `--ip-list` is. `gcp routers show` prints `custom pages: on` or `off` from the record, and exits 1 when a service's `DMT_CUSTOM_PAGES` differs from it, as after a `--custom-pages=false` stopped between the two services. The custom role `dormetoRouterBackends` gains `compute.instances.setMachineType`, for the router's fallback machine types: `dormeto gcp routers install` adds it to an installed role, and its plan says `~ role dormetoRouterBackends: description "…", + compute.instances.setMachineType`.

The values Google Cloud imposes are named `fixed` in `workloads show`, with their reasons in the README: a hold, a held-request count and a drain of 0, the wake mode `browser` behind the global load balancer's policy, a wake timeout of at most 1700 s, and more than the swap's wait + 60 s behind a swap. An inherited value outside these is brought into them, and an explicit one is refused.

These change for workloads onboarded before, with the router that carries the settings levels:

- Behind a swap (the regional, classic and internal kinds), a workload that inherits the wake mode now wakes on a plain request, `any`, as on AWS: a request the filters let through wakes it, and is answered with the waiting page at once. Set `--wake-mode browser`, on the workload or the router, to keep the behaviour of before, where only the page's probe, an explicit wake or a wake rule woke it.
- The built-in wake timeout is 600 s for every kind of workload. The computed 300 s for pods and the swap's wait added to the default are gone, and the swap's floor stays (more than 160 s regional, more than 310 s classic, more than 90 s internal, more than 210 s cross-region internal, shown `fixed` when it applies). A workload with a Cloud SQL companion keeps 1700 s, the most a wake can take, now shown `fixed` too.
- Behind the global load balancer's policy, a request that the filters let through counts in the stats as `deferred:browser mode`, scanners included. A visitor on a `.php` URL, or on a path a no-wake rule keeps, gets a page whose probe never wakes the workload, as on AWS.
- The wake header (`x-dormeto-wake: 1`) and the probe header now wake under a swap and on the policy's fetch, as on AWS; whether Google's error fetch passes a visitor's header of its own was not measured.
- With the request logs, the idle check leaves out what the waker leaves out: not scanners, not `HEAD` requests, not an IP list's addresses, and now not what a no-wake rule of any level matches (on the method, the path, the user agent and the address a log holds).
- As on AWS, a router level's `joinMinutes` joins a workload's windows when its schedule sets none (`gcp routers settings --join-minutes`), for the check, the waker and `schedule show`.
- A backend service with two VMs of one name in two zones is refused at the onboarding, `backend service <service> has two VMs named <name> (zones <zone> and <zone>): Dormeto records a workload's VMs by name; rename one`.
- A router's `dormeto gcp routers install` must run again to give the role `dormetoRouterBackends` `compute.instances.setMachineType`: the plan shows it, and without it a wake that needs a fallback machine type fails with `the router's role lacks compute.instances.setMachineType: run dormeto gcp routers install again`.

The README's Google Cloud section documents the settings and their levels, what a rule sees on each request (a visitor's behind a swap, the page's probe, the policy's fetch, an explicit wake), the waiting page and its custom-pages switch, the capacity retries and the fallback machine types (an unmanaged group's VMs and a zonal network endpoint group's, a managed group refused), the internal load balancers and their limits, and GKE's exact refusal: a URL map named `k8s2-`, `k8s-um-`, `gkegw1-` or `gkemcg1-`, or `mci-` and a six-character hash, or described by `k8sResource` or `kubernetes.io/ingress-name`, is refused, and a standalone network endpoint group on a load balancer of your own is the way.

`dormeto gcp routers pause --project P --region R --reason <text> [--for <duration> | --until <time>] [--keep-asleep] [--dry-run] [--yes] [--json]` and `dormeto gcp routers resume --project P --region R [--dry-run] [--yes] [--json]` pause and resume the whole Google Cloud router, on the flags and with the lines of `dormeto aws routers pause` and `resume`: `--reason` 1 to 256 characters, `--for` a duration up to 366 days (`90m`, `2h`, `3d`), `--until` an RFC 3339 time or `HH:MM` today in the local time zone, within 366 days, both together refused (exit 2, before any call), `--dry-run` printing the router and the body and sending nothing, `--yes` accepted (one router asks nothing), `--json` printing the controller's answer by router (`{"<router>": …}`, as AWS's), neither pausing until `resume`; without `--keep-asleep` the workloads asleep are woken and printed. They call the controller's `POST /v1/pause` and `POST /v1/resume` as you (`by` is the gcloud account); a pause of one workload (`dormeto gcp workloads pause`) stays when the router is resumed. A router below 0.40.0 has no route for them, which the CLI says as `router <version> doesn't have the router's pause: dormeto gcp routers install --version <this CLI's>`. `dormeto gcp workloads onboard` takes AWS's `--name` (the workload's name for people, up to 64 characters, no control characters; default its ID) and `--set-tag k=v`, repeatable, with AWS's checks before any call (exit 2): at most 50 tags, a key of 1 to 128 characters, a value of up to 256, letters, digits, spaces and `_ . : / = + - @`, no key starting with `dormeto:`, a key once. They are sent only when given, and the router sets the source (`cli`). `dormeto gcp workloads show` prints `name:` and `tags:` (`--json` carries `name` and `tags`). Routers 0.40.0 and later: with either flag, an older router, which drops them, is refused after the dry run and before any change, with the install to run (exit 1).

`dormeto gcp connect` and `disconnect` refuse a record made for another environment, or under an older provider name (`aws`, now `aws-<account>`), naming every field that differs and only those: `--env: the manager's connection names provider aws, not those of environment dev (aws-121014831971)`.

Every writing command of `dormeto aws` takes `--yes`, a no-op where it asks nothing, as `dormeto gcp routers pause` does: `types set|delete`, `users invite|enable|resend|reset-mfa|groups`, `groups create|update`, `discovery dismiss|restore|run`, `exclusions lift`, `manager check`, `routers install|description`, `workloads onboard|description|idle-window`, `savings discounts`, `settings set` and the `waiting-page set|clear` of the settings, the routers and the workloads. They answered `unknown flag: --yes`.

`dormeto aws workloads settings --help` says what `--hibernate` does on each cloud: AWS, EC2 instances launched with hibernation are hibernated, not stopped; Google Cloud, VMs are suspended, not stopped.

`dormeto gcp workloads onboard` and the other workloads commands that call the controller add `the router's access may still be propagating (Google applies new grants within minutes): try again in a few minutes` to a controller's 500 within 10 minutes of the install record's `installedAt`, or whose body names Google's 403 (`Missing or insufficient permissions`), where they printed `internal error` alone.

## 0.11.0

Workload settings at scale, with manager 0.22.0 and routers 0.37.0: `settings set`, `routers settings` and `workloads
settings` take `--idle-minutes`, `--wait-seconds`, `--drain-seconds`, `--wake-timeout-seconds`, `--max-holders`,
`--failed-retry-seconds`, `--wake-mode`, `--no-wake-rules-file` and `--wake-rules-file` (`default` or `inherit` removes one), and
`workloads settings` takes `--type`. `dormeto aws types` lists, shows, sets and deletes the organization's workload types.
`workloads show` says where each setting comes from and the workload's type.

Google Cloud's coverage, with router 0.37.0: regional and classic load balancers, zonal network endpoint groups,
schedules, pauses, IP lists, Cloud SQL companions and GKE pods. Their commands came with 0.8.0, written before Google was
measured; router 0.37.0 carries what the measures of 2026-11-04 changed. Accepted the same day in `dormeto-dev` (vision plan
2026-10-30), but for ten workloads on one URL map.

- Cloud SQL and GKE are no longer said to have never run against Google: `gcp check`, `install`, `uninstall --with`,
  `workloads onboard --cloud-sql|--gke-cluster` and `workloads access` print no warning of them.
- A billing read refused for quota (Google's 429, after the client's own tries) is a warning, `! billing not checked`, as a
  refused one is: gcloud's token has no quota project, so that read counts against a quota Google shares between gcloud's
  users, and it stopped an uninstall in the acceptance.

- With router 0.37.0, `gcp workloads onboard --cloud-sql` takes a primary with read replicas, which an older router
  refuses. Google stops only the primary (measured), so the onboarding prints `! Cloud SQL instance <name> has read
  replicas (<replicas>): Google stops only the primary, its replicas keep running, and billing, while the workload
  sleeps.` and goes on, on stderr under `--json`. A read replica is still refused.
- The README's Google Cloud section documents each part: the optional access (`--with cloud-sql,gke`) and what Google
  adds per feature, as measured (turning `container.googleapis.com` on adds `roles/container.serviceAgent`,
  `roles/file.serviceAgent` and six APIs, a GKE cluster `roles/container.defaultNodeServiceAgent`, Cloud SQL nothing on
  the project); IP lists (`--ip-list`, `--no-ip-lists`); schedules and pauses (`workloads schedule`, `pause`, `resume`);
  the onboarding's `--load-balancer`, `--wake-timeout`, `--cloud-sql` and `--gke-cluster`; the Kubernetes access
  (`workloads access show|grant|revoke`); `offboard --force`, whose `notGivenBack` exits 1; and the limits to know, such
  as a `--keep-asleep` workload that a visitor still wakes, a listed address's `POST /_dormeto/wake` that still wakes, a
  database that belongs to one router, and a GKE Standard cluster's nodes, paid all the same. It says how far each part
  has been tried, Cloud SQL and GKE now measured on Google, and its example installs router 0.37.0.

## 0.10.0

- `waiting-page set`: the title and the description take Markdown from router 0.36.0, as the README says. The CLI sends them
  as it did.
- `workloads schedule show` and `workloads show` print a recurrence in words: `Every 2 weeks on Mon, Thu, 20:00–07:00 next day,
  from 2027-01-04, no end date`, `Monthly on day 15`, `Yearly on 25 Dec`, `Once on 2027-01-04` (daily with `from` and `until` the same day), `Cron 30 8 * * 1-5, 10 h 30 min`. A plain weekly window
  prints as it did. So does `gcp workloads schedule show`.
- `schedule set --file`, with `--tag` too, and `schedule show --json` carry `repeat`, `from` and `until`. It needs router 0.36.0:
  an older one's `unknown field` now says so. The README has a Schedule recurrences section.
- Next events of a schedule with a `repeat` or a `from` say their year: they can be up to 400 days away.
- A window's own `exceptions` (router 0.36.0), the dates it does not start on: `schedule show` says them last
  (`except 2027-01-04 and 2027-01-11`, three at most), `schedule set --file` carries them, and an older router's `unknown field`
  for them says it needs 0.36.0.
- Joined windows (router 0.36.0): `joinMinutes`, the largest gap that joins two windows of one kind, 0 to 120, a multiple of 5,
  15 by default. `schedule set --file` carries it, with `--tag` too.
- `settings set --ended-window-days <days>|default` (manager 0.21.0, routers 0.36.0): how long after its last valid occurrence a
  window stays in its schedule, 30 to 3650 days, 0 keeps them, a year by default; `settings show` prints it.
- `settings set` and `routers settings` take `--join-minutes <minutes>|inherit`, alone or with `--hibernate`; what is not given
  stays as it was, and `--hibernate` is no longer required when `--join-minutes` is given. The organization's needs manager
  0.21.0 or later, which the CLI says.
- `schedule show` and `workloads show` print `join windows up to 15 min apart (organization)` or `windows never join (workload)`,
  `settings show` and `routers show` a line of their own, and `schedule show --json` adds `joinMinutes`, `joinFrom` and
  `joinInherited`. So does `gcp workloads schedule show`. An older router prints nothing new.
- A router's `unknown field` or `unknown setting` for `joinMinutes` says it needs router 0.36.0, as for the recurrences.

## 0.9.0

- Every command that was at the top level moves under `dormeto aws`, as the Google Cloud ones are under `dormeto gcp`:
  `dormeto aws install`, `dormeto aws routers list`, `dormeto aws estimate`, and so on for alerts, api, audit,
  discovery, estimate, exclusions, groups, install, ip-lists, manager, operations, overview, routers, savings,
  settings, uninstall, users, whoami and workloads, with their subcommands, options, exit codes and output unchanged.
  `dormeto --help` lists `aws`, `gcp`, `version` and the help and completion commands; every hint, `Next:` line, error
  message and usage names the new form. The old names still work, hidden from the help and the completion, with the
  same output and exit code, and each prints one line on stderr: `dormeto: "dormeto install" is now "dormeto aws
  install"; the old name goes in a later release.` They go in a later release: change scripts and runbooks to the new
  names now.

## 0.8.0

- The branded waiting page (router 0.35.0): `dormeto workloads waiting-page`, `routers waiting-page` and `settings waiting-page`
  (the organization's) `show`, `set` and `clear`. `set` replaces the level's document: `--title`, `--description` and
  `--icon auto|none|<file>` write a template, `--html <file or ->` writes a custom page, and mixing them is a usage error
  (exit 2). An icon file (PNG, JPEG, WebP, SVG or ICO, at most 64 KiB, its type taken from its bytes) and a page (UTF-8,
  not blank, at most 256 KiB) are checked before any call (exit 2). `show` prints the effective icon, title and description
  with the level each comes from, the cached favicon with its date or why there is none, and an icon or a page as its type
  and size, never its content; `--json` prints everything. A write prints the page's warnings; a custom page needs an
  administrator in the dashboard, but a caller of the manager's `/iam/` routes counts as one, so whoever may write these routes can set custom HTML; a router before 0.35.0 is refused with its upgrade sentence. The README has a Waiting page section.
- Google Cloud (router 0.35.0, the manager is unchanged): `dormeto gcp check`, `install`, `status` and `uninstall` for a
  router `dmtr-<region>` in a project, and `dormeto gcp workloads list|show|onboard|offboard|nap|wake` through its
  controller with your identity token. `check` is read-only and says, per command, which permissions you lack. `install`
  checks them, prints every IAM binding it adds, applies, prints the policy difference after each step and records it;
  with `--image` it installs the router too. `uninstall` removes exactly what `install` added, from the install record.
  Every command takes `--gcloud-configuration`; exit codes as `dormeto install`.
- `gcp workloads onboard --url-map <map> --backend-service <name>` asks the controller for a dry run, binds the tag
  `dormeto=managed` to each VM and managed group it names as you (the router holds no permission to tag), then onboards
  the workload awake. `--id` defaults to the backend service's name when it is a valid workload ID, else it is asked on a
  terminal (exit 2 without one); `--idle-minutes` (5 to 1440, the router's range, refused with exit 2 before any call) and `--hibernate true|false`
  are the settings. `offboard --force` (and `uninstall --force`) offboard a workload that cannot wake, leaving its VMs and groups down
  (listed). `nap` and `wake` say the action was asked and how to follow it. `offboard` removes the tag bindings after the controller
  has restored the references, and `uninstall --force` does the same for each workload before it removes the tag.
  The README has a Google Cloud section.
- How far Google Cloud has been tried, in three levels. Accepted on a real project (2026-10-30): `gcp check`, `install`,
  `uninstall`, `status` and `workloads`, with Compute Engine VMs (unmanaged and managed groups, suspended when possible)
  behind a global external Application Load Balancer. Built and reviewed, its acceptance not run yet: regional and classic
  load balancers, zonal network endpoint groups, schedules and pauses, IP lists; some of their behavior was measured on
  Google, no more. Written, never run against Google: Cloud SQL companions and GKE. There is no public router image (you
  build and push yours with `scripts/gcp-image.sh` of the router), no dashboard or manager for Google Cloud yet, and the
  branded waiting page does not apply to it. The README says so in its Google Cloud section.
- `--with cloud-sql`, `--with gke`, `--cloud-sql` and `--gke-cluster` (`gcp check`, `install`, `uninstall`,
  `workloads onboard` and `workloads access show|grant|revoke`) print one warning on stderr, once per command, that Cloud SQL
  and GKE on Google Cloud have never run against Google and are to be tried in a project you can lose; the command goes on.
- The embedded copy of the router's `gcp/access.json` is the router 0.35.0's again.

## 0.7.0

- The hibernation of workloads' EC2 instances, set at its three levels (manager 0.19.0, routers 0.32.0): `dormeto
  settings show` and `settings set --hibernate true|false` for the manager's default, `routers settings <router>...
  --hibernate true|false|inherit`, `workloads settings (<router>/<workload>... | --tag k=v...) --hibernate
  true|false|inherit` and `workloads onboard --hibernate true|false`. Several targets are confirmed on a terminal
  (`--yes` without one, `--dry-run` lists them); a workload written by Terraform is refused with the manager's text
  (exit 1); `--json` prints the manager's answers.
- `routers show` prints the router's hibernate setting (its own, or `inherit (manager: on, read 2m ago)`, or the
  built-in default) and `workloads show` the value, where it comes from and which instances were launched with
  hibernation; nothing for a router before 0.32.0. A workload with no standalone EC2 instance says `does not apply`; a
  router that has not read the manager's settings yet says its naps stop; a fresh boot due is said too.
- The optional Markdown description of a workload or a router (manager 0.19.0): `workloads description
  <router>/<workload>` and `routers description <router>` print it as written, with who wrote it and when, or `no
  description` (read from the manager's store, so it works when the router is out of reach); `--set <text>`, `--file <path or ->` and `--clear` (at most one, else exit 2) write it; `--json`
  prints the manager's answer. `workloads show` and `routers show` print it, indented, under their header lines.
- Private EKS endpoints (manager 0.19.0, routers 0.33.0): `routers show` says for each cluster whether the router
  reaches it "directly" or "through its proxy <name> (<version>)" (nothing for a router before 0.33.0). The estimate
  lists a cluster with a private endpoint with the reason "…set up with the router of <account> <region>: install it",
  and one whose public endpoint is limited with private access off with the reason to turn private access on.

## 0.6.0

- Every operation of the manager from the command line, a verb per action of the dashboard, through the manager's
  `/iam/*` routes with the install's AWS profile (manager 0.15.0 or later): `dormeto whoami`, `overview`,
  `routers show|install|uninstall|pause|resume`, `workloads show|history|idle-window|wake|nap|pause|rename|tags`,
  `workloads onboard|offboard|exclude`, `discovery status|run|candidates|dismiss|restore|access`,
  `discovery config show|set`, `exclusions list|add|lift`, `operations list|show|watch`,
  `users list|invite|role|groups|disable|enable|resend|reset-mfa`, `groups list|show|create|update|delete`, `audit`,
  `savings show|discounts`, `alerts list`, `ip-lists list` and `manager status|check|changes|update`.
  `dormeto api <method> <path>` calls any other route, signed, and prints its answer (exit 1 on a status other than
  2xx). The README has a section per group.
- The long operations (onboard, offboard, the install, upgrade and uninstall of routers, exclusions and their lifting,
  the manager's update) are followed step by step until they end; `--no-wait` prints the operation's id and
  `dormeto operations watch <id>` instead, and `--timeout` (60m by default) and Ctrl-C stop following while the
  operation goes on in the manager. A wake and a discovery run are waited for with `--wait`. A conflict with another
  operation names it, `(operation <id>: dormeto operations watch <id>)`, in `routers upgrade` too.
- Writes on several workloads or routers are confirmed on a terminal, and the destructive ones always are
  (`routers uninstall`, `workloads offboard`, `workloads exclude`, `exclusions add`, `users disable`, `users role`,
  `groups delete`, `discovery config set`, `manager update`); the question of a workload write lists its targets;
  `--yes` without a terminal, `--dry-run` where a preview exists.
- Every command has `--json`, the manager's answer after the CLI's own filters on stdout alone (questions, dry-run
  lists and progress go to stderr), `keep-awake`, `resume` and `schedule set|clear` included. A list prints the API's
  object (`{users: […]}`), every page in it; the commands that start operations print an array when they take several
  targets and an object when they take one; a dry run prints `{"dryRun": true, "targets": […]}` and a declined
  confirmation `{"confirmed": false, "targets": […]}`; a command where nothing succeeded prints `[]` or `{}`.
- `--region` is always the manager's region: a router's is `--router-region` (`routers install`, as `routers list`), a
  candidate's `--candidate-region` (`discovery candidates`). A candidate is named by its id or its target group's name.
- `--for` takes whole days first (`3d`, `1d12h`), `workloads keep-awake --for` included (up to 7 days); its message for
  a bad value says so.
- The README's permission section gives three policies, read-only (`GET`), wake and nap, and every route, with the route
  each write calls. From manager 0.18.0, the manager refuses to demote or disable its last enabled administrator; an
  older one does not, and the CLI does not check it itself.

## 0.5.0

- `dormeto workloads keep-awake`, `resume` and `schedule show|set|clear`: keep workloads awake for up to 7 days
  (`--for 2h` or `--until 17:30`, with `--wait` until they are awake, for CI), end the pause, and set, show or clear
  their weekly awake and nap windows, one workload or every workload with a tag (`--tag`, `--yes`, `--dry-run`).
  `keep-awake` and `resume` work with any manager 0.15.0 or later (they use pauses); schedules need manager 0.16.0 and
  routers 0.31.0.

Also in 0.5.0 (prepared as 0.4.1, never published on its own):

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
