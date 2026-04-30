# Linode Terraform Testbench

Questo documento descrive il nuovo testbench distribuito su Linode creato con Terraform.

## Obiettivo

Creare 3 macchine in region diverse:

- `relay` pubblico e aperto su `tcp/4001`
- `edge` gestito via SSH ma chiuso in ingresso applicativo/libp2p
- `service` gestito via SSH ma chiuso in ingresso applicativo/libp2p

## Perche' edge/service sono "NAT-like"

Con soli 3 Linode in region diverse, un NAT reale richiederebbe gateway addizionali o una topologia piu' complessa.

Per il bench, quello che ci interessa davvero e' forzare questi vincoli:

- `edge` e `service` non devono essere dialabili direttamente dall'esterno sul path libp2p;
- il relay deve essere l'unico peer statico pubblicamente raggiungibile;
- il traffico tunnel deve andare via relay.

Per questo il bench usa firewall host-level (`ufw`) e `force_reachability: private` su edge/service.

## Layout

Terraform stack:

- `infra/terraform/linode-distributed/`

Smoke harness:

- `tests/smoke-terraform-linode.sh`

## Workflow atteso

1. preparare `terraform.tfvars`
2. `terraform init`
3. `terraform apply`
4. eseguire `./tests/smoke-terraform-linode.sh`
5. verificare risposta end-to-end e `connection_path=relayed`
6. `terraform destroy` a fine test

## Cosa fa lo smoke

Lo smoke:

1. compila `tubo` e `dummy-api-server` in locale;
2. genera una PSK effimera;
3. legge gli IP da `terraform output`;
4. carica binari, swarm key e config YAML sui tre nodi;
5. avvia:
   - `relay` sul nodo relay
   - `edge` sul nodo edge
   - `dummy-api-server` + `service` sul nodo service
6. interroga `edge` tramite SSH locale al nodo edge;
7. verifica nei log dell'edge che il percorso sia relayed.

## Nota pratica

Poiche' l'edge e' volutamente chiuso in ingresso, la richiesta di test viene eseguita **dall'interno dell'host edge via SSH**, non da Internet pubblica.

Questo e' coerente con l'obiettivo del bench: testare il piano dati distribuito relay-first, non l'esposizione pubblica dell'ingress edge.

## File da toccare quando avremo il PAT

- `infra/terraform/linode-distributed/terraform.tfvars`

Valori necessari:

- `linode_token`
- `root_pass`
- `ssh_public_key`
- `ssh_private_key_path`
- region desiderate
