terraform {
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.49"
    }
  }
  required_version = ">= 1.5"

  # Uncomment to use remote state (recommended for production):
  # backend "s3" {
  #   bucket = "nlm-terraform-state"
  #   key    = "nlm/terraform.tfstate"
  #   region = "eu-central-1"
  # }
}

provider "hcloud" {
  token = var.hcloud_token
}

# ── SSH key ───────────────────────────────────────────────────────────────────

resource "hcloud_ssh_key" "nlm" {
  name       = "nlm-deploy"
  public_key = file(var.ssh_public_key_path)
}

# ── Firewalls ─────────────────────────────────────────────────────────────────

resource "hcloud_firewall" "controller" {
  name = "nlm-controller"

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_firewall" "agent" {
  name = "nlm-agent"

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # TCP listener — reachable by all nodes in this deployment.
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "8444"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

# ── Controller ────────────────────────────────────────────────────────────────

resource "hcloud_server" "controller" {
  name         = "nlm-controller"
  image        = var.os_image
  server_type  = var.controller_server_type
  location     = var.controller_location
  ssh_keys     = [hcloud_ssh_key.nlm.id]
  firewall_ids = [hcloud_firewall.controller.id]

  user_data = templatefile("${path.module}/user_data.tpl", {
    role        = "controller"
    nlm_version = var.nlm_version
  })

  labels = {
    app  = "nlm"
    role = "controller"
  }
}

# ── Agent nodes ───────────────────────────────────────────────────────────────

resource "hcloud_server" "agent" {
  for_each = var.agent_locations

  name         = each.key
  image        = var.os_image
  server_type  = var.agent_server_type
  location     = each.value
  ssh_keys     = [hcloud_ssh_key.nlm.id]
  firewall_ids = [hcloud_firewall.agent.id]

  user_data = templatefile("${path.module}/user_data.tpl", {
    role        = "agent"
    nlm_version = var.nlm_version
  })

  labels = {
    app  = "nlm"
    role = "agent"
  }
}
