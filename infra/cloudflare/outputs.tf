output "tunnel_id" {
  description = "UUID of the Cloudflare Tunnel."
  value       = cloudflare_zero_trust_tunnel_cloudflared.app.id
}

output "tunnel_status" {
  description = <<-EOT
    inactive, degraded, healthy or down. Note what this does NOT measure: it
    reports the connector's link to Cloudflare's edge, not whether the origin
    answers. A tunnel with nothing listening on 127.0.0.1:8080 reads healthy
    and serves 502, so this is not the check that proves a deploy worked.
  EOT
  value       = cloudflare_zero_trust_tunnel_cloudflared.app.status
}

output "tunnel_token" {
  description = "Connector token for cloudflared. Feed to Ansible; never commit."
  value       = data.cloudflare_zero_trust_tunnel_cloudflared_token.app.token
  sensitive   = true
}
