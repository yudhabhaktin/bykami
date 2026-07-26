terraform {
  required_version = ">= 1.5"

  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = "~> 1.286"
    }
  }

  # Same rule as the Cloudflare stack: state holds secrets in plaintext and this
  # repository is public, so it must never be committed. Local file for now,
  # gitignored. Migrate to the shared R2 backend at the same time Cloudflare's
  # does — see infra/cloudflare/main.tf for the block.
}

provider "alicloud" {
  region = var.region

  # CI holds no Alibaba credentials at all.
  #
  # GitHub mints a short-lived OIDC token per run; RAM trades it for STS
  # credentials that expire in 15 minutes and are scoped, by the role's trust
  # policy, to this repository on this branch. There is no long-lived AccessKey
  # to leak from a public repo, to rotate, or to forget to revoke — and a stolen
  # run log is worth nothing 15 minutes later.
  #
  # The block disappears when oidc_role_arn is empty, which is how a local run
  # falls back to ALIBABA_CLOUD_ACCESS_KEY_ID / _ACCESS_KEY_SECRET or an
  # ~/.aliyun profile. Note those names: ALICLOUD_ACCESS_KEY and
  # ALICLOUD_SECRET_KEY are deprecated as of provider v1.228.0 and are silently
  # ignored, which presents as "no credentials" rather than as a warning.
  dynamic "assume_role_with_oidc" {
    for_each = var.oidc_role_arn == "" ? [] : [1]
    content {
      oidc_provider_arn  = var.oidc_provider_arn
      role_arn           = var.oidc_role_arn
      oidc_token_file    = var.oidc_token_file
      role_session_name  = "github-actions"
      session_expiration = 900
    }
  }
}

# The trial instance is NOT a managed resource, and that is the whole design of
# this file.
#
# The free trial is attached to this specific instance ID via a savings plan. A
# managed alicloud_instance would let Terraform decide to replace it — image_id
# and instance_type are both ForceNew — and a replacement is a new instance ID,
# which is very unlikely to inherit the trial. The failure mode is silent: the
# apply succeeds, the box comes back, and the billing quietly turns into
# pay-as-you-go at full rate.
#
# So Terraform owns only what is safely additive: the SSH key pair and the
# security group rules. The instance, VPC, vSwitch, and security group were
# created by the trial flow and stay console-owned until the trial ends. The
# commented alicloud_instance at the bottom of this file is what replaces all of
# this when the box is bought for real.
data "alicloud_instances" "trial" {
  ids = [var.instance_id]

  # Suppresses a second API call, DescribeInstanceRamRole, which populates
  # ram_role_name and disk_device_mappings. Nothing here reads either. Left on,
  # it fails the plan under a least-privilege role — and the honest fix is to
  # stop making the call, not to grant a permission for data we discard.
  enable_details = false
}

# Console-created instances have no key pair, so login is a console-reset root
# password — fine for a rescue path, unusable for Ansible and unacceptable as
# the standing arrangement.
#
# Alibaba accepts RSA only; ed25519 is rejected on import. 2048-bit is what the
# console generates and what this expects.
resource "alicloud_ecs_key_pair" "deploy" {
  count = var.ssh_public_key == "" ? 0 : 1

  key_pair_name = "bykami-deploy"
  public_key    = var.ssh_public_key
}

# force = true reboots the instance so the key works immediately. Acceptable
# because nothing is serving yet; revisit before there is traffic to drop.
resource "alicloud_ecs_key_pair_attachment" "deploy" {
  count = var.ssh_public_key == "" ? 0 : 1

  key_pair_name = alicloud_ecs_key_pair.deploy[0].key_pair_name
  instance_ids  = [var.instance_id]
  force         = true
}

# Bootstrap-only, and off by default.
#
# The end state has no inbound SSH at all: cloudflared dials out, so the box
# needs no reachable port for either traffic or deploys. But the tunnel has to be
# installed over something, and that something is this rule. Set ssh_admin_cidr
# to your own address, bootstrap, then clear it — leaving it set is a standing
# exposure that buys nothing once the tunnel is up.
resource "alicloud_security_group_rule" "ssh_bootstrap" {
  count = var.ssh_admin_cidr == "" ? 0 : 1

  security_group_id = var.security_group_id
  type              = "ingress"
  ip_protocol       = "tcp"
  port_range        = "22/22"
  cidr_ip           = var.ssh_admin_cidr
  nic_type          = "intranet" # required for VPC security groups
  policy            = "accept"
  priority          = 1
  description       = "Bootstrap SSH. Remove once cloudflared is running."
}

# No ingress rules for 80/443 anywhere in this file, on purpose. If one ever
# looks necessary, the tunnel is misconfigured — that is the tell, so removing
# the temptation keeps it a loud failure rather than a quiet workaround.

# When the trial ends on 2026-10-26 this replaces the data source above and the
# instance becomes managed like everything else. Left here rather than in a
# commit message because the decision to make is visible from the config it
# affects.
#
# resource "alicloud_instance" "app" {
#   instance_name              = "bykami-app"
#   instance_type              = "ecs.e-c1m1.large"
#   image_id                   = <ubuntu 24.04 LTS, via data.alicloud_images>
#   vswitch_id                 = "vsw-t4nwwj3yx0q786ie56g1y"
#   security_groups            = [var.security_group_id]
#   key_name                   = alicloud_ecs_key_pair.deploy[0].key_pair_name
#   system_disk_category       = "cloud_essd"
#   system_disk_size           = 20
#   instance_charge_type       = "PostPaid"     # subscription is cheaper if it runs continuously
#   internet_charge_type       = "PayByTraffic"
#   internet_max_bandwidth_out = 5              # >0 assigns a public IP; 0 means none
#   deletion_protection        = true
# }
