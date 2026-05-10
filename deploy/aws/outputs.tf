output "controller_ip" {
  description = "Elastic IP of the NLM controller (point your DNS A record here)"
  value       = aws_eip.controller.public_ip
}

output "hub_use1_ip" {
  description = "Elastic IP of hub-use1 (us-east-1)"
  value       = aws_eip.hub_use1.public_ip
}

output "hub_usw1_ip" {
  description = "Elastic IP of hub-usw1 (us-west-1)"
  value       = aws_eip.hub_usw1.public_ip
}

output "hub_euc1_ip" {
  description = "Elastic IP of hub-euc1 (eu-central-1)"
  value       = aws_eip.hub_euc1.public_ip
}

output "spoke_apse1_ip" {
  description = "Public IP of spoke-apse1 (ap-southeast-1 / Singapore)"
  value       = aws_instance.spoke_apse1.public_ip
}

output "spoke_apne1_ip" {
  description = "Public IP of spoke-apne1 (ap-northeast-1 / Tokyo)"
  value       = aws_instance.spoke_apne1.public_ip
}

output "spoke_sae1_ip" {
  description = "Public IP of spoke-sae1 (sa-east-1 / São Paulo)"
  value       = aws_instance.spoke_sae1.public_ip
}

output "litestream_access_key_id" {
  description = "AWS Access Key ID for the Litestream IAM user"
  value       = aws_iam_access_key.litestream.id
}

output "litestream_secret_access_key" {
  description = "AWS Secret Access Key for the Litestream IAM user"
  value       = aws_iam_access_key.litestream.secret
  sensitive   = true
}

output "controller_url" {
  description = "HTTPS URL of the controller (requires DNS to be pointed at controller_ip)"
  value       = "https://${var.controller_domain}"
}

output "node_summary" {
  description = "All nodes with their IPs and roles — paste into scripts/deploy.sh"
  value = {
    controller = aws_eip.controller.public_ip
    hub_use1   = aws_eip.hub_use1.public_ip
    hub_usw1   = aws_eip.hub_usw1.public_ip
    hub_euc1   = aws_eip.hub_euc1.public_ip
    spoke_apse1 = aws_instance.spoke_apse1.public_ip
    spoke_apne1 = aws_instance.spoke_apne1.public_ip
    spoke_sae1  = aws_instance.spoke_sae1.public_ip
  }
}
