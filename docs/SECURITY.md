# Security Policy & Design Constraints

Questo documento stabilisce i requisiti di sicurezza e le restrizioni architetturali fondamentali per il libp2p API Tunnel Platform, garantendo che la piattaforma sia robusta contro attacchi comuni in ambienti P2P distribuiti.

## 🔒 Principali Principi di Sicurezza
1.  **Zero Trust:** Nessun nodo (Edge, Connector, Control Plane) viene fidato automaticamente. Ogni interazione deve essere verificata e autorizzata.
2.  **Principio del Minimo Privilegio:** I processi operativi (`Connector Agent`) dovrebbero avere il minor set di permessi possibile sul sistema host.
3.  **Inbound Portless Design:** L'architettura è progettata per non richiedere porte inbound esposte sui servizi interni (Origin Services).

## 🔐 Meccanismi di Sicurezza Implementati
### Autenticazione e Autorizzazione (AuthN/AuthZ)
*   **Peer Signatures:** Tutte le comunicazioni chiave, in particolare gli annunci di servizio (`ServiceRegistration`), devono essere accompagnate da firme crittografiche del Peer ID. Questo garantisce l'autenticità dell'annunciante.
*   **Tenant Isolation:** L'identificatore `tenant_id` (presente nel TunnelFrame) è il primo punto di controllo per l'autorizzazione, isolando logicamente i carichi di lavoro e prevenendo cross-talk tra clienti/servizi diversi.
*   **Policy Enforcement:** Il Control Plane sarà responsabile della definizione e applicazione delle politiche di accesso (`authz`).

### Protezione contro Attacchi P2P Comuni
*   **Lease / Heartbeat Expiry:** I record di servizio hanno una durata limitata (TTL). Se un Connector smette di inviare heartbeat, il suo annuncio viene automaticamente revocato dal sistema di Discovery.
*   **Rate Limiting & Quotas:** L'Edge Gateway e il Control Plane implementeranno limiti sul numero di richieste per peer/tenant per prevenire Denial-of-Service (DoS).
*   **Replay Protection:** Utilizzo di sequenziatori monotonici o timestamp crittografati sui messaggi di stato critici.

## 🛡️ Rischi Specifici da Affrontare
1.  **Compromissione del Connector:** Se un Attaccante riesce a compromettere un Connector, questo può agire come punto di ingresso malevolo al servizio locale (Origin Service). È cruciale isolare strettamente l'accesso al `localhost` e implementare autenticazione end-to-end.
2.  **Masquerading:** L'attacco in cui un peer si spaccia per un altro. Questo viene mitigato dall'uso rigoroso di firme crittografiche basate sul Peer ID noto.

## 📦 Stack Tecnologico e Dipendenze
*   **Crittografia:** Utilizzo delle primitive crittografiche standard del libp2p Go stack (Curve, Noise/TLS).
*   **Key Management:** Si prevede l'integrazione di un sistema di gestione delle chiavi sicuro (es. Vault) nelle fasi successive (Milestone 4).