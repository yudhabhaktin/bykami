# Cloudflare stack

Pages projects, the Zero Trust tunnel, its ingress rules, and the DNS record
that points at it.

## Running it

State lives in R2, so `terraform init` needs the endpoint. It cannot be
hardcoded in the backend block: this repository is public and the endpoint
contains the account ID.

```bash
set -a && . /Users/ina/yudha/.env && set +a

# The s3 backend reads AWS_* names. The R2_* names the Ansible backup role
# uses are not consulted, and a mismatch reports "no valid credential
# sources" rather than a permission error — which sends you looking at the
# token instead of the variable name.
export AWS_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export AWS_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"

export TF_VAR_cloudflare_api_token="$CLOUDFLARE_API_TOKEN"
export TF_VAR_cloudflare_account_id=<account id>
export TF_VAR_cloudflare_zone_id=<zone id>

terraform init -backend-config="endpoints={s3=\"$R2_ENDPOINT\"}"
terraform plan
```

Forget the `-backend-config` and init fails at the endpoint, not with a
missing-argument message.

## Why state moved to R2

Migrated 2026-07-27, the day the tunnel landed. Before that, state held four
Pages projects and losing it meant re-importing them — annoying, not
dangerous. It now holds the tunnel's **connector token in plaintext**, which
is a bearer credential for every hostname routed through the tunnel. That is
not a thing to keep in a directory one `git add -f` away from a public
repository.

The pre-migration local files are gone. `terraform.tfstate.backup` in
particular contained the token and is exactly what the migration existed to
remove.

## The `skip_*` flags are not tuning

R2 implements the S3 API but not STS, not the AWS region model, and not the
trailing checksum header. Each flag turns off a call that would otherwise be
made to an AWS service that is not there:

| Flag | Without it |
|---|---|
| `skip_credentials_validation` | calls STS `GetCallerIdentity`, which R2 has no equivalent of |
| `skip_region_validation` | rejects `auto`, which is the only region R2 accepts |
| `skip_requesting_account_id` | tries to resolve an AWS account ID |
| `skip_metadata_api_check` | probes EC2 IMDS and stalls before failing |
| `skip_s3_checksum` | sends a trailing checksum header R2 rejects |
| `use_path_style` | uses virtual-host addressing, wrong for the R2 endpoint |

## What is deliberately not here

`infra/alicloud` has **no** backend and did not migrate. Every resource there
is behind a `count` of 0 and the instance is a data source, so its state is
empty — a backend would protect nothing while breaking the CI plan job, which
runs a plain `terraform init` with no R2 credentials. See the note in that
stack's `main.tf` for when to add one.

The four Pages custom domains and the five CNAMEs serving them are live but
unmanaged. Adopting them means `cloudflare_pages_domain` resources plus
`cloudflare_dns_record` blocks, imported rather than created — creating them
would collide with what is already serving traffic.
