locals {
  trial = data.alicloud_instances.trial.instances[0]
}

# Feeds the Ansible inventory. Empty whenever the instance is stopped: a
# pay-by-traffic public IP is auto-assigned at start and released at stop, so it
# is not a stable address and nothing should be pinned to it. Deploys reach the
# box through the tunnel instead; this is for bootstrap and rescue.
output "ssh_host" {
  description = "Current public IP, or empty if stopped."
  value       = local.trial.public_ip
}

output "status" {
  value = local.trial.status
}

# Surfaced because the trial ships CentOS 7.9, which went end-of-life on
# 2024-06-30 — no security updates, repositories moved to vault, and every
# Ansible role in the design written against apt. Replace the system disk with
# Ubuntu 24.04 before configuring anything. Once that is done this output stops
# being interesting, which is the point: it is here to be checked, not kept.
output "image_id" {
  description = "Expect ubuntu_24_04. A centos_7_9 value means the box has not been re-imaged yet."
  value       = local.trial.image_id
}

output "inbound_ssh_open_to" {
  description = "Bootstrap SSH exposure. Empty is the intended steady state."
  value       = var.ssh_admin_cidr
}
