# libp2p API Tunnel Platform

Questo repository contiene l'implementazione di una piattaforma di trasporto API nativa su libp2p. L'obiettivo è permettere sia ai client che ai servizi di operare dietro NAT, supportando HTTP e streaming robusti con forte isolamento per tenant.

## 🎯 Obiettivo
Costruire un sistema self-hosted open-source basato sul protocollo P2P nativo (libp2p).

## 🏗️ Architettura Generale
Il design è "flat-first". I servizi operano dietro NAT e usano il meccanismo di tunneling libp2p, con un componente Edge Gateway facoltativo per l'ingresso HTTP legacy. Non c'è un control plane centrale di registro dei servizi, ma un controllo distribuito gestito tramite rendezvous/pubsub.

## 🛠️ Tech Stack
*   **Linguaggio Primario:** Go (per l'MVP)
*   **Networking:** libp2p Go stack
*   **Configurazione:** YAML / TOML
*   **API di Gestione:** OpenAPI

## 🧱 Struttura del Monorepo
Questo repository è organizzato come un monorepo per separare chiaramente la logica di business (pacchetti `pkg`) dai servizi esecutivi (`cmd`).

- `cmd/*`: Contiene il codice sorgente per i vari server e client (es. `control-plane`, `edge-gateway`).
- `pkg/*`: Contiene le librerie riciclabili e la logica di dominio (es. `protocol`, `forwarding`, `auth`).
- `deploy/`: Configurazione per il deployment (Docker Compose, Helm).
- `docs/`: Documentazione dell'architettura, protocollo e API.

## 🛣️ Roadmap di Implementazione (Milestones)
[Qui verrà inserita la roadmap dettagliata del progetto.]