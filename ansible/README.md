# Ansible — what is inside the box

Terraform makes the box exist. This makes it usable. Nothing here creates a
cloud resource, and nothing in `infra/` shells out to a playbook.

```bash
ansible-galaxy collection install -r requirements.yml

export BYKAMI_VPS_HOST=<current public IP from the ECS console>

ansible-lint
ansible-playbook site.yml --tags base --check --diff
ansible-playbook site.yml --tags base
ansible-playbook site.yml --tags base            # re-run: expect changed=0
```

That last line is the real test. A playbook that reports changes on a second
identical run is not describing the box, it is editing it — and once that is
true you can no longer use a clean run to prove nothing has drifted.

## The host address is not in this repo

`inventory.yml` reads `BYKAMI_VPS_HOST` from the environment. The trial box's
public IP is pay-by-traffic and auto-assigned, so Alibaba releases it on stop
and hands back a different one on start; an address committed today is wrong
after the next stop. Unset, the connection fails with `Could not resolve
hostname BYKAMI_VPS_HOST-is-not-set`, which is the error naming its own fix.

## `base`

Swap, `vm.swappiness`, unattended upgrades, SSH hardening, deploy user.

Two things in it are less obvious than they look.

**`cloud-init` is held, deliberately.** The image ships Alibaba's build, which
owns the conffile pinning `datasource_list: [ AliYun ]`. Upgrading to the Ubuntu
archive build drops that file as an obsolete conffile, and a box that cannot
identify its datasource can come back from a reboot with no network config —
recoverable only through the VNC console. `unattended-upgrades` is enabled and
its allowed origins include plain `noble` and `noble-security`, both carrying a
newer `cloud-init`, so the hold is the only thing between those two facts. The
role asserts the hold rather than trusting the image to keep it.

**`authorized_key` runs with `exclusive: true`.** Rotating a key by adding the
new one leaves the old one authorized. That is not hypothetical here: a
disclosed key kept root on this box after it had supposedly been rotated,
because rotation only ever added. Declaring the full set means deleting a key
from `base_deploy_user_authorized_keys` deletes it from the box.

`base_permit_root_login` stays at `prohibit-password` until the deploy user's
key is proven. Setting it to `no` first locks the box, and the only way back in
is the VNC console.

## `cloudflared`

Install, service user, systemd unit, token. **No ingress config** — and that is
a deliberate departure from the one-line summary in `design/infrastructure.md`,
which lists ingress here.

The connector runs in remotely-managed mode: Terraform owns
`cloudflare_zero_trust_tunnel_cloudflared` and its routes, and the box holds
only a token. Ingress cannot live in two places. A `config.yml` here plus rules
in Terraform is two sources of truth, and the failure is a route that works
until the next playbook run restores a stale copy. Adding a hostname is
therefore a Terraform change, and this role should not need touching again once
the token is in place.

Two details in the unit are load-bearing:

- **The token is an environment variable, never an argument.** `cloudflared`
  reads `TUNNEL_TOKEN` for `--token`. Passed as an argument it would sit in
  `/proc/<pid>/cmdline`, readable by every local user via `ps`.
- **`--no-autoupdate`.** The self-updater replaces its own binary and restarts,
  which makes the running version something no playbook chose and no commit
  records. `apt` owns the version.

### Running it

The tunnel exists as of 2026-07-26 — `bykami.id` is registered and the
Terraform resources in `infra/cloudflare/tunnel.tf` are applied. The token
comes from that stack:

```bash
cd ../infra/cloudflare
install -m 600 /dev/null /tmp/tv.yml
printf 'cloudflared_token: "%s"\n' "$(terraform output -raw tunnel_token)" > /tmp/tv.yml
cd ../../ansible && ansible-playbook site.yml -e @/tmp/tv.yml
rm -f /tmp/tv.yml
```

A file rather than `-e cloudflared_token=...` on the command line, because an
argument is visible in `ps` to every local user for as long as the play runs —
the same reason the systemd unit passes it by `EnvironmentFile`.

### Verifying it, which is not what you would guess

`systemctl is-active cloudflared` and the tunnel's own `healthy` status both
report the connector's link to Cloudflare's edge. Neither says anything about
whether your origin answers. A box with nothing listening on `127.0.0.1:8080`
reports `healthy` and serves 502.

