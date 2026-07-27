# Infrastructure

Target: **Alibaba Cloud ECS, 2 vCPU / 2 GiB**, Cloudflare in front, Terraform for
resources, Ansible for configuration, GitHub Actions for CI/CD.

Provisioned 2026-07-26 on the free trial: instance `i-t4n7sxn0wwzevsimxwnr`,
`ecs.e-c1m1.large`, Singapore zone A (`ap-southeast-1`), pay-as-you-go, trial
credit through 2026-10-26.

Two things about it were not fit for the design, and both were fixed on
2026-07-26 before anything else was configured:

- **It ran CentOS 7.9**, end-of-life since 2024-06-30 — no security updates,
  repositories moved to vault, and every Ansible role below written against
  `apt`. Replaced with Ubuntu 24.04 via the console's Replace Operating System
  flow, which calls `ReplaceSystemDisk`. That wipes the system disk but keeps
  the instance ID, so the trial's savings-plan binding survived.
- **It had no key pair and no public IP.** Login was a console password reset,
  which Ansible cannot use; and with no public IP the box had no outbound path
  to install anything. Both are start-time settings, so they were fixed together
  during the re-image.

The re-image also grew the system disk from 20 GB to 100 GB — worth knowing
because system disks can be grown but never shrunk, so this is fixed for the
life of the instance and draws against the trial credit continuously whether or
not the box is doing anything. Nothing here needs 100 GB; check Billing → Cost
Analysis to see what it actually costs before assuming the credit stretches to
2026-10-26. Size the disk deliberately on the rebuild.

Its public IP is auto-assigned and pay-by-traffic, which means it is **released
whenever the instance stops**. Nothing may be pinned to it — it is a bootstrap
address, not an endpoint.

Static sites live on Cloudflare Pages. Only dynamic services run on the VPS.

## The memory budget no longer decides the architecture

**This section was written against a 1 vCPU / 1 GB target and derived everything
from it. That premise is gone** — the box is 2 GiB. The conclusions below all
survive, but several now rest on different grounds, and the difference matters:
an argument that has quietly stopped applying will be reopened by the first
person who checks the arithmetic.

| Component | Resident | Verdict | Still because |
|---|---|---|---|
| OS (Debian minimal) | ~180 MB | Fixed | — |
| Docker daemon | ~100 MB | **Avoid** | One static binary needs no image versioning. The RAM argument is halved, not gone — 5% of 2 GiB against 10% of 1 GB |
| Traefik | ~70 MB | **Dropped** | Still one destination |
| cloudflared | ~30 MB | Required | — |
| Go service (per process) | ~30–50 MB | Consolidate | No independent scaling to win on 2 vCPU |
| SQLite | in-process | Free | Operational simplicity, no longer memory contention |

Doubling the budget loosened it; it did not remove it. 2 GiB is still small
enough that the decisions below would be made the same way — which is the useful
result, because it means none of them were only ever about the RAM.

### 1. No Docker

Docker's daemon costs ~100 MB — once 10% of the box, now 5% — and its main
value here would be Traefik's label-based discovery. Static Go binaries under
systemd cost nothing and deploy just as easily from CI. The cost argument has
weakened; the "nothing to gain" argument has not.

Per infra conventions: reach for Docker when there are several services *and* the
deploy story needs image versioning. Tagging binaries by commit SHA gives the same
rollback guarantee without the daemon.

### 2. One Go binary, not three

Identity, loyalty, and booking are **modules, not services**. On 1 vCPU, splitting
them means three Go runtimes, three GC heaps, and inter-process calls across
localhost for zero benefit — there is no independent scaling to gain on a single
core.

Build a modular monolith with clean internal package boundaries. Splitting later
is a refactor, not a rewrite, and only worth doing when a specific module actually
needs separate scaling.

### 3. Traefik is likely unnecessary here

With static sites on Cloudflare Pages and one Go binary on the VPS, the routing
job is: *send everything to one process.* Cloudflare Tunnel already maps hostnames
to local ports in its own ingress config.

