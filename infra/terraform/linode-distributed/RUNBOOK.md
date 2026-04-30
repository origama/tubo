# Linode distributed testbench runbook

## Prerequisiti

- Terraform stack applicato in `infra/terraform/linode-distributed`
- token Linode disponibile in `~/.token`
- accesso SSH funzionante ai 3 nodi

Esporta il token per Terraform:

```bash
export TF_VAR_linode_token="$(< ~/.token)"
```

## Terraform

### Plan

```bash
cd /root/p2p-api-tunnel/infra/terraform/linode-distributed
terraform plan
```

### Apply

```bash
cd /root/p2p-api-tunnel/infra/terraform/linode-distributed
terraform apply
```

### Output utili

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

## Nodi correnti

- relay: `172.104.128.174`
- edge: `45.79.168.161`
- service: `172.104.190.233`

## Smoke test distribuito

```bash
cd /root/p2p-api-tunnel
./tests/smoke-terraform-linode.sh
```

Lo smoke:

- builda `tubo` e `dummy-api-server`
- genera una swarm key temporanea
- carica binari e YAML sui nodi
- avvia relay / edge / service / dummy origin
- verifica health, discovery, route e `connection_path=relayed`

Per lasciare i processi remoti in esecuzione:

```bash
cd /root/p2p-api-tunnel
KEEP_RUNNING=1 ./tests/smoke-terraform-linode.sh
```

## Benchmark persistenti con risultati confrontabili

Esegue il setup sul testbed Linode/Terraform, lancia gli scenari di carico e salva:

- `tests/perf/results/linode-terraform/<timestamp>/report.json`
- `tests/perf/results/linode-terraform/<timestamp>/summary.md`
- `tests/perf/results/linode-terraform/latest.json`
- `tests/perf/results/linode-terraform/latest.md`

Comando:

```bash
cd /root/p2p-api-tunnel
python3 ./tests/perf/run_linode_terraform_perf.py
```

## SSH rapido

```bash
ssh root@172.104.128.174
ssh root@45.79.168.161
ssh root@172.104.190.233
```

## Log remoti

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

## Health checks remoti

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

## Verifica relay path

```bash
ssh root@45.79.168.161 "grep 'connection_path=relayed' /var/log/tubo/edge.log"
```

## Stop processi remoti

```bash
for host in 172.104.128.174 45.79.168.161 172.104.190.233; do
  ssh root@$host '
    for name in relay edge service dummy-api-server; do
      if [ -f "/var/run/p2p-api-tunnel/$name.pid" ]; then
        kill "$(cat /var/run/p2p-api-tunnel/$name.pid)" 2>/dev/null || true
        rm -f "/var/run/p2p-api-tunnel/$name.pid"
      fi
    done
  '
done
```

## Destroy infrastruttura

```bash
cd /root/p2p-api-tunnel/infra/terraform/linode-distributed
terraform destroy
```

## Note operative

- Terraform gestisce solo 3 Linode e 3 Linode Cloud Firewall.
- Il provisioning runtime non passa piu' da Terraform.
- Le regole cloud firewall sono allineate a `rwct-fw`, con `4001/tcp` aperta solo sul relay.
- I benchmark salvano risultati storici e un `latest.*` per confronto rapido tra run successivi.
