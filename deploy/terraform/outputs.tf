output "controller_ipv4" {
  description = "Public IPv4 address of the NLM controller"
  value       = hcloud_server.controller.ipv4_address
}

output "controller_ipv6" {
  description = "Public IPv6 address of the NLM controller"
  value       = hcloud_server.controller.ipv6_address
}

output "agent_ips" {
  description = "Map of agent node name to public IPv4 address"
  value       = { for name, srv in hcloud_server.agent : name => srv.ipv4_address }
}

output "agent_ipv6s" {
  description = "Map of agent node name to public IPv6 address"
  value       = { for name, srv in hcloud_server.agent : name => srv.ipv6_address }
}

output "next_steps" {
  description = "Post-provision checklist"
  value       = <<-EOT
    1. Point your DNS A record to: ${hcloud_server.controller.ipv4_address}
    2. Copy /etc/nlm/*.env files to each node (controller.env, litestream.env, agent.env)
    3. On the controller node, run:
         make install
         systemctl enable --now nlm-litestream nlm-controller
    4. On each agent node, run:
         make install
         systemctl enable --now nlm-agent-listener nlm-agent-hub
         systemctl enable --now nlm-agent-spoke.timer
    5. Provision nodes via the admin API:
         curl -H "Authorization: Bearer $NLM_ADMIN_TOKEN" \
              -H "Content-Type: application/json" \
              -d '{"id":"hub-nbg1","role":"hub","address":"<ip>:8444"}' \
              https://<domain>/api/v1/admin/nodes
  EOT
}
