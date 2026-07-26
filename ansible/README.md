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

Until the `app` role exists, that returns 502 and **that is the correct
answer** — the tunnel was verified end to end on 2026-07-26 with a throwaway
listener on 8080, which returned 200.

## Order of operations, once the tunnel is real

1. `--tags base`, confirm a second run is `changed=0`
2. Install and start `cloudflared`
3. **Verify the tunnel actually carries traffic** — not that the service is
   `active`, which it will be regardless
4. Only then remove the port 22 ingress rule from the security group

Closing 22 before step 3 leaves a box reachable only through the VNC console.
