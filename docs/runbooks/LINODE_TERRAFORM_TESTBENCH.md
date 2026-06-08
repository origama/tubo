# Linode Terraform Testbench

This document describes the new distributed Linode testbench created with Terraform.

## Goal

Create 3 machines in different regions:

- public `relay` exposed on `tcp/4001`
- `edge` managed over SSH but closed to inbound application/libp2p traffic
- `service` managed over SSH but closed to inbound application/libp2p traffic

## Why edge/service are "NAT-like"

With only 3 Linodes in different regions, a real NAT setup would require extra gateways or a more complex topology.

For this bench, what we really care about is enforcing these constraints:

- `edge` and `service` must not be directly dialable from the outside on the libp2p path;
- the relay must be the only publicly reachable static peer;
- tunnel traffic must go through the relay.

For this reason, the bench uses host-level firewalls (`ufw`) and `force_reachability: private` on edge/service.

## Layout

Terraform stack:

- `infra/terraform/linode-distributed/`

Smoke harness:

- `tests/smoke-terraform-linode.sh`
- `tests/smoke-terraform-linode-mixed-version.sh`

## Expected workflow

1. prepare `terraform.tfvars`
2. `terraform init`
3. `terraform apply`
4. run `./tests/smoke-terraform-linode.sh`
5. verify end-to-end response and `connection_path=relayed`
6. `terraform destroy` when the test is complete

## What the smoke test does

The base smoke test:

1. builds `tubo` and `dummy-api-server` locally;
2. generates an ephemeral PSK;
3. reads IPs from `terraform output`;
4. uploads binaries, swarm key, and YAML config to the three nodes;
5. starts:
   - `relay` on the relay node
   - `edge` on the edge node
   - `dummy-api-server` + `service` on the service node
6. queries `edge` through local SSH access to the edge node;
7. verifies in edge logs that the path is relayed.

## Practical note

Because edge is intentionally closed to inbound traffic, the test request is executed **from inside the edge host over SSH**, not from the public Internet.

This is consistent with the goal of the bench: testing the distributed relay-first data plane, not public exposure of edge ingress.

## Current-protocol smoke

The real bench also exercises the current protocol path with:

- `tests/smoke-terraform-linode-mixed-version.sh`

The script validates current edge -> current service (`/p2p-tunnel/1.1` with hello handshake) and queries protocol debug/admin endpoints to collect evidence of the active negotiation.

## Files to update once the PAT is available

- `infra/terraform/linode-distributed/terraform.tfvars`

Required values:

- `linode_token`
- `root_pass`
- `ssh_public_key`
- `ssh_private_key_path`
- desired regions
