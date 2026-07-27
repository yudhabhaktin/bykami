# The tunnel is how anything reaches the VPS, and the reason the VPS needs no
# open inbound port. cloudflared dials out from the box and Cloudflare routes
# to that connection, so the security group can end up with no ingress rules at
# all and nothing depends on a firewall rule staying correct.

resource "cloudflare_zero_trust_tunnel_cloudflared" "app" {
  account_id = var.cloudflare_account_id
  name       = "bykami-app"

  # Remotely-managed, and this one setting is what divides ownership between
  # this file and ansible/. With "cloudflare", ingress lives in the resource
  # below and the box holds nothing but a token. With "local" the box would
  # need a config.yml, which would put routing in two places that drift — the
  # failure being a route that works until the next playbook run restores a
  # stale copy.
  #
  # roles/cloudflared is written against this choice: it installs no config.
  config_src = "cloudflare"

  # No tunnel_secret. It sets the password for a *locally*-managed tunnel, so
  # for config_src = "cloudflare" it is one more secret to store for nothing.
}

resource "cloudflare_zero_trust_tunnel_cloudflared_config" "app" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.app.id
  source     = "cloudflare"

  config = {
    ingress = concat(
      [
        {
          # The operator admin surface, per platform-architecture.md. 127.0.0.1
          # and not 0.0.0.0: the Go binary binds localhost precisely so that the
          # tunnel is the only path to it, and pointing this at a public
          # interface would quietly undo that.
          hostname = "app.bykami.id"
          service  = "http://127.0.0.1:8080"
        },
      ],
      # The booth agent, when a test deployment is up. A separate hostname
      # rather than a path under app.bykami.id: the two are different programs
      # with different lifetimes, and path routing would put the kiosk one
      # cloudflared config error away from shadowing the operator console.
      var.booth_test_enabled ? [
        {
          hostname = var.booth_test_hostname
          service  = "http://127.0.0.1:8899"
        },
      ] : [],
      [
        {
          # Required, and required to be last. cloudflared demands a catch-all
          # with no hostname; without it the whole config is rejected. 404 rather
          # than a proxy target so an unrouted hostname fails obviously instead
          # of silently landing on the admin app.
          service = "http_status:404"
        },
      ],
    )
  }
}

resource "cloudflare_dns_record" "app" {
  zone_id = var.cloudflare_zone_id
  name    = "app.bykami.id"
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.app.id}.cfargotunnel.com"

  # Both of these are load-bearing. cfargotunnel.com resolves to nothing in
  # public DNS — it is meaningful only to Cloudflare's edge — so an unproxied
  # record produces a hostname that simply does not resolve. And ttl must be 1
  # (automatic) whenever proxied is true; any other value is rejected.
  proxied = true
  ttl     = 1

  comment = "Cloudflare Tunnel to the VPS. Managed by Terraform."
}

resource "cloudflare_dns_record" "booth_test" {
  count = var.booth_test_enabled ? 1 : 0

  zone_id = var.cloudflare_zone_id
  name    = var.booth_test_hostname
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.app.id}.cfargotunnel.com"

  proxied = true
  ttl     = 1

  comment = "Booth agent test deployment. Temporary. Managed by Terraform."
}

# The connector token, which is what Ansible feeds to cloudflared. It is a
# bearer credential for the entire tunnel: anyone holding it can register a
# connector and receive traffic for every hostname routed above. It is a data
# source rather than an attribute because the v5 tunnel resource exposes no
# token field — reaching for .token fails at plan time.
data "cloudflare_zero_trust_tunnel_cloudflared_token" "app" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.app.id
}
