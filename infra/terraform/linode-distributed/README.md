# Linode multi-region distributed testbench

This Terraform stack creates 3 Linodes in **different regions**:

- public `relay`
- `edge` with closed ingress (SSH only, NAT-like)
- `service` with closed ingress (SSH only, NAT-like)

## Important: real NAT vs NAT-like

With only 3 Linodes in different regions, building a "real" NAT setup for edge and service without adding extra regional gateways is awkward and not very useful for this project.

For this bench we use a **NAT-like** approach:

- `edge` and `service` have public Linode IPs for SSH management;
- but their application/libp2p ingress is closed by the **Linode Cloud Firewall**;
- `relay` remains the only node openly reachable on `tcp/4001`.

This forces behavior very close to a behind-NAT deployment for our test case:

- no reliable inbound direct dial to `edge`/`service`;
- discovery and data plane must rely on the public relay;
- the bench verifies the `connection_path=relayed` path.

## What Terraform creates

Terraform:

- creates 3 Linode VMs;
- creates 3 dedicated **Linode Cloud Firewalls** and attaches them to the VMs;
- uses cloud firewall rules aligned with `rwct-fw` for SSH/proxy/ICMP allowlisting;
- keeps `relay` (`tcp/4001`) exposed **only on the relay node**;
- no longer performs bootstrap via Terraform; runtime provisioning is delegated to the smoke script.

Deployment of `tubo`, YAML configs, and the actual smoke run is handled by:

- `tests/smoke-terraform-linode.sh`

## Main files

- `versions.tf`
- `variables.tf`
- `main.tf`
- `outputs.tf`
- `terraform.tfvars.example`
- `../../../tests/smoke-terraform-linode.sh`

## Usage

1. Copy the vars:

```bash
cd infra/terraform/linode-distributed
cp terraform.tfvars.example terraform.tfvars
```

2. Fill in:

- `root_pass`
- `ssh_public_key`
- `ssh_private_key_path` (**absolute path**, for example `/root/.ssh/id_ed25519`)
- desired regions
- optionally CIDR overrides if you want to diverge in the future from the current `rwct-fw` rules

3. Export the token without saving it in the file:

```bash
export TF_VAR_linode_token="$(< ~/.token)"
```

4. Apply:

```bash
terraform init
terraform apply
```

5. Run the smoke test:

```bash
cd /root/tubo
./tests/smoke-terraform-linode.sh
```

## Useful outputs

```bash
terraform output relay_public_ip
terraform output edge_public_ip
terraform output service_public_ip
terraform output relay_firewall_id
terraform output edge_firewall_id
terraform output service_firewall_id
terraform output relay_ssh
terraform output edge_ssh
terraform output service_ssh
```

## Destroy

```bash
terraform destroy
```

## Current limits

- it does not yet use NodeBalancers/VPC/dedicated gateways;
- it does not create permanent systemd units: the smoke uses remote `nohup` processes;
- real validation requires a Linode PAT and a working SSH key.