Traefik would add ~70 MB — 7% of total RAM — to route traffic that has one
destination. It earns its place with many services, middleware chains, or
label-based discovery. None of those apply yet.

**Decided: dropped.** Cloudflare Tunnel → Go binary. Add Traefik when a second
dynamic service exists and middleware is genuinely needed; it slots in without
disturbing anything else.

Moot for phase 1 regardless — that phase is static only, and nothing runs on the
VPS at all.

### Resulting budget

| Component | Resident |
|---|---|
| OS | ~180 MB |
| cloudflared | ~30 MB |
| Go binary (modular monolith) | ~60 MB |
| **Total** | **~270 MB** |

Against 2 GiB that is ~13% utilised, leaving ~1.7 GiB. Comfortable, not
lavish — enough that a deploy or a traffic spike is uneventful, not so much that
the budget can be ignored.

## Mandatory

- **Swap** — 2 GB swapfile, `vm.swappiness=10`. Cheap insurance during a deploy;
  no longer the difference between running and OOM.
- **`GOMEMLIMIT`** ≈ **1.4 GiB**, revised from 750 MiB. Sized to leave the OS and
  cloudflared their ~210 MB with room to spare, rather than to the box total: a
  limit set at capacity is not a limit, because the OOM killer arrives before the
  GC does.
- **Never build on the VPS.** Still true — a toolchain on a production host is a
  liability regardless of how fast it compiles. Build in Actions, ship the binary.
- **SQLite, not Postgres.** The memory argument is gone; the operational one
  stands. One file, one backup, no second daemon, no connection pool to tune, and
  the ledger's guarantees are already enforced in SQLite schema.

## Terraform — things that exist

Owns resources with an API and a lifecycle. Never shells out to Ansible.

**Cloudflare** (provider `cloudflare`, API token scoped to the one zone):
- Zone settings — TLS mode, always-use-HTTPS, minimum TLS version
- DNS records for `bykami.id` and every subdomain
- `cloudflare_zero_trust_tunnel_cloudflared` — the tunnel and its ingress rules
- Cloudflare Pages projects for the four static sites
- WAF / rate-limiting rules

**VPS — Alibaba Cloud ECS, Singapore (`ap-southeast-1`).** Provider
`aliyun/alicloud`, config in `infra/alicloud/`.

**The trial instance is deliberately *not* a Terraform resource**, which reverses
the earlier plan to import it. The trial is attached to this specific instance ID
through a savings plan, and losing the ID loses the trial silently — the apply
succeeds, the box comes back, and billing has quietly become full-rate
pay-as-you-go.

An earlier version of this section blamed ForceNew on `image_id` and
`instance_type`. That was wrong, and it matters because it argued against a
re-image that turned out to be safe: both are modifiable in place, and changing
`image_id` calls `ReplaceSystemDisk`, which wipes the system disk but keeps the
instance ID. The fields that genuinely force replacement are
`system_disk_category`, `availability_zone`, `data_disks` and `spot_strategy`.

The real blocker is state, not ForceNew. CI has no remote backend, so a
stateless `terraform plan` against a managed `alicloud_instance` would propose
creating a second one on every run. Making the instance a resource means moving
state to R2 first, and adding `lifecycle { prevent_destroy = true }` so that a
plan which *would* replace it errors instead.

So Terraform owns only what is additive — the SSH key pair and the security group
rules — and the instance, VPC, vSwitch, and security group stay console-owned
until the trial ends. A commented `alicloud_instance` in `main.tf` is what
replaces this arrangement when the box is bought for real.

This does not weaken the rule it appears to break. **Promotional pricing is a
console purchase flow**: creating the instance from Terraform gets standard
rates, so it is bought by hand either way. The only change is that a
console-bought instance under a *discount tied to its ID* should be referenced,
not imported.

Avoid mainland-China regions: a domain serving traffic from them needs ICP
filing, which takes weeks and gates launch.

