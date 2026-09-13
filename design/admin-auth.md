# Admin access — username, password, and who may manage whom

The console at `admin.bykami.id` signs in with a username and a password. The
username is the credential's label — "yudha", "kasir-1" — and the password
proves it. They are the same thing, not two columns: the label says who the
write is attributed to, and the password is the only secret.

This replaces the earlier single-password flow, which itself replaced TOTP.
Each step removed friction; this step removes the last ambiguity — a password
without a username meant whoever generated the password knew it, but the person
typing it in was not authenticated as a distinct individual. The username fixes
that, and the machinery behind it turns the console from a shared door into
individual accounts without adding a provider or an invitation flow.

## The decision

**A label and a password. The label is the identity, the password proves it.**

Not a phone, because a number was only ever there to name a person, and a label
does that without a SIM. Not a code from an app, because enrolling an app
before a person can work is the thing that blocked the console for months.

## One password per person, not one shared password

The form is identical either way, so this is not a question about the UI. It is
about what is behind it.

Give each operator their own generated password and the pair — label plus
password — is the identity: the server looks it up, finds whose it is, and
attributes the write to that person. Share one password and two properties die
at once:

- **Attribution.** `membership_purchases.operator` is `NOT NULL` and its comment
  says why — "Which human wrote the stamp. Provenance at the counter is a
  person." With one shared secret, that column records the fact that somebody
  logged in, which is the one thing never in doubt. The row a dispute needs is
  the row that stops meaning anything. Correcting it later is impossible: the
  question is unanswerable rather than unanswered.
- **Revocation.** Disabling one credential ends that person's access on their
  next request and nobody else notices. With a shared password, the only lever
  is rotation, which logs out everybody — including whoever was going to
  investigate. The usual result is that nobody rotates.

With one operator, per-person and shared are the same thing in practice: one
password exists, one person has it. The difference appears on the day there is a
second.

## Managers and the last-manager guard

Some operators may create or disable other operators; most may not. The
difference is `can_manage`, a boolean on `admin_credentials` added by migration
`0012_admin_operators.sql`. Migration `0011` — the password schema — was already
applied to the production database, so `0012` builds on it with `ALTER TABLE`
rather than rewriting what already exists.

The guard is enforced at both ends:

- **`UnsetManage` refuses to remove the last manager.** Somebody has to be able
to get back in.
- **`RemoveWithActor` refuses to disable the last manager.** A console with no
manager is a console nobody can administer, and the only fix would be a shell on
the box — which this box does not have.
- **The first credential is automatically a manager.** `AddWithCreator` computes
`can_manage` from a subquery: `CASE WHEN COUNT(*) = 0 THEN 1 ELSE 0 END`. The
console has no way to promote anybody, so shell enrolment is the only place a
first manager can come from. Two adds racing cannot both claim first because the
count and the insert are one statement.

Migration `0013_admin_manager.sql` repairs installs that `0012` left managerless:
when no non-disabled credential has `can_manage = 1`, it promotes the
earliest-created one. It is a no-op on an empty table and on a table that
already has a manager.

**Privileged actions — add, disable, promote, demote, reset — require the acting
manager to re-enter their own password** before the action happens. This is not a
second factor; it is a speed bump against a session that was left unattended, and
it records that the manager proved possession at the moment of the decision.

Adding an operator is the one that matters most, and it was briefly missing from
the list. The CSRF token is derived from the session cookie, so anybody holding a
stolen cookie can compute it — which means that without the re-entry, a stolen
cookie could *mint* a credential with its own password and survive the theft
being noticed, rather than borrow a session for thirty days. Re-authentication is
what makes a stolen session recoverable.

The flow is three requests, and the split between them is load-bearing:

1. Each action's button is a **GET** to the interstitial. A GET starts a flow and
   performs nothing, so no route carries one of these actions out on a GET: a
   link, a refresh or a scanner's prefetch can never disable somebody.
2. The interstitial posts **the manager's own password**. Proving possession mints
   a single-use token that expires in a minute.
3. That token lands on a **confirmation page**, which says in words what is about
   to happen and posts to the action. Only that POST performs it.

The confirmation page is not decoration. The interstitial used to redirect
straight to the action with its token in the query, and the action routes are
POST-only — so a browser that had just typed the password correctly landed on
`405 Method Not Allowed` and the action silently never ran. It passed its tests
only because they read the `Location` header and posted by hand, which is not
something a browser does.

## A credential is born unrotated

`must_change` is 1 on every new credential, including the first one created from
the shell. Until the operator sets a password of their own, every request they
make is redirected to one page, and that page is the only one they can reach.

The reason is attribution, which is the whole reason the username exists. A
generated password is known to whoever generated it, so if it stayed the working
password the manager could sign in as the cashier — and every write that cashier
made would be deniable, with the row naming a human meaning very little. Rotating
at first use kills the handed-over password before it can serve as an alibi, and
`created_by` records that the handover happened anyway.

The new password is the operator's own, so the server asks for twelve characters
and nothing more. Length is the only rule that does measurable work; symbol
requirements mostly produce `Password1!` and a note on the monitor.

**Reset is the manager's job, and it stays in the console.** `/operators` has a
Reset action that issues a fresh one-time password, shows it once, and sets
`must_change` again — the same path as creating somebody, needing nobody outside
this system. Email was considered and rejected: there is no mail sender here at
all, credentials have no address to send to, and a reset link would make the
mailbox the way into an admin console, which is a weaker lock than the password
it protects. When a WhatsApp provider eventually exists for customer OTP, the
same sender can carry a short-lived code. Until then the manager standing next to
the person is the recovery path — and if the manager forgets their own password,
the shell on the box is the way back in, which is exactly why that bootstrap
stays.

