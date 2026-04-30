# Linode multi-region distributed testbench

Questo stack Terraform crea 3 Linode in **region diverse**:

- `relay` pubblico
- `edge` con ingress chiuso (SSH only, NAT-like)
- `service` con ingress chiuso (SSH only, NAT-like)

## Importante: NAT reale vs NAT-like

Con soli 3 Linode in region diverse, fare un NAT "vero" per edge e service senza introdurre gateway aggiuntivi per regione e' scomodo e poco utile per questo progetto.

Per questo bench usiamo un approccio **NAT-like**:

- `edge` e `service` hanno IP pubblici Linode per la gestione SSH;
- ma il loro ingress applicativo/libp2p viene chiuso dal **Linode Cloud Firewall**;
- `relay` resta l'unico nodo apertamente raggiungibile su `tcp/4001`.

Questo forza un comportamento molto vicino a un deployment dietro NAT per il nostro caso di test:

- niente direct dial inbound affidabile verso `edge`/`service`;
- discovery e data plane devono appoggiarsi al relay pubblico;
- il bench verifica il percorso `connection_path=relayed`.

## Cosa crea Terraform

Terraform:

- crea 3 VM Linode;
- crea 3 **Linode Cloud Firewall** dedicati e li associa alle VM;
- usa regole cloud firewall allineate a `rwct-fw` per allowlist SSH/proxy/ICMP;
- mantiene `relay` (`tcp/4001`) **solo sul nodo relay**;
- non esegue piu' bootstrap via Terraform; il provisioning runtime e' demandato allo smoke script.

Il deploy di `tubo`, delle config YAML e dello smoke vero e proprio e' fatto da:

- `tests/smoke-terraform-linode.sh`

## File principali

- `versions.tf`
- `variables.tf`
- `main.tf`
- `outputs.tf`
- `terraform.tfvars.example`
- `../../../tests/smoke-terraform-linode.sh`

## Uso

1. Copia i vars:

```bash
cd infra/terraform/linode-distributed
cp terraform.tfvars.example terraform.tfvars
```

2. Inserisci:

- `root_pass`
- `ssh_public_key`
- `ssh_private_key_path` (**path assoluto**, ad esempio `/root/.ssh/id_ed25519`)
- region desiderate
- opzionalmente gli override dei CIDR se vuoi divergere in futuro dalle regole attuali di `rwct-fw`

3. Esporta il token senza salvarlo nel file:

```bash
export TF_VAR_linode_token="$(< ~/.token)"
```

4. Applica:

```bash
terraform init
terraform apply
```

5. Lancia lo smoke:

```bash
cd /root/p2p-api-tunnel
./tests/smoke-terraform-linode.sh
```

## Output utili

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

## Limiti attuali

- non usa ancora NodeBalancers/VPC/gateway dedicati;
- non crea systemd unit permanenti: lo smoke usa processi remoti `nohup`;
- la validazione reale richiede un Linode PAT e una chiave SSH funzionante.