**Credentials — CI holds none.** Not a RAM user with an AccessKey, which was the
earlier plan: **OIDC**. GitHub mints a token per run, RAM trades it for STS
credentials that expire in 15 minutes, and the role's trust policy pins who may
make that trade to this repository on named refs. The policies are in
`infra/alicloud/ram/`, applied by hand because Terraform cannot authenticate
until they exist.

This is worth the extra setup specifically *because* the repo is public. A
long-lived AccessKey is a standing secret that has to be rotated, guarded in
logs, and revoked if a workflow is ever tricked into printing it. A 15-minute
STS credential scoped to one key pair and one security group is close to
worthless to anyone who captures it — and there is nothing left to rotate.

The root account's own AccessKey should not exist at all; MFA on it, and
everything else through RAM.

**Preflight is now just `terraform plan`.** The previous provider had a
purpose-built credential checker because nothing else exercised the API before
provisioning. That is no longer true: both stack inputs default to empty, so the
`Alibaba Cloud` workflow plans with no secret beyond the API credentials, and a
clean plan means the key works and the instance is where the config says it is.
A second checker would only be another thing to keep correct.

The lesson that checker taught still holds, now carried by error codes rather
than by a script: "the key is wrong" and "the key lacks permission" are different
problems that a half-finished `apply` reports identically. Alibaba distinguishes
them — `InvalidAccessKeyId.NotFound` and `SignatureDoesNotMatch` against
`Forbidden.RAM` — so read the code before re-issuing a key that is already fine.

## The trial box

| | |
|---|---|
| Instance | `i-t4n7sxn0wwzevsimxwnr` (`ecs.e-c1m1.large`) |
| Region | Singapore zone A, `ap-southeast-1` |
| Size | 2 vCPU / 2 GiB, 100 GB system disk (`/dev/vda3`, 99 GB usable) |
| Network | `vpc-t4nhh131nrk5jf0zbb90y` / `vsw-t4nwwj3yx0q786ie56g1y`, sg `sg-t4n7sxn0wwzevsiowbta` |
| Public IP | auto-assigned at start, released at stop — 5 Mbps pay-by-traffic, $0.081/GB |
| Image | Ubuntu 24.04.4 LTS, kernel 6.8.0-124-generic (re-imaged 2026-07-26) |
| Billing | Pay-as-you-go, $90 trial credit through 2026-10-26 |
| State | Running; auto-release set for 2026-10-24 00:00:00 |

Both of the questions this table originally raised are now answered, and the
answers went the way that needs action rather than the way that needed none:

- **The trial does not stop at expiry.** Pay-as-you-go plus exhausted credit
  keeps running and starts charging. See "The trial ends 2026-10-26" below.
- **The public IP is transient, not permanent.** It was released when the
  instance stopped, so the tunnel model is not violated — but it also means the
  box currently has no outbound path, and bootstrapping needs one. Assign a
  public IP at start, restrict port 22 to an admin address via
  `ssh_admin_cidr`, enforce key-only auth (`PasswordAuthentication no`), get
  `cloudflared` dialling out, then clear the rule.

### Bootstrap state as of 2026-07-26

Done: re-imaged to Ubuntu 24.04, hostname `bykami-app`, timezone `Asia/Jakarta`
(the image ships UTC+8, which silently puts every log line and cron schedule an
hour ahead of the business), key-only auth enforced via
`/etc/ssh/sshd_config.d/00-hardening.conf`, and `sshd -T` confirms
`passwordauthentication no` and `permitrootlogin without-password`.

The `base` role has been applied and a second run reports `changed=0`, so the
box's configuration is now described by `ansible/` rather than by whatever was
typed into it. That run added the 2 GB swapfile the memory budget calls for —
absent until then — set `vm.swappiness=10`, and created the `deploy` user. The
sshd drop-in it wrote differs from the hand-applied one by comments only, which
is the useful part: adopting the role changed no behaviour.