The only check that means anything is a request that reaches the origin and
comes back:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://app.bykami.id/healthz
```

The `app` role now exists and the box serves the API, so this returns **200**.
A 502 today means the service is down, not that nothing is deployed — check
`systemctl status bykami` before suspecting the tunnel.

## `app`

Binary drop, systemd unit with `GOMEMLIMIT`, SQLite data dir, backups.

```bash
./scripts/build-linux.sh                   # leaves dist/bykami-linux
cd ansible && ansible-playbook site.yml --tags app -e @booth.vars.yml
```

The role ships a binary and refuses to build one — a toolchain on a production
host is a liability regardless of how fast it compiles.

**`copy` compares checksums, which is what makes a redeploy safe.** An unchanged
binary fires no handler, so re-running the play does not restart the service and
does not drop in-flight requests. Only a genuinely new binary causes a restart.

**The health check is the deploy gate.** After the handlers are flushed — the
`meta: flush_handlers` is load-bearing, without it the check would test the
*previous* binary — the role polls `/healthz`, which touches the database. A 200
proves the app reached its own storage. `systemctl is-active` proves only that a
process started, which is the same trap as reading tunnel status.

### Pull-based deploy

CI publishes a GitHub Release tagged `api-<sha>`; the box polls and installs.
Nothing reaches into the box, which is the point — the VPS has one inbound rule
and GitHub's runner ranges are far too broad to allowlist without undoing the
reason the tunnel exists.

**Off by default.** `app_update_enabled: false`, because a box that installs
whatever `main` published, unattended, is a path from a merged commit straight
to production. Turning it on is a deliberate act:

```bash
ansible-playbook site.yml --tags app -e app_update_enabled=true
```

The updater verifies a SHA-256 before anything touches the running system,
installs atomically with `mv`, restarts, then polls `/healthz` for 30 seconds.
On failure it restores the previous binary and leaves the version marker
pointing at the last release that passed — so the next run retries rather than
recording the bad release as installed.

All four paths were exercised against the real box on 2026-07-26: no release
(clean exit), good release (install + health pass), tampered binary (refused,
nothing changed), and a binary that never listens (installed, health failed,
rolled back, app healthy again after ~32s).

**The checksum proves integrity, not provenance.** Anyone who can push to
`main` can publish a release the box will install. That is the same trust
boundary as any CI deploy, and it is the reason the timer is opt-in. Signing
would close it and is not done.

**Do not sort releases by list order.** GitHub's list endpoint does not
reliably return newest-first — observed returning an older release ahead of a
newer one — so the script sorts on `published_at` explicitly. Taking the first
match installs a stale binary or appears to sit on the current version forever.

### Opening the operator console

Nobody can sign in to the console on a default deployment, and that is the gate
working rather than a bug: the console logs in with the same phone OTP flow
customers use, and `app_otp_delivery` is empty, so `/login` answers 503.

Opening it needs two values, both in a gitignored vars file:

```yaml
# app.vars.yml, mode 0600, gitignored
app_otp_delivery: "log"
app_admin_phones: "08xxxxxxxxxx"   # the operator's number, comma-separated for more
```

```bash
ansible-playbook site.yml --tags app -e @app.vars.yml
```

Then sign in at `https://app.bykami.id/`, and read the code out of the journal:

```bash
ssh <host> "journalctl -u bykami -n 30 | grep -o '\"code\":\"[0-9]*\"'"
```

**Understand what `log` costs before setting it.** There is exactly one sender in
the binary and it writes codes to the journal, so for as long as this is on, every
one-time code on the platform — customers' as well as operators' — is readable by
anything that can read the journal. On this box that is root, and root is the
person deploying. It is a considered trade for a single-operator studio that needs
the console before a WhatsApp provider account exists, not a thing to leave on
once one does. It is logged at `Warn` on every send so the exposure is visible
rather than silent.

To close it again, unset `app_otp_delivery` and re-run the play. Sessions already
issued survive — deliberately, see `internal/httpapi`: revoking every login by
changing a delivery setting would be a surprising way for that flag to behave.

### Connecting the studio's Google Calendars

Booking works with none of this. Availability comes from its own database, and the
busy ranges it would read from Google are a cache — see `api/internal/booking`. So
this is what buys the owner's own calendar back, not what makes booking function.

Three steps, and only the third is in the console.

**1. A service account, and its key in the environment.** In Google Cloud: a
project, the Calendar API enabled, a service account, and a JSON key. Base64 it,
because the key is multi-line PEM and an `EnvironmentFile` has no syntax for a
value with newlines:

```bash
base64 -w0 < service-account.json      # macOS: base64 -i service-account.json
```

```yaml
# app.vars.yml
app_google_credentials: "ewogICJ0eXBlIjogInNlcnZpY2VfYWNjb3VudCIsCi…"
```

A service account rather than OAuth on purpose: an unverified Google project
issues refresh tokens that expire after seven days, so a consent-screen
integration works for a week and then stops silently. A service-account key does
not rotate. The cost is step 2.

