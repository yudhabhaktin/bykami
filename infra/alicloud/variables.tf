variable "region" {
  description = "Alibaba Cloud region. Singapore, not Jakarta — see design/infrastructure.md."
  type        = string
  default     = "ap-southeast-1"
}

variable "instance_id" {
  description = "The free-trial ECS instance, created in the console on 2026-07-26."
  type        = string
  default     = "i-t4n7sxn0wwzevsimxwnr"
}

variable "security_group_id" {
  description = "Security group the trial flow created alongside the instance."
  type        = string
  default     = "sg-t4n7sxn0wwzevsiowbta"
}

variable "ssh_public_key" {
  description = "Public half of the deploy key, RSA — Alibaba rejects ed25519 on import. Empty creates no key pair, which is what lets CI plan this stack with no secret beyond the API credentials."
  type        = string
  default     = ""

  validation {
    condition     = var.ssh_public_key == "" || startswith(var.ssh_public_key, "ssh-rsa ")
    error_message = "Alibaba ECS key pairs accept RSA only. Generate with: ssh-keygen -t rsa -b 2048"
  }
}

# Set by CI from TF_VAR_*. Empty locally, which switches the provider back to a
# static key or an ~/.aliyun profile. None of these three is a secret — an ARN
# names a role, it does not grant it — but the account ID inside them is not
# worth publishing in a public repo's logs either, so CI passes them as secrets.
variable "oidc_provider_arn" {
  description = "acs:ram::<account>:oidc-provider/<name>"
  type        = string
  default     = ""
}

variable "oidc_role_arn" {
  description = "Role CI assumes. Empty disables OIDC entirely."
  type        = string
  default     = ""
}

variable "oidc_token_file" {
  description = "Path the runner wrote the GitHub OIDC token to."
  type        = string
  default     = ""
}

variable "ssh_admin_cidr" {
  description = "Address allowed to reach port 22, e.g. 203.0.113.4/32. Empty means no inbound SSH rule, which is the intended steady state once the tunnel is up."
  type        = string
  default     = ""
}