`deploy` has no authorized keys yet, so it cannot log in. That is deliberate —
it wants a key of its own rather than a copy of root's, and
`base_deploy_user_authorized_keys` stays empty until one is minted. Until then
root remains the only way in, which is why `base_permit_root_login` is still
`prohibit-password` rather than `no`.

**Leave `cloud-init` on hold.** The image ships Alibaba's own build, `23.2.2-8`,
held along with `intel-microcode`, while the archive offers `26.1`. The hold is
not staleness to tidy up: the package owns
`/etc/cloud/cloud.cfg.d/aliyun_cloud.cfg`, which pins `datasource_list:
[ AliYun ]`. Upgrading to the Ubuntu build removes that conffile as obsolete,
and a box that cannot identify its datasource may come back from a reboot with
no network configuration — recoverable only through the VNC console. Upstream
cloud-init does support `AliYun` and would probably re-detect it, but "probably"
is doing load-bearing work in that sentence. `unattended-upgrades` is enabled
and its allowed origins include plain `noble` and `noble-security`, both of
which carry a newer `cloud-init`; the hold is the only thing standing between
those two facts.

**A deploy private key was disclosed in full and is burned.** It was rotated,
but rotation only *added* the new key — the burned one stayed authorized for
root until it was removed on 2026-07-26. `authorized_keys` now holds exactly one
key, `SHA256:YWCg0VdnM9eoXW6RrfxFKpvyJK/zDFJkb2XXa6+nfgQ`, verified by logging
in with it and confirming every other local key is refused. The previous file is
kept as a `.bak` beside it.

Both follow-ups are now closed, audited against the API rather than assumed:

- **The ECS key pair holds the good key, not the burned one.** One pair exists
  in `ap-southeast-1`, `bykami-deploy`, MD5 `42:04:3f:56:52:2e:4e:f3:c8:3d:ac:
  04:a1:cb:b5:a8` — identical to the surviving local key. Re-attaching it
  cannot resurrect the disclosed key, which was the thing worth checking.
- **The security group carries exactly one inbound rule**, TCP 22/22 from a
  single `/32`. No `0.0.0.0/0`.

That rule is **not** Terraform-managed, despite carrying the description text
from `alicloud_security_group_rule.ssh_bootstrap`. There is no state, and
`ssh_admin_cidr` is empty, so the resource's `count` is 0. Two consequences
worth knowing before either is discovered the hard way: clearing
`ssh_admin_cidr` will never remove this rule, and setting it to the same
address would try to create a rule that already exists rather than adopt it.
Removing the rule is a console action, or an import first.

It is also pinned to a residential address, so it goes stale whenever that IP
changes — one more reason the tunnel is the destination rather than a nicety.

Rotating a key is two steps, and the second one is the whole point. Adding the
new key restores access, which makes it feel finished; removing the old one is
what ends the exposure, and nothing fails if you skip it.

## Residency is an object-storage problem, not a region problem

**Gate: no live customer session until residency is settled.** Singapore is
where the trial box happened to be created. That is a placement, not a decision.

The reasoning that pointed at Jakarta was face photos and minors' data under
UU 27/2022. But the photos do not live on the VPS — `kiosk.md` puts them in R2,
and **R2 cannot be pinned to Indonesia**: its jurisdictional restrictions are EU
and FedRAMP only, and `apac` is a location *hint*, not a guarantee. A Jakarta VPS
serving photos out of R2 buys the feeling of having decided and very little else.

So the fork is about where the *objects* sit, and it should be taken once, with
legal input:

| | Metadata | Photos | Trade |
|---|---|---|---|
| Offshore | Singapore ECS | R2 | Cheapest — R2 egress is free |
| In-country | Jakarta ECS (`ap-southeast-5`) | Alibaba OSS Jakarta | Real residency; loses free egress on the product's dominant byte-flow |

Alibaba offers Jakarta (`ap-southeast-5`) at ordinary ECS pricing, so in-country
does not carry the price penalty that killed it under the previous provider.
Price it before deciding.

Until this is settled the trial box carries **synthetic sessions only**.

