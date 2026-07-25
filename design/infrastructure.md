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

**Recommendation: drop Traefik for now.** Cloudflare Tunnel → Go binary. Add
Traefik when a second dynamic service exists and middleware is genuinely needed;
it slots in without disturbing anything else.

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
- DNS records for `bykami.com` and every subdomain
- `cloudflare_zero_trust_tunnel_cloudflared` — the tunnel and its ingress rules
- Cloudflare Pages projects for the three static sites
- WAF / rate-limiting rules

**VPS** — depends on the provider having a Terraform provider. Many Indonesian
hosts do not. If none exists, the instance is provisioned manually and Terraform
manages Cloudflare only; record that explicitly rather than pretending otherwise.

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
- Ansible secrets → `ansible-vault`, or injected as env from Actions
- Terraform state → encrypted remote backend, restricted access
- Never bake config with credentials into any shipped artifact

## Open

- Which VPS provider? Determines whether Terraform can manage the instance or
  only Cloudflare.
- Keep or drop Traefik — see the recommendation above.
- Where does Terraform state live? (Cloudflare R2 with the S3 backend is free and
  avoids adding a vendor.)

## Note for implementation

Ansible module arguments and the Cloudflare Terraform provider's schema change
between major versions, and getting them wrong produces a plan that looks correct
and fails on apply. Look up version-specific syntax via `context7` rather than
recalling it. For Terraform authoring specifically, load the `terraform-skill`.
