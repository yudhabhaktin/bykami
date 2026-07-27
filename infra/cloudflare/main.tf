terraform {
  required_version = ">= 1.5"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.12"
    }
  }

  # State lives in R2, not on a laptop and never in this repository.
  #
  # Migrated 2026-07-27, when the tunnel landed. Until then state held four
  # Pages projects and losing it meant re-importing them; it now holds the
  # tunnel's connector token in plaintext, which is a bearer credential for
  # every hostname routed through the tunnel.
  #
  # The account ID is deliberately not written here — this repository is public.
  # It comes from the environment, which is also what keeps this block valid
  # HCL: backend blocks cannot interpolate variables, so the endpoint has to be
  # supplied at init time either way:
  #
  #   terraform init -backend-config="endpoints={s3=\"$R2_ENDPOINT\"}"
  #
  # Credentials are AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, which is what the
  # s3 backend reads. The R2_* names the backup role uses are not consulted, and
  # a mismatch presents as "no valid credential sources" rather than as a
  # permission error.
  #
  # The skip_* flags are not optional tuning. R2 implements the S3 API but not
  # STS, not the AWS region model, and not the trailing checksum header, so each
  # one turns off a call that would otherwise fail against a non-AWS endpoint.
  backend "s3" {
    bucket = "bykami-tfstate"
    key    = "cloudflare.tfstate"
    region = "auto"

    skip_credentials_validation = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_s3_checksum            = true
    use_path_style              = true
  }
}

provider "cloudflare" {
  # Supplied as CLOUDFLARE_API_TOKEN. Never written to a .tfvars file.
  api_token = var.cloudflare_api_token
}

locals {
  # Each vertical is a separate Pages project so it owns its own sitemap and
  # deploys independently — a studio copy change must not redeploy the others.
  sites = {
    root       = { project = "bykami-root", hostname = "bykami.id" }
    studio     = { project = "bykami-studio", hostname = "studio.bykami.id" }
    booth      = { project = "bykami-booth", hostname = "booth.bykami.id" }
    dimsamcong = { project = "bykami-dimsamcong", hostname = "dimsamcong.bykami.id" }
  }
}

resource "cloudflare_pages_project" "site" {
  for_each = local.sites

  account_id        = var.cloudflare_account_id
  name              = each.value.project
  production_branch = "main"
}

# Custom domains are attached but NOT managed here, which is drift rather than
# a decision. bykami.id was registered on 2026-07-26 and the domains were
# attached through the dashboard; the zone now carries CNAMEs for the apex,
# www, studio, booth and dimsamcong, none of them in state.
#
# Terraform owns the four projects and, in tunnel.tf, the zone's one other
# record. Adopting the rest means cloudflare_pages_domain resources plus
# cloudflare_dns_record blocks for the five CNAMEs, imported rather than
# created — creating them would collide with what is already live.