## The trial ends 2026-10-26, and nothing stops on its own

The trial is **USD 90 of credit against a pay-as-you-go instance**, not a free
plan that expires closed. Credit runs out or the window shuts, the instance keeps
running, and the charges land on the payment method. Latency to notice is the
danger: at ~$30/month of headroom this is a small bill that arrives quietly.

Three controls, worth understanding as a set because they do different jobs:

| Control | Where | What it actually does |
|---|---|---|
| **Auto-release time** | ECS instance page, next to Billing Method | The only one that *stops* spending. Set it and the instance is deleted at that timestamp. Pay-as-you-go only. |
| **Budget alert** | Billing → Budgets | Emails at a threshold. Never stops anything. |
| **Stop in Economical Mode** | Instance page | Already on. vCPU and memory stop billing; **disks, EIP, and bandwidth do not.** |

Auto-release **destroys the instance and its disk**. That is the price of it
being the only real stop, and it is the right trade while the box holds nothing —
it stops being right the moment there is a booking in the database. Snapshot
first, then re-evaluate; a control that quietly deletes production is worse than
the bill it prevents.

Auto-release and **Release Protection** pull in opposite directions — one exists
to delete the instance on a schedule, the other to prevent deletion. Pick one
deliberately rather than discovering the interaction at the deadline.

Two traps specific to how this trial was sold:

- **"Switch to Subscription"** sits next to the billing method on the instance
  page. Subscription is cheaper for a box that runs continuously and is the right
  end state — but subscriptions renew, and auto-renew is the default. Switch when
  the decision is made, not to make the trial cheaper.
- It is a **savings-plan-backed trial**, so check Billing → Savings Plans for a
  plan with its own auto-renew setting. That is a second renewal switch in a
  different part of the console from the first.

### Two rules that outlived the previous provider

**Spot is not an option here at any discount**, and the temptation is real
because it is a fraction the price of anything else. The vendor warnings name the
disqualifying case exactly: *do not use for database and single point services
that cannot be interrupted*. SQLite sits on the instance's local disk and there
is no second node, so a repossession is not downtime — it is the booking history
and the loyalty ledger gone. The ledger's whole point is that a dispute stays
resolvable months later.

**The pricing API is no substitute for the purchase page.** It quotes list rates
for pay-as-you-go only and knows nothing of subscription, spot, or promotion, so
it cannot answer "what will this cost". This was learned the expensive way once
already; it is not an Alibaba-specific caveat.

Also note that continuous running inverts the usual advice: pay-as-you-go costs
*more* than subscribing. Pay-as-you-go earns its place only while the box is
disposable, which for the next three months it is.

**State** — remote backend with encryption. Terraform state contains secrets in
plaintext and must never sit in the repo, which is public.

Done for `infra/cloudflare` on 2026-07-27: R2 bucket `bykami-tfstate`, key
`cloudflare.tfstate`, via the S3-compatible backend. The trigger was the tunnel
landing — before it, state held four Pages projects and losing it meant
re-importing them; after it, state holds the connector token, a bearer
credential for every hostname routed through the tunnel.

Two things about that migration are easy to get wrong later:

- **The backend reads `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`**, not the
  `R2_*` names the Ansible backup role uses. A mismatch reports "no valid
  credential sources", which sends you to check the token rather than the
  variable name.
- **`terraform init` needs `-backend-config` for the endpoint.** It cannot be
  hardcoded because it contains the account ID and this repo is public, and
  backend blocks cannot interpolate variables. See `infra/cloudflare/README.md`.

`infra/alicloud` deliberately did **not** migrate. Its resources are all behind
a `count` of 0 and the instance is a data source, so there is no state to
protect — and a backend would break the CI plan job, which runs a plain
`terraform init` with no R2 credentials. It gains one at the same moment it
gains its first real resource.

## Ansible — what is inside the box

Owns packages, users, systemd units, config files, the deployed app. Never
creates cloud resources Terraform manages.

