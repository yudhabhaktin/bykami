# Infrastructure

Target: **1 vCPU / 1 GB VPS** in Indonesia, Cloudflare in front, Terraform for
resources, Ansible for configuration, GitHub Actions for CI/CD.

Static sites live on Cloudflare Pages. Only dynamic services run on the VPS.

## The memory budget decides the architecture

1 GB is the binding constraint. Everything below follows from it.

| Component | Resident | Verdict |
|---|---|---|
| OS (Debian minimal) | ~180 MB | Fixed |
| Docker daemon | ~100 MB | **Avoid** |
| Traefik | ~70 MB | **Probably unnecessary — see below** |
| cloudflared | ~30 MB | Required |
| Go service (per process) | ~30–50 MB | Consolidate |
| SQLite | in-process | Free |

Naive stack — Docker + Traefik + cloudflared + three Go services — lands around
**520 MB before serving a single request**, leaving no headroom for traffic
spikes or a deploy. Two decisions reclaim most of it.

### 1. No Docker

Docker's daemon costs ~100 MB, roughly 10% of the box, and its main value here
would be Traefik's label-based discovery. Static Go binaries under systemd cost
nothing and deploy just as easily from CI.

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

Leaves ~700 MB of headroom on a 1 GB box. Comfortable.

## Mandatory on 1 GB

- **Swap before anything else** — 2 GB swapfile, `vm.swappiness=10`. Without it a
  deploy or a traffic spike OOM-kills the box.
- **`GOMEMLIMIT`** ≈ 750 MiB. Go's GC assumes it can grow into memory that does
  not exist and will be OOM-killed rather than collecting.
- **Never build on the VPS.** `go build` on 1 vCPU is slow and memory-hungry, and
  puts a toolchain on a production host for no reason. Build in Actions, ship the
  binary.
- **SQLite, not Postgres.** On 1 GB the database and the app would fight.

## Terraform — things that exist

Owns resources with an API and a lifecycle. Never shells out to Ansible.

**Cloudflare** (provider `cloudflare`, API token scoped to the one zone):
- Zone settings — TLS mode, always-use-HTTPS, minimum TLS version
- DNS records for `bykami.id` and every subdomain
- `cloudflare_zero_trust_tunnel_cloudflared` — the tunnel and its ingress rules
- Cloudflare Pages projects for the four static sites
- WAF / rate-limiting rules

**VPS — Tencent Cloud, Jakarta region (`ap-jakarta`).** Official Terraform
provider `tencentcloudstack/tencentcloud`, so the instance is managed rather than
hand-built — the fallback of "provision manually, let Terraform own Cloudflare
only" is not needed.

Jakarta puts the backend in-country, which matters for a database-backed booking
flow in a way it never did for static pages. Avoid mainland-China regions: a
domain serving traffic from them needs ICP filing, which takes weeks and gates
launch.

**Preflight before provisioning** — `infra/tencent/preflight.sh`, run by the
`Tencent preflight` workflow. It checks `sts:GetCallerIdentity` and
`cvm:DescribeRegions` separately, because "the key is wrong" and "the key lacks
CAM permission" are different problems that a half-finished `terraform apply`
reports identically. It signs offline against Tencent's published test vector
first, so a green self-test means a red API result is genuinely the account
rather than the script.

**New-user promotional pricing is a console purchase flow.** If a promo is used,
the instance is bought by hand and Terraform *imports* it, exactly as the
Cloudflare zone was. Creating it from Terraform gets standard rates. That is a
pricing constraint dictating the workflow, not a preference.

## Jakarta costs more than the latency is worth — open

Account is Tencent Cloud **International** (`console.tencentcloud.com`).
Verified 2026-07-26:

| | Lighthouse | CVM |
|---|---|---|
| `ap-jakarta` | **not offered** — HK, Singapore, Tokyo, Silicon Valley, Toronto, Frankfurt, Mumbai only | yes, 3 AZs (zone 3 restricted) |
| Cheapest | from $1.68/mo new-user, list $4.20/mo for 2 vCPU / 2 GB / 40 GB, bandwidth included | **S5.MEDIUM2**, 2 vCPU / 2 GB, $0.03/hr ≈ **$21.90/mo**, bandwidth billed separately |
| Free tier | 2C2G, 3 months | 2C4G, 3 months |

