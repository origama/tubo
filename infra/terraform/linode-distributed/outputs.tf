output "relay_public_ip" {
  value       = linode_instance.relay.ip_address
  description = "Public IPv4 of the relay host"
}

output "edge_public_ip" {
  value       = linode_instance.edge.ip_address
  description = "Public IPv4 of the edge host (SSH only; ingress closed)"
}

output "service_public_ip" {
  value       = linode_instance.service.ip_address
  description = "Public IPv4 of the service host (SSH only; ingress closed)"
}

output "relay_region" {
  value = linode_instance.relay.region
}

output "edge_region" {
  value = linode_instance.edge.region
}

output "service_region" {
  value = linode_instance.service.region
}

output "relay_firewall_id" {
  value       = linode_firewall.relay.id
  description = "Linode cloud firewall attached to the relay"
}

output "edge_firewall_id" {
  value       = linode_firewall.edge.id
  description = "Linode cloud firewall attached to the edge node"
}

output "service_firewall_id" {
  value       = linode_firewall.service.id
  description = "Linode cloud firewall attached to the service node"
}

output "relay_ssh" {
  value = "ssh root@${linode_instance.relay.ip_address}"
}

output "edge_ssh" {
  value = "ssh root@${linode_instance.edge.ip_address}"
}

output "service_ssh" {
  value = "ssh root@${linode_instance.service.ip_address}"
}