Roles, in `ansible/roles/` — see `ansible/README.md` for the run order and the
non-obvious parts:
1. `base` — swap, `vm.swappiness`, unattended upgrades, SSH hardening, deploy user
2. `cloudflared` — install, service user, systemd unit, connector token
3. `app` — binary drop, systemd unit with `GOMEMLIMIT`, SQLite data dir, backups

**`cloudflared` owns no ingress config**, which corrects this list's earlier
claim that it did. The connector runs in remotely-managed mode: the tunnel and
its routes are Terraform resources, and the box holds only a token. Ingress
cannot be owned in two places — a `config.yml` on the box plus rules in
Terraform is two sources of truth, and the failure is a route that works until
the next playbook run restores a stale copy. Adding a hostname is a Terraform
change, not an Ansible run.

Rules:
- Real modules (`ansible.builtin.systemd`, `apt`, `template`, `user`) — never bare
  `shell`. Where `shell` is unavoidable it carries `creates:`, `removes:`, or
  `changed_when:`; an unguarded `shell` reports changed every run and hides drift.
- Handlers restart services; tasks do not.
- Pin roles and collections in `requirements.yml` with explicit versions.

Verification — must be clean:
```bash
ansible-lint playbooks/
ansible-playbook -i inventory site.yml --check --diff
ansible-playbook -i inventory site.yml        # re-run: expect ok=N changed=0
```

## GitHub Actions

- **Build once, deploy that artifact.** Tag by commit SHA, never `latest`, so
  rollback is redeploying a known SHA rather than rebuilding.
- Path-filtered workflows:
  - `sites/**` → build Astro, deploy to Cloudflare Pages via `wrangler`
  - `api/**` → `go build`, ship binary over SSH, `systemctl restart`
  - `agent/**` **or** `apps/kiosk/**` → build the kiosk bundle *first*, then
    cross-compile the agent that embeds it. A UI change is an agent change; a
    binary built before the bundle ships yesterday's UI
  - `infra/**` → `terraform plan` on PR; apply gated on manual approval
- Deploy over SSH with a dedicated key, command-restricted where possible
- Dependabot grouped by ecosystem (npm, gomod, github-actions) or PR volume
  buries real work

## Secrets

The repo is **public**. Nothing sensitive in the tree, ever.

- Cloudflare API token, tunnel credentials, Xendit keys, SSH deploy key → GitHub
  Actions secrets, injected at runtime
- Alibaba Cloud → **no stored credential.** OIDC, per above. The only Actions
  secrets are `ALIBABA_OIDC_PROVIDER_ARN` and `ALIBABA_ROLE_ARN`, which name a
  role rather than granting it; they are secrets only to keep the account ID out
  of a public repo's logs. Note that `ALICLOUD_ACCESS_KEY` / `ALICLOUD_SECRET_KEY`
  are deprecated provider env vars as of v1.228.0 and are **silently ignored** —
  the current names are `ALIBABA_CLOUD_ACCESS_KEY_ID` and
  `ALIBABA_CLOUD_ACCESS_KEY_SECRET`. A stale name presents as "no credentials
  found", not as a warning, so it reads like a permissions problem.
- R2 S3 credentials for the photo bucket → Actions secret. The agent never holds
  them; it uploads through `api/`, which mints signed URLs.
- Ansible secrets → `ansible-vault`, or injected as env from Actions
- Terraform state → encrypted remote backend, restricted access
- Never bake config with credentials into any shipped artifact

### Workflow rules, because the repo is public

Anyone can open a pull request here, and a pull request is a proposal to run code
on a runner that may hold secrets. Five rules follow, and they are rules rather
than preferences:

- **Pin every third-party action to a commit SHA**, with the version in a
  trailing comment. A tag is a pointer its owner can repoint; that is exactly how
  the `tj-actions/changed-files` compromise reached thousands of repositories in
  a single afternoon. Enforced at the repository level — an unpinned action now
  fails rather than running.
- **Grant permissions per job, not per workflow.** `permissions: {}` at the top,
  and a job that needs `id-token: write` says so alone. The job that builds
  pull-request code should never be the job that can mint a cloud token.
