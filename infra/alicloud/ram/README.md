# RAM identity for CI

Two policies, applied by hand in the console, once. They are not Terraform
resources because Terraform cannot authenticate until they exist — and because a
CI identity that CI can rewrite is not a boundary.

Replace `ACCOUNT_ID` in both files with the Alibaba account ID before pasting.

## The IdP's certificate thumbprint will expire, and OIDC will stop working

Registered 2026-07-26 against the chain GitHub serves today:

| | |
|---|---|
| Thumbprint | `ab9d0263244dd0326eb67015705a667e79cfe998` |
| Certificate | `CN=Root YR, O=ISRG` (displayed by the console under its *issuer*, `ISRG Root X1`) |
| Valid until | 2032-09-03 |

Alibaba pins trust to this thumbprint. Let's Encrypt rotates intermediates far
more often than that expiry date suggests, and GitHub has changed CA before — so
the practical lifetime is unknown and shorter than 2032.

The failure is abrupt and reads like a permissions problem: every workflow run
starts failing at `AssumeRoleWithOIDC` on a day nobody changed anything. The fix
is to re-fetch and add the new thumbprint, but the diagnosis is what costs the
afternoon, which is the reason this table exists.

RAM accepts up to five thumbprints and tries each, so adding a second costs
nothing and covers the case where the served chain shortens by one link.
Worth adding now: **`cabd2a79a1076a31f21d253635cb039d4329a5e8`** — ISRG Root X1,
self-signed, valid to 2035-06-04.

To re-derive either value:

```bash
openssl s_client -servername token.actions.githubusercontent.com \
  -showcerts -connect token.actions.githubusercontent.com:443 </dev/null 2>/dev/null \
| openssl x509 -noout -fingerprint -sha1
```

That prints the *leaf*. The registered thumbprint is the last certificate in the
chain, so split the output and fingerprint that one — the certificate whose
issuer the console echoes back at you.

## `trust-policy.json` — who may assume the role

The load-bearing line is `oidc:sub`. It pins the role to **this repository on
these refs**. Without it, the trust policy says "any GitHub Actions run anywhere
in the world", because every public runner presents a token from the same
issuer with the same audience. That is the single misconfiguration that turns
OIDC from an improvement into a liability, and it is easy to miss precisely
because everything works either way.

`repo:…:pull_request` is listed alongside `refs/heads/main` so that pull requests
opened from branches in this repository can plan. Forks are excluded by GitHub
itself, which withholds the token there, and again by the workflow's own
fork guard — two independent mechanisms, neither relying on the other.

**The `@`-suffixed numbers are not a typo.** GitHub's subject claim now embeds
immutable IDs by default — user `44221675`, repository `1312201709` — so the sub
reads `repo:bhaktiyudha@44221675/bykami@1312201709:…` rather than the
name-only form most documentation still shows. Confirm with:

```bash
gh api repos/:owner/:repo/actions/oidc/customization/sub
```

Match it rather than switching the repository back to name-only. IDs cannot be
released and re-registered by someone else, so this form closes the attack where
a repository is renamed or deleted and its old name claimed by a stranger who
then inherits the trust policy.

The cost is that **transferring or recreating this repository silently breaks
OIDC**, with an `ImplicitDeny` that names no condition. Re-run the command above
and update this file if that ever happens.

Adding a ref to this list grants whoever can push that ref everything the role
can do. That is a permission change, not configuration — and it is why the next
section matters more than this one.

## `ci-policy.json` — what the role may do

**Read-only, because the workflow only plans.** `alicloud.yml` runs `terraform
validate` and `terraform plan`; there is no apply job. A role that can write is
therefore granting an authority nothing exercises, and an unexercised permission
is the one nobody notices being used.

This is also what settles the `pull_request` sub above. A pull request from a
branch in this repository can assume the role and read six `Describe` calls.
Today that is uninteresting even though `main` is unprotected and anyone with
push access could reach the role by pushing to `main` anyway. The read-only
policy is what keeps it uninteresting after `main` *is* protected — at which
point the PR path would otherwise be a way around the protection.

**When an apply job is added, writes come back in the same commit** — scoped to
the one key pair and the one security group this stack owns, and granted to a
role restricted to `environment:production` rather than to any PR. Not before.
The two halves are one change: the permission and the thing that needs it should
never be separated by six months of nobody remembering.

Note what is missing even from the eventual write set: nothing can create an
instance, so a stolen token cannot mine cryptocurrency on the account — the usual
reason cloud credentials are worth stealing.

The `Deny` block is the durable half, and stays whatever the `Allow` block says.
Explicit denies win over any later `Allow`, including the apply-job grant above
and including a broad managed policy attached in a hurry six months from now. It
closes the two failures that actually matter here:

- **`ReplaceSystemDisk`, `DeleteInstance`** — the trial discount is bound to
  instance `i-t4n7sxn0wwzevsimxwnr`. Losing that instance means losing the trial,
  silently, with the box apparently still working.
- **`ModifyInstanceChargeType`, `RenewInstance`, `ModifyInstanceAutoReleaseTime`**
  — billing changes are an owner decision made in the console. CI has no reason
  to touch them and every reason not to be able to.

These denies apply only to the CI role. Re-imaging the box to Ubuntu is done by
the account owner and is unaffected.
