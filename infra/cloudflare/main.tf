terraform {
  required_version = ">= 1.5"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.12"
    }
  }

  # State holds secrets in plaintext and this repository is public, so it must
  # never be committed. Phase 1 uses a local file, gitignored — the only managed
  # resources are four Pages projects, which are trivially recreatable.
  #
  # Migrate to the R2 backend below when the zone lands in phase 2, at which
  # point state starts holding tunnel credentials and is genuinely valuable.
  #
  # backend "s3" {
  #   bucket                      = "bykami-tfstate"
  #   key                         = "phase-1.tfstate"
  #   region                      = "auto"
  #   endpoints                   = { s3 = "https://<account>.r2.cloudflarestorage.com" }
  #   skip_credentials_validation = true
  #   skip_region_validation      = true
  #   skip_requesting_account_id  = true
  #   skip_s3_checksum            = true
  #   use_path_style              = true
  # }
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