- **The cloud role grants what the workflow does today, not what it might do
  later.** `alicloud.yml` only plans, so the RAM role is read-only; write
  permissions arrive in the same commit as the apply job that needs them, scoped
  to `environment:production`. Granting ahead of use is how a permission ends up
  outliving its reason — and an authority nothing exercises is the one whose
  misuse nobody notices.
- **Never `pull_request_target`.** It runs the base branch's workflow with full
  secrets against the fork's code, and that combination is the standard way
  public repositories leak credentials.
- **Untrusted input reaches a shell only through `env:`, never through `${{ }}`
  interpolation.** Branch names, PR titles, and commit messages are all attacker
  controlled. `deploy.yml` additionally strips the head ref to a character class
  before it reaches `wrangler`.

## Division of labour

Cloudflare and the compute provider split cleanly, and the split is deliberate:

- **Cloudflare** owns DNS, the edge, and everything static. It already has a
  Jakarta PoP, so moving static hosting to the VPS would not put a single byte
  closer to Banyuwangi — it would only add cost and a second deploy path.
- **Alibaba Cloud** owns compute. Cloudflare additionally owns **R2**, where the
  photo objects live — never the VPS disk, because serving galleries is the
  product's dominant egress and R2 has no egress fee.

The VPS should keep **no inbound rules** — not no public IP. The distinction was
stated wrongly here before, and getting it backwards costs an afternoon: an ECS
instance in a VPC with no public IP and no NAT gateway has no internet egress at
all, so `cloudflared` cannot dial out and the tunnel never comes up. It presents
as a broken tunnel, not as a missing address.

So the end state is a public IP with an empty inbound rule set. A public IP is an
egress path and a *potential* ingress path; the security group decides whether it
is an actual one. Port 22 is opened only to bootstrap `cloudflared`, scoped by
`ssh_admin_cidr` in `infra/alicloud/`, and cleared once the tunnel is up. After
that nothing depends on the provider's firewall rules staying correct, which was
the point of the tunnel in the first place.

## Managed robots.txt contradicts ours

Verified against the live zone on 2026-07-26. The earlier assumption — that
Cloudflare blocks AI crawlers at the edge before they reach origin — is **wrong
for this zone**: ClaudeBot, GPTBot, and CCBot all fetch pages successfully.

What actually happens is narrower and easier to miss. Cloudflare detects the
origin's `robots.txt` and *merges* a managed block ahead of it:

```
# BEGIN Cloudflare Managed content
User-agent: *
Content-Signal: search=yes,ai-train=no,use=reference
User-agent: ClaudeBot
Disallow: /
...
# END Cloudflare Managed Content
User-agent: ClaudeBot
Allow: /            ← ours, and it arrives second
```

Every crawler the catalogue exists to reach is told `Disallow: /` before it
reads our `Allow: /`, and `ai-train=no` is asserted as a reservation of rights
under EU DSM Article 4. That is the opposite of the intent.

**Owner action, dashboard only** — the API rejects zone-settings writes for this
token (`9109 Unauthorized`), the same wall that blocked zone creation:

1. Security → Settings → filter *Bot traffic* → turn **off**
   "Set your preference to block training in robots.txt"
2. Zone Overview → Control AI Crawlers → uncheck **Display Content Signals Policy**

Re-verify with `curl https://studio.bykami.id/robots.txt` and confirm no
`Cloudflare Managed content` block. Once a token with Zone:Edit exists this
becomes a Terraform-managed zone setting rather than a manual step.

## Open

- Where does Terraform state live? (Cloudflare R2 with the S3 backend is free and
  avoids adding a vendor.)

## Note for implementation

Ansible module arguments and the Cloudflare Terraform provider's schema change
between major versions, and getting them wrong produces a plan that looks correct
and fails on apply. Look up version-specific syntax via `context7` rather than
recalling it. For Terraform authoring specifically, load the `terraform-skill`.