CVM prices queried from `DescribeZoneInstanceConfigInfos` for `ap-jakarta-1`,
2026-07-26. Next cheapest are SA5.MEDIUM2 at $36.50/mo and S5.MEDIUM4 at
$43.80/mo, so S5 is the only Jakarta option in the intended price range at all.

Jakarta therefore costs roughly **5–13x Lighthouse Singapore before traffic**,
which is a far wider gap than the original latency-versus-cost framing assumed.

So the cheap product and the in-country region are mutually exclusive, which the
original "cheapest VPS in Indonesia" assumption did not anticipate.

Singapore instead of Jakarta costs roughly 30 ms of extra round trip from
Banyuwangi. That was the whole stated case for Jakarta, and it is weaker than it
looks: the pages users actually wait on are static and already served from
Cloudflare's Jakarta PoP, so the delta applies only to booking and OTP calls —
a handful of requests, none of them perceptibly slower.

The reason that might still favour Jakarta is **data residency, not speed**.
The database holds Indonesian customers' phone numbers and booking history, and
Indonesia's PDP law reaches that. Offshore storage by a private operator is
generally permitted, but it carries obligations that in-country storage does
not. Decide this deliberately, and not on latency grounds.

**Decided: claim the CVM free tier, not the Lighthouse one.** Eligibility is per
*product*, so spending the CVM trial leaves the Lighthouse trial intact — and
the two are not interchangeable rehearsals. CVM exercises the VPC, the security
group and the `tencentcloud_instance` resource that the design assumes;
Lighthouse has no VPC and a different resource, so none of that work would carry
over. Trialling the expensive product and keeping the cheap one in reserve also
happens to be the order that preserves both options.

The three-month clock is the risk: $0 to $21.90/mo plus traffic is a cliff, not
a slope. Decide Jakarta versus Singapore before month three, with real traffic
data rather than the estimate above.

**State** — remote backend with encryption. Terraform state contains secrets in
plaintext and must never sit in the repo, which is public.

## Ansible — what is inside the box

Owns packages, users, systemd units, config files, the deployed app. Never
creates cloud resources Terraform manages.

Roles:
1. `base` — swap, `vm.swappiness`, unattended upgrades, SSH hardening, deploy user
2. `cloudflared` — install, credentials file, systemd unit, ingress config
3. `app` — binary drop, systemd unit with `GOMEMLIMIT`, SQLite data dir, backups

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
  - `infra/**` → `terraform plan` on PR; apply gated on manual approval
- Deploy over SSH with a dedicated key, command-restricted where possible
- Dependabot grouped by ecosystem (npm, gomod, github-actions) or PR volume
  buries real work

## Secrets

The repo is **public**. Nothing sensitive in the tree, ever.

- Cloudflare API token, tunnel credentials, Xendit keys, SSH deploy key → GitHub
  Actions secrets, injected at runtime
- `TENCENTCLOUD_SECRET_ID` / `TENCENTCLOUD_SECRET_KEY` → same, named for the
  Terraform provider's own variables so nothing is renamed later. Issue them to a
  dedicated CAM sub-user, never the root account: root keys carry billing and
  account closure, and this repo is public enough that the blast radius of a
  mistake should be one VPS.
- Ansible secrets → `ansible-vault`, or injected as env from Actions
- Terraform state → encrypted remote backend, restricted access
- Never bake config with credentials into any shipped artifact

## Division of labour

Cloudflare and Tencent Cloud split cleanly, and the split is deliberate:

- **Cloudflare** owns DNS, the edge, and everything static. It already has a
  Jakarta PoP, so moving static hosting to Tencent would not put a single byte
  closer to Banyuwangi — it would only add cost and a second deploy path.
- **Tencent Cloud** owns compute. That is where in-country origin actually pays,
  because a booking request hits SQLite rather than a cached file.

The VPS keeps no public IP: Cloudflare Tunnel dials out, so nothing about this
depends on Tencent's firewall rules staying correct.

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
