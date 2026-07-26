variable "cloudflare_api_token" {
  description = "Cloudflare API token scoped to Pages:Edit on the bykami account."
  type        = string
  sensitive   = true
}

variable "cloudflare_account_id" {
  description = "Cloudflare account ID."
  type        = string
}