## What is given up, stated plainly

**Two factors became one, and one factor is honestly one.** A password that
leaks, that is shoulder-surfed at the counter, or that is reused from somewhere
else is total access to every customer's phone number, every card, every
booking, and the ability to void a purchase. That is a real downgrade from
TOTP, taken deliberately in exchange for not having to enrol an authenticator
before a person can work.

Everything below exists to make one factor as strong as one factor can be.

## How the login works

```
admin_credentials
  id           TEXT PRIMARY KEY
  label        TEXT NOT NULL UNIQUE      -- "yudha", "kasir-1"
  lookup       BLOB NOT NULL UNIQUE      -- SHA-256 of the password
  hash         BLOB NOT NULL             -- salted PBKDF2-HMAC-SHA256
  salt         BLOB NOT NULL
  can_manage   INTEGER NOT NULL DEFAULT 0
  created_at   INTEGER NOT NULL
  last_used_at INTEGER
  disabled_at  INTEGER
  created_by   TEXT
  disabled_by  TEXT
```

`lookup` is the index, because a KDF cannot be searched. `hash` is what is
verified. **Both are checked**, and that ordering is the point: a lookup that
merely found a row would make a database read a login, because the reader could
add their own row and sign in. Verifying the KDF afterwards means the index is
useful for finding a candidate and useless as a credential.

The password is generated, not chosen: 24 characters of base32-ish alphabet from
`crypto/rand`, which is ~120 bits. That is why a fast hash is acceptable as the
index and why the KDF's iteration count is insurance rather than the main
defence — there is no dictionary to attack.

Hashing is `crypto/pbkdf2` with HMAC-SHA-256 from the standard library. No new
dependency: the repository's dependency list is deliberately tiny, and the
alternative (`golang.org/x/crypto`) would be a module added for one call.

## Enrolment never touches the web

```
bykami admin password add kasir-1       # prints the password once
bykami admin password list              # labels and last use, never a secret
bykami admin password rm kasir-1        # access ends on their next request
bykami admin password manage kasir-1    # grant can_manage
bykami admin password unmanage kasir-1  # revoke can_manage
```

The same rule as `admin enroll` before: the secret is minted on the box and
shown to whoever is standing there, once, and there is no route that creates a
credential. A web-invited operator needs an invitation flow, an email or a
WhatsApp message, and a way to revoke the invitation — three things this system
has no provider for and does not want.

## The console stops borrowing a customer session

Today signing in to the console calls `identity.StartSession`, which mints a row
in `users` for the operator's phone number. That is why the comment in
`identity.go` says "an operator who has never been a customer has no users row
until the first time they sign in to the console". With no phone there is no
such row to mint, and the borrowing stops:

```
admin_sessions
  token_hash BLOB PRIMARY KEY
  credential_id TEXT NOT NULL REFERENCES admin_credentials (id)
  expires_at INTEGER NOT NULL
  created_at INTEGER NOT NULL
```

Thirty days, unchanged, and the cookie keeps its `__Host-` shape: `Secure`,
`Path=/`, no `Domain`, `HttpOnly`, `SameSite=Strict`. One paste per device per
month. A console session is now a console session and nothing else.

## Why not a JWT

A JWT is self-contained, which is precisely the problem: it cannot be revoked
before it expires, so `password rm kasir-1` would keep working until their token
ran out, and a stolen one is valid for its full lifetime. It also moves identity
*into* the token, where the client holds it, instead of deriving it per request —
the property `admin.go` was built around ("privilege is derived per request and
never stored in the session"). The opaque token in `admin_sessions` is what makes
revocation instant, and there is nothing to gain by replacing it.

## Failures are counted, because there is no username to count them against

A single password field has no identifier to rate-limit, so the count is per
source address: ten failures in fifteen minutes locks that address out for
fifteen, failures are logged with the address, and the response is the same
sentence every time. The comparison is constant-time and a wrong password costs
the same whether or not any credential exists.

This is weaker than the per-number lockout it replaces, and honestly so: an
attacker with many addresses can spread attempts. The mitigation is the password
itself, which is 120 bits and not guessable, and the lockout is there to stop the
cost of trying rather than to be the thing that saves us.

## What is removed

- **The authenticator login path**, and with it `bykami admin enroll` and
  `bykami admin revoke`. `internal/mfa` and the `admin_totp` table from
  migration `0005` stay in the tree rather than being deleted in the same change:
  migration `0005` may already have run on the box, and a schema that removes a
  table is a schema that cannot be rolled back. Unused is recoverable; dropped is
  not.
- **`-admin-phones` stops being an authentication input.** It keeps one job: the
  operator attributed by `bykami membership import` when no label is given. That
  flag should eventually become `-operator <label>`, and that is a follow-up, not
  part of this.

## Reversal

The TOTP registry is left in place and the console's login is one handler, so
restoring the authenticator is putting a second step back in front of a session
that already exists. Nothing here forecloses it, and if the owner later wants two
factors, the shape to reach for is per-credential, not a global switch.

## What the change is judged by

Not by whether it is easier to log in — it obviously is. By whether **a write in
the ledger still names a person**. If `membership_purchases.operator` starts
carrying a label that three people share, the change failed on its own terms,
whatever the login screen looks like.
