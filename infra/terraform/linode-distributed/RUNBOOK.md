# Linode distributed testbench runbook

## Prerequisites

- Terraform stack applied in `infra/terraform/linode-distributed`
- Linode token available in `~/.token`
- working SSH access to the 3 nodes

Export the token for Terraform:

```bash
export TF_VAR_linode_token="$(< ~/.token)"
```

## Terraform

### Plan

```bash
cd /root/tubo/infra/terraform/linode-distributed
terraform plan
```

### Apply

```bash
cd /root/tubo/infra/terraform/linode-distributed
terraform apply
```

### Useful outputs

```bash
terraform output relay_public_ip
terraform output edge_public_ip
terraform output service_public_ip
terraform output relay_ssh
terraform output edge_ssh
terraform output service_ssh
terraform output relay_firewall_id
terraform output edge_firewall_id
terraform output service_firewall_id
```

## Current nodes

- relay: `172.104.128.174`
- edge: `45.79.168.161`
- service: `172.104.190.233`

## Distributed smoke test

```bash
cd /root/tubo
./tests/smoke-terraform-linode.sh
```

The smoke:

- builds `tubo` and `dummy-api-server`
- generates a temporary swarm key
- uploads binaries and YAML to the nodes
- starts relay / edge / service / dummy origin
- verifies health, discovery, route, and `connection_path=relayed`

To leave remote processes running:

```bash
cd /root/tubo
KEEP_RUNNING=1 ./tests/smoke-terraform-linode.sh
```

## Persistent benchmarks with comparable results

Runs the setup on the Linode/Terraform testbed, executes the load scenarios, and saves:

- `tests/perf/results/linode-terraform/<timestamp>/report.json`
- `tests/perf/results/linode-terraform/<timestamp>/summary.md`
- `tests/perf/results/linode-terraform/latest.json`
- `tests/perf/results/linode-terraform/latest.md`

Command:

```bash
cd /root/tubo
python3 ./tests/perf/run_linode_terraform_perf.py
```

## Quick SSH

```bash
ssh root@172.104.128.174
ssh root@45.79.168.161
ssh root@172.104.190.233
```

## Remote logs

### Relay

```bash
ssh root@172.104.128.174 'tail -n 200 /var/log/tubo/relay.log'
```

### Edge

```bash
ssh root@45.79.168.161 'tail -n 200 /var/log/tubo/edge.log'
```

### Service

```bash
ssh root@172.104.190.233 'tail -n 200 /var/log/tubo/service.log'
```

### Dummy API

```bash
ssh root@172.104.190.233 'tail -n 200 /var/log/tubo/dummy-api-server.log'
```

## Remote health checks

### Relay

```bash
ssh root@172.104.128.174 'curl -fsS http://127.0.0.1:8092/healthz'
```

### Edge

```bash
ssh root@45.79.168.161 'curl -fsS http://127.0.0.1:8443/healthz'
ssh root@45.79.168.161 'curl -fsS http://127.0.0.1:8444/healthz'
ssh root@45.79.168.161 'curl -fsS http://127.0.0.1:8444/services'
ssh root@45.79.168.161 'curl -fsS http://127.0.0.1:8444/routes'
```

### Service

```bash
ssh root@172.104.190.233 'curl -fsS http://127.0.0.1:8091/healthz'
```

## Verify the relay path

```bash
ssh root@45.79.168.161 "grep 'connection_path=relayed' /var/log/tubo/edge.log"
```

## Stop remote processes

```bash
for host in 172.104.128.174 45.79.168.161 172.104.190.233; do
  ssh root@$host '
    for name in relay edge service dummy-api-server; do
      if [ -f "/var/run/tubo/$name.pid" ]; then
        kill "$(cat /var/run/tubo/$name.pid)" 2>/dev/null || true
        rm -f "/var/run/tubo/$name.pid"
      fi
    done
  '
done
```

## Destroy the infrastructure

```bash
cd /root/tubo/infra/terraform/linode-distributed
terraform destroy
```

## Operational notes

- Terraform only manages 3 Linodes and 3 Linode Cloud Firewalls.
- Runtime provisioning no longer goes through Terraform.
- Cloud firewall rules are aligned with `rwct-fw`, with `4001/tcp` open only on the relay.
- Benchmarks save historical results and a `latest.*` snapshot for quick comparison between runs.
