variable "linode_token" {
  description = "Linode API token"
  type        = string
  sensitive   = true
}

variable "project" {
  description = "Project/name prefix used for Linode labels and tags"
  type        = string
  default     = "tubo"
}

variable "environment" {
  description = "Environment suffix"
  type        = string
  default     = "testbench"
}

variable "image" {
  description = "Linode image"
  type        = string
  default     = "linode/ubuntu24.04"
}

variable "instance_type" {
  description = "Linode plan/type for all nodes"
  type        = string
  default     = "g6-nanode-1"
}

variable "relay_region" {
  description = "Region for the public relay"
  type        = string
  default     = "eu-central"
}

variable "edge_region" {
  description = "Region for the edge host"
  type        = string
  default     = "us-east"
}

variable "service_region" {
  description = "Region for the service host"
  type        = string
  default     = "ap-south"
}

variable "root_pass" {
  description = "Initial root password required by Linode"
  type        = string
  sensitive   = true
}

variable "ssh_public_key" {
  description = "SSH public key content that will be installed on all nodes"
  type        = string
}

variable "ssh_private_key_path" {
  description = "Local path to the SSH private key matching ssh_public_key; used by Terraform provisioners"
  type        = string
}

variable "ssh_allowed_cidrs" {
  description = "Deprecated override from the earlier setup; kept for compatibility but not used by the cloud firewall rules"
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "ssh_proxy_ipv4_cidrs" {
  description = "IPv4 CIDRs for SSH proxy ingress, mirroring rwct-fw"
  type        = list(string)
  default = [
    "172.236.119.4/30",
    "172.234.160.4/30",
    "172.236.94.4/30",
  ]
}

variable "ssh_proxy_ipv6_cidrs" {
  description = "IPv6 CIDRs for SSH proxy ingress, mirroring rwct-fw"
  type        = list(string)
  default = [
    "2600:3c06::f03c:94ff:febe:162f/128",
    "2600:3c06::f03c:94ff:febe:16ff/128",
    "2600:3c06::f03c:94ff:febe:16c5/128",
    "2600:3c07::f03c:94ff:febe:16e6/128",
    "2600:3c07::f03c:94ff:febe:168c/128",
    "2600:3c07::f03c:94ff:febe:16de/128",
    "2600:3c08::f03c:94ff:febe:16e9/128",
    "2600:3c08::f03c:94ff:febe:1655/128",
    "2600:3c08::f03c:94ff:febe:16fd/128",
  ]
}

variable "eaa_proxy_ipv4_cidrs" {
  description = "IPv4 CIDRs for EAA proxy ingress, mirroring rwct-fw"
  type        = list(string)
  default = [
    "139.144.212.168/31",
    "172.232.23.164/31",
    "95.255.219.149/32",
    "95.255.219.149/32",
  ]
}

variable "ssh_myself_ipv4_cidrs" {
  description = "IPv4 addresses for the operator SSH allowlist, mirroring rwct-fw"
  type        = list(string)
  default = [
    "188.217.147.158/32",
    "172.236.202.99/32",
    "172.232.189.160/32",
    "2.43.175.107/32",
  ]
}

variable "relay_allowed_cidrs" {
  description = "Deprecated override from the earlier setup; kept for compatibility but not used by the cloud firewall rules"
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "extra_tags" {
  description = "Extra tags to add to Linodes and Linode firewalls"
  type        = list(string)
  default     = []
}
