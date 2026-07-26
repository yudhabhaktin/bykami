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
    root       = { project = "bykami-root", hostname = "bykami.com" }
    studio     = { project = "bykami-studio", hostname = "studio.bykami.com" }
    booth      = { project = "bykami-booth", hostname = "booth.bykami.com" }
    dimsamcong = { project = "bykami-dimsamcong", hostname = "dimsamcong.bykami.com" }
  }
}

resource "cloudflare_pages_project" "site" {
  for_each = local.sites

  account_id        = var.cloudflare_account_id
  name              = each.value.project
  production_branch = "main"
}

# Custom domains are deliberately absent. They require the zone, and bykami.com
# is not registered yet. Until then every site serves from <project>.pages.dev
# under noindex, while canonical URLs already point at the hostnames above — so
# cutover is attaching domains and flipping BYKAMI_INDEXABLE, not a migration.
