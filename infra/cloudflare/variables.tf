# Four permissions, because one provider block serves this whole stack:
# Account -> Cloudflare Tunnel: Edit, Account -> Cloudflare Pages: Edit,
# Zone -> DNS: Edit, Zone -> Zone: Read. A tunnel-only token plans clean and
# then fails on the Pages resources; a Pages-only token cannot see the zone at
# all, which presents as an empty zone lookup rather than a permission error.
#
# Kept separate from the narrower Pages-only token in the GitHub
# CLOUDFLARE_API_TOKEN secret, which wrangler uses to deploy. Widening that one
# instead would put tunnel and DNS authority into every site deploy.
variable "cloudflare_api_token" {
  description = "Cloudflare API token: Tunnel:Edit, Pages:Edit, DNS:Edit, Zone:Read."
  type        = string
  sensitive   = true
}

variable "cloudflare_account_id" {
  description = "Cloudflare account ID."
  type        = string
}

variable "cloudflare_zone_id" {
  description = "Zone ID for bykami.id."
  type        = string
}
