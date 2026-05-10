variable "hcloud_token" {
  description = "Hetzner Cloud API token"
  type        = string
  sensitive   = true
}

variable "ssh_public_key_path" {
  description = "Path to the SSH public key to install on all nodes"
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "controller_server_type" {
  description = "Hetzner server type for the controller node"
  type        = string
  default     = "cx21" # 2 vCPU, 4 GB RAM
}

variable "agent_server_type" {
  description = "Hetzner server type for agent nodes"
  type        = string
  default     = "cx11" # 2 vCPU, 2 GB RAM
}

variable "controller_location" {
  description = "Hetzner datacenter location for the controller"
  type        = string
  default     = "nbg1" # Nuremberg
}

variable "agent_locations" {
  description = "Map of agent node name to Hetzner datacenter location"
  type        = map(string)
  default = {
    "hub-nbg1" = "nbg1" # Nuremberg
    "hub-fsn1" = "fsn1" # Falkenstein
    "hub-hel1" = "hel1" # Helsinki
  }
}

variable "os_image" {
  description = "OS image for all nodes"
  type        = string
  default     = "ubuntu-24.04"
}

variable "nlm_version" {
  description = "NLM release version to install on nodes (used in cloud-init)"
  type        = string
  default     = "latest"
}