**2. Share each calendar with the service account.** In Google Calendar, per
calendar: settings → *Share with specific people* → add the service account's
address → permission **"Make changes to events"**. Nothing less lets it write a
booking in.

The address is printed on the console's **Pengaturan** page and logged at startup:

```bash
ssh <host> "journalctl -u bykami | grep service_account"
```

**3. Point each resource at its calendar id.** In the console, *Pengaturan* → paste
the Calendar ID next to each room → **Simpan** → **Tes sinkron sekarang**.
(Reaching the console needs the login opened — see above.) The
Status column then shows what Google actually said, per room, so a calendar that
was not shared says `notFound` instead of failing quietly for a week.

The id is not the calendar's name. Find it in Google Calendar under settings →
*Integrate calendar* → **Calendar ID**; it looks like an address, often
`…@group.calendar.google.com`.

Same job from a shell, if the console is not open yet:

```bash
bykami -db /var/lib/bykami/bykami.db booking resources
bykami -db /var/lib/bykami/bykami.db booking calendar photobox <id>
```

Clearing a calendar id detaches it and drops the ranges it had cached, so a room
whose calendar is removed does not stay blocked against a calendar nobody reads.

**The catalogue seeds itself.** The play fills the booking tables on any box whose
`booking_services` table is empty — the studio's three rooms, seventeen packages,
09:00–21:00 hours and the two prayer breaks — so a fresh install does not finish
healthy and then serve a booking page with no packages on it. It skips entirely
once anything is in the tables, so a price edited on the box is not overwritten by
the next deploy, and `app_booking_seed: false` turns it off.

It runs as the service user, not root. SQLite writes `-wal` and `-shm` files owned
by whoever opened the database, and seeding as root leaves the service unable to
write to its own storage — a booking page that lists packages and cannot take a
booking.

**What is still shell-only:** *changing* hours, breaks, packages or prices after
the initial seed, via `bykami booking` on the box. They change when the studio
buys a room, which is rarer than connecting a calendar.

### Backups are half-finished, deliberately visibly

A daily timer runs `sqlite3 .backup`, checks `PRAGMA integrity_check` and
deletes the copy if it fails, keeps 7, and writes `0600` under a `0750`
directory. `.backup` and not `cp`: the database is in WAL mode, so a file copy
can catch a checkpoint mid-flight and produce a backup that restores to a torn
state — and it usually looks fine, because the damage sits in pages the restore
does not touch until much later.

### Off-box copies to R2, and the storage cap

Enabled with a vars file — never `-e key=value`, which puts the secret in `ps`
for every local user:

```yaml
# r2vars.yml, mode 0600, gitignored
app_r2_enabled: true
app_r2_endpoint: "https://<account>.r2.cloudflarestorage.com"
app_r2_access_key_id: "..."
app_r2_secret_access_key: "..."
```

```bash
ansible-playbook site.yml --tags app -e @r2vars.yml
```

**Two independent limits, because retention by count is not a bound.** Thirty
copies of a 72 KB database is 2 MB; thirty copies of a database that has grown
to 500 MB is 15 GB, and the first sign would be a bill. So there is also
`app_r2_budget_bytes`, default 2 GB against a 10 GB free tier, checked against
the bucket's actual contents before every upload.

Room is made **before** uploading, not after. Uploading first and pruning second
means the bucket transiently exceeds the cap, which is exactly the moment a free
tier stops being free.

Pruning targets `budget - size`, not `budget`. Targeting the cap itself leaves
no room for the upload, so the newest backup gets refused while older ones are
kept — backwards, since the newest is the one worth having. This was a real bug,
found by testing with a deliberately tiny cap; it looks correct until the bucket
is actually full, and then it silently keeps the wrong copies.

A backup larger than the entire cap is refused outright rather than pruned
around: deleting every previous copy still would not make it fit, so the old
ones stay.

**`no_head = true` in the rclone config is required, not tuning.** R2 returns
501 Not Implemented for the HEAD rclone issues to verify an object after upload.
The data lands anyway — rclone reports a failed copy, retries, and the second
attempt succeeds — so every backup logged two ERROR lines while working
perfectly. A nightly error that self-heals is worse than either outcome: it
trains you to ignore the log, and the first genuine failure looks identical.
Since that flag removes rclone's own verification, the script compares the
uploaded size against the local file itself.

**A failed upload never fails the backup.** Losing the off-box copy is bad;
losing the local one because the network blipped is worse.

Verified on 2026-07-26: the timer ran, the backup passed its integrity check,
and it opened with the real schema — `users`, `sessions`, `otp_challenges`,
`loyalty_entries`, `schema_migrations`. A backup nobody has opened is a
hypothesis.

