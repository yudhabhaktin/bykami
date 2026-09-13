# Admin access — one password, and what it costs

The console at `admin.bykami.id` signs in with an operator's phone number and a
six-digit code from an authenticator app, and who counts as an operator is a
startup allow-list (`-admin-phones`) checked against the currently verified
session on every request. That is a good shape for a system with several
operators.

It is being replaced with a single password field. No username, no phone, no
code. This record is about what that trades away and how the parts that survive
the change survive it.

## The decision

**One field. A password. Nothing else.**

Not a username, because the credential is the identifier — see below. Not a
phone, because the number was only ever there to name a person.

## One password per person, not one shared password

The form is identical either way, so this is not a question about the UI. It is
about what is behind it.

Give each operator their own generated password and the password *is* the
identity: the server looks it up, finds whose it is, and attributes the write to
that person. Share one password and two properties die at once:

- **Attribution.** `membership_purchases.operator` is `NOT NULL` and its comment
  says why — "Which human wrote the stamp. Provenance at the counter is a
  person." With one shared secret, that column records the fact that somebody
  logged in, which is the one thing never in doubt. The row a dispute needs is
  the row that stops meaning anything. Correcting it later is impossible: the
  question is unanswerable rather than unanswered.
- **Revocation.** Removing one person from `-admin-phones` ends their access on
  their next request and nobody else notices. With a shared password, the only
  lever is rotation, which logs out everybody — including whoever was going to
  investigate. The usual result is that nobody rotates.

With one operator, per-person and shared are the same thing in practice: one
password exists, one person has it. The difference appears on the day there is a
second.

## What is given up, stated plainly

**Two factors become one.** Phone-plus-code is something on a list plus something
in a hand; a password is a single thing. A password that leaks, that is shoulder
surfed at the counter, or that is reused from somewhere else is now total access
to every customer's phone number, every card, every booking, and the ability to
void a purchase. That is a real downgrade and it is being taken deliberately, in
exchange for not having to enrol an authenticator before a person can work.

Everything below exists to make one factor as strong as one factor can be.

## How one field identifies somebody

```
admin_credentials
  id           TEXT PRIMARY KEY
  label        TEXT NOT NULL UNIQUE      -- "yudha", "kasir-1"
  lookup       BLOB NOT NULL UNIQUE      -- SHA-256 of the password
  hash         BLOB NOT NULL             -- salted PBKDF2-HMAC-SHA256
  salt         BLOB NOT NULL
  created_at   INTEGER NOT NULL
  last_used_at INTEGER
  disabled_at  INTEGER
```

`lookup` is the index, because a KDF cannot be searched. `hash` is what is
verified. **Both are checked**, and that ordering is the point: a lookup that
merely found a row would make a database read a login, because the reader could
add their own row and sign in. Verifying the KDF afterwards means the index is
useful for finding a candidate and useless as a credential.

The password is generated, not chosen: 24 characters of base32-ish alphabet from
`crypto/rand`, which is ~120 bits. That is why a fast hash is acceptable as the
index and why the KDF's iteration count is insurance rather than the main defence
— there is no dictionary to attack.

Hashing is `crypto/pbkdf2` with HMAC-SHA-256 from the standard library. No new
dependency: the repository's dependency list is deliberately tiny, and the
alternative (`golang.org/x/crypto`) would be a module added for one call.

## Enrolment never touches the web

```
bykami admin password add kasir-1     # prints the password once
bykami admin password list            # labels and last use, never a secret
bykami admin password rm kasir-1      # access ends on their next request
```

The same rule as `admin enroll` today: the secret is minted on the box and shown
to whoever is standing there, once, and there is no route that creates a
credential. A web-invited operator needs an invitation flow, an email or a
WhatsApp message, and a way to revoke the invitation — three things this system
has no provider for and does not want.

## The console stops borrowing a customer session

Today signing in to the console calls `identity.StartSession`, which mints a row
in `users` for the operator's phone number. That is why the comment in
`identity.go` says "an operator who has never been a customer has no users row
until the first time they sign in to the console". With no phone there is no such
row to mint, and the borrowing stops:

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
  table is a schema that cannot be rolled back.  Unused is recoverable; dropped is
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
