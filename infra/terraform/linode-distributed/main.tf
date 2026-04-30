provider "linode" {
  token = var.linode_token
}

locals {
  name_prefix = "${var.project}-${var.environment}"
  common_tags = concat([
    var.project,
    var.environment,
    "distributed-testbench",
    "terraform",
  ], var.extra_tags)

  boot_prep = <<-EOT
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    apt-get install -y curl
    mkdir -p /opt/p2p-api-tunnel/bin /etc/tubo /var/log/tubo /var/run/p2p-api-tunnel
  EOT
}

resource "linode_instance" "relay" {
  label           = "${local.name_prefix}-relay"
  image           = var.image
  region          = var.relay_region
  type            = var.instance_type
  root_pass       = var.root_pass
  authorized_keys = [trimspace(var.ssh_public_key)]
  tags            = concat(local.common_tags, ["relay", "public"])
}

resource "linode_instance" "edge" {
  label           = "${local.name_prefix}-edge"
  image           = var.image
  region          = var.edge_region
  type            = var.instance_type
  root_pass       = var.root_pass
  authorized_keys = [trimspace(var.ssh_public_key)]
  tags            = concat(local.common_tags, ["edge", "nat-like"])
}

resource "linode_instance" "service" {
  label           = "${local.name_prefix}-service"
  image           = var.image
  region          = var.service_region
  type            = var.instance_type
  root_pass       = var.root_pass
  authorized_keys = [trimspace(var.ssh_public_key)]
  tags            = concat(local.common_tags, ["service", "nat-like"])
}

resource "linode_firewall" "relay" {
  label           = "${var.project}-relay-fw"
  inbound_policy  = "DROP"
  outbound_policy = "ACCEPT"
  linodes         = toset([tonumber(linode_instance.relay.id)])
  tags            = local.common_tags

  inbound {
    label       = "accept-inbound-icmp"
    action      = "ACCEPT"
    protocol    = "ICMP"
    ipv4        = ["0.0.0.0/0"]
    ipv6        = ["::/0"]
    description = "Accept inbound ICMP"
  }

  inbound {
    label       = "accept-inbound-tcp-ssh-proxies"
    action      = "ACCEPT"
    protocol    = "TCP"
    ports       = "22,443"
    ipv4        = var.ssh_proxy_ipv4_cidrs
    ipv6        = var.ssh_proxy_ipv6_cidrs
    description = "Accept inbound TCP from SSH proxies"
  }

  inbound {
    label       = "accept-inbound-tcp-eaa-proxies"
    action      = "ACCEPT"
    protocol    = "TCP"
    ports       = "22, 443"
    ipv4        = var.eaa_proxy_ipv4_cidrs
    description = "Accept inbound TCP from EAA proxies"
  }

  inbound {
    label    = "accept-inbound-SSH-myself"
    action   = "ACCEPT"
    protocol = "TCP"
    ports    = "22"
    ipv4     = var.ssh_myself_ipv4_cidrs
  }

  inbound {
    label    = "relay"
    action   = "ACCEPT"
    protocol = "TCP"
    ports    = "4001"
    ipv4     = ["0.0.0.0/0"]
  }
}

resource "linode_firewall" "edge" {
  label           = "${var.project}-edge-fw"
  inbound_policy  = "DROP"
  outbound_policy = "ACCEPT"
  linodes         = toset([tonumber(linode_instance.edge.id)])
  tags            = local.common_tags

  inbound {
    label       = "accept-inbound-icmp"
    action      = "ACCEPT"
    protocol    = "ICMP"
    ipv4        = ["0.0.0.0/0"]
    ipv6        = ["::/0"]
    description = "Accept inbound ICMP"
  }

  inbound {
    label       = "accept-inbound-tcp-ssh-proxies"
    action      = "ACCEPT"
    protocol    = "TCP"
    ports       = "22,443"
    ipv4        = var.ssh_proxy_ipv4_cidrs
    ipv6        = var.ssh_proxy_ipv6_cidrs
    description = "Accept inbound TCP from SSH proxies"
  }

  inbound {
    label       = "accept-inbound-tcp-eaa-proxies"
    action      = "ACCEPT"
    protocol    = "TCP"
    ports       = "22, 443"
    ipv4        = var.eaa_proxy_ipv4_cidrs
    description = "Accept inbound TCP from EAA proxies"
  }

  inbound {
    label    = "accept-inbound-SSH-myself"
    action   = "ACCEPT"
    protocol = "TCP"
    ports    = "22"
    ipv4     = var.ssh_myself_ipv4_cidrs
  }
}

resource "linode_firewall" "service" {
  label           = "${var.project}-service-fw"
  inbound_policy  = "DROP"
  outbound_policy = "ACCEPT"
  linodes         = toset([tonumber(linode_instance.service.id)])
  tags            = local.common_tags

  inbound {
    label       = "accept-inbound-icmp"
    action      = "ACCEPT"
    protocol    = "ICMP"
    ipv4        = ["0.0.0.0/0"]
    ipv6        = ["::/0"]
    description = "Accept inbound ICMP"
  }

  inbound {
    label       = "accept-inbound-tcp-ssh-proxies"
    action      = "ACCEPT"
    protocol    = "TCP"
    ports       = "22,443"
    ipv4        = var.ssh_proxy_ipv4_cidrs
    ipv6        = var.ssh_proxy_ipv6_cidrs
    description = "Accept inbound TCP from SSH proxies"
  }

  inbound {
    label       = "accept-inbound-tcp-eaa-proxies"
    action      = "ACCEPT"
    protocol    = "TCP"
    ports       = "22, 443"
    ipv4        = var.eaa_proxy_ipv4_cidrs
    description = "Accept inbound TCP from EAA proxies"
  }

  inbound {
    label    = "accept-inbound-SSH-myself"
    action   = "ACCEPT"
    protocol = "TCP"
    ports    = "22"
    ipv4     = var.ssh_myself_ipv4_cidrs
  }
}