## `booth` — a test deployment, and it should not outlive the test

Puts the **booth agent** on this box behind `booth-test.bykami.id`. The agent is
a booth binary: on a real deployment it owns a printer, a hot folder and a camera
on a PC in a shop, and it has none of those here. What it has here is a real
HTTPS origin, which is the one thing a laptop cannot provide — `getUserMedia`
refuses to run on an insecure origin, so the capture flow cannot be tried on a
phone without it.

Off unless `booth_enabled` is true. The role is listed in `site.yml`
unconditionally so that turning the flag back off **stops** the service rather
than leaving whatever was last deployed running indefinitely.

```bash
./scripts/build-linux.sh                   # kiosk bundle, then both binaries
openssl rand -hex 24                       # then put it in a vars file
cd ansible && ansible-playbook site.yml --tags booth -e @booth.vars.yml
```

`booth.vars.yml` is gitignored, so the binary path in it is the one thing about
this deploy that version control cannot keep honest. Point it at the script's
output and it stays true:

```yaml
booth_binary_src: "{{ playbook_dir }}/../dist/bykami-agent-linux"
```

A path under `/tmp` or a scratch directory works exactly once, then the
directory is cleaned and the play stops on a missing binary.

`booth_access_token` takes a comma-separated list, one token per tester:

```yaml
booth_access_token: "<tester-1>,<tester-2>,<tester-3>"
```

Withdrawing somebody is deleting their token and re-running the play — their
cookie stops being recognised, everyone else's keeps working. With one shared
secret that operation costs everybody their access, which is why it never
happens.

Two things this role refuses to do:

- **Start without an access token.** The agent enforces it too; the assert here
  only moves the failure earlier and names it. Every route the agent serves is
  unauthenticated by design, and `/api/capture` takes a 16 MB upload.
- **Put the token in the unit file.** `/etc/systemd/system` is world-readable and
  `ExecStart` is visible in `ps`. It goes in a mode-0600 `EnvironmentFile`.

### Auto-deploy

`booth_update_enabled: true` puts the same release poller on the booth that
`app` has. CI publishes `agent-<sha>` with a linux/amd64 binary alongside the
Windows one; the box checks every ten minutes, verifies the checksum, installs,
restarts, and rolls back if `/api/state` does not come up.

Set it in the vars file **and clear `booth_binary_src`**. With both set, every
play run reinstalls the local build over whatever the timer pulled, which is a
deploy that silently goes backwards.

```yaml
booth_update_enabled: true
booth_binary_src: ""
```

This role's third refusal used to be pulling its own updates — *a temporary
surface should not acquire a supply chain*. What that argument was really
against was building a delivery mechanism nobody had asked for. Deploying by
hand turned out to mean deploying rarely, from a laptop, with the binary path
and the SSH firewall hole both going stale between runs; the supply chain was a
laptop either way, and a worse one.

The security property that mattered is untouched: **the box pulls, CI reaches
into nothing.** No inbound rule to open, no deploy credential in GitHub. What
the checksum does *not* buy is trust — anyone who can push to `main` can publish
a release, which is the same boundary as any CI deploy. Signing would close
that, and is not done here.

One difference from `app`, and the reason the script is not shared: the API is
a stateless request handler and can restart at any moment. The booth has a
customer standing in front of it who has already paid. So the updater **defers
while a session is live**, and treats a booth that will not answer `/api/state`
as busy rather than idle.

That wait is bounded at `booth_update_max_defer_minutes` (2h). Nothing sweeps
abandoned sessions — somebody who opens the QR screen and walks off leaves a row
in `awaiting_payment` forever — so an unbounded check would defer every release
from that moment on while still reporting success.

Watch it work:

```bash
systemctl list-timers bykami-agent-update.timer
journalctl -u bykami-agent-update -n 50
cat /var/lib/bykami-agent/.deployed-version
```

`GOMEMLIMIT` is 350MiB against the app's 1400MiB. The box is 2 GiB, the agent
decodes and rescales full-size frames, and without a limit of its own one 24 MP
frame at a bad moment takes the operator console down with it.

The matching Cloudflare ingress is `booth_test_enabled` in
`infra/cloudflare/`, also off by default. Both flags have to be on for the
hostname to resolve to anything.

## Order of operations, once the tunnel is real

1. `--tags base`, confirm a second run is `changed=0`
2. Install and start `cloudflared`
3. **Verify the tunnel actually carries traffic** — not that the service is
   `active`, which it will be regardless
4. Only then remove the port 22 ingress rule from the security group

Closing 22 before step 3 leaves a box reachable only through the VNC console.
