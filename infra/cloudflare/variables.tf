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

# Off by default, and turning it on is meant to be a deliberate act with a diff
# behind it.
#
# The booth agent is a local process on a booth PC: it serves the kiosk to the
# browser on that same machine and answers only to localhost. This publishes it,
# which exists for one reason — getUserMedia refuses to run on an insecure
# origin, so testing the capture flow on a phone or a tablet needs real HTTPS
# and a real hostname.
#
# The agent itself refuses to start with a public host and no access token, so
# this cannot on its own put an open photo-upload endpoint on the internet. It
# is still a public surface that would not otherwise exist.
variable "booth_test_enabled" {
  description = "Publish booth-test.bykami.id through the tunnel to the booth agent. Test deployments only."
  type        = bool
  default     = false
}

variable "booth_test_hostname" {
  description = "Hostname for the booth test deployment."
  type        = string
  default     = "booth-test.bykami.id"
}
