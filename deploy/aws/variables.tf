variable "ssh_public_key_path" {
  description = "Path to the SSH public key to install on all nodes"
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "admin_token" {
  description = "NLM admin token (protects /api/v1/admin/* and the web UI login)"
  type        = string
  sensitive   = true
}

variable "litestream_s3_bucket" {
  description = "S3 bucket name for Litestream DB replication (must be globally unique)"
  type        = string
}

variable "controller_domain" {
  description = "Fully-qualified domain name pointing to the controller Elastic IP (Caddy uses this for TLS)"
  type        = string
}

variable "acme_email" {
  description = "Email address for Let's Encrypt certificate notifications"
  type        = string
}
