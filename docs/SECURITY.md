# Security Policy & Design Constraints

Questo documento stabilisce i requisiti di sicurezza e le restrizioni architetturali fondamentali per il libp2p API Tunnel Platform, garantendo che la piattaforma sia robusta contro attacchi comuni in ambienti P2P distribuiti.

## 🔒 Principali Principi di Sicurezza

1. **Zero Trust:** Nessun nodo (Edge Gateway, Connector) viene fidato automaticamente. Ogni interazione deve essere verificata e autorizzata.
2. **Principio del Minimo Privilegio:** I processi operativi (`Connector Agent`) dovrebbero avere il minor set di permessi possibile sul sistema host.
3. **Inbound Portless Design:** L'architettura è progettata per non richiedere porte inbound esposte sui servizi interni (Origin Services).

## 🔐 Meccanismi di Sicurezza Implementati

### Autenticazione e Autorizzazione (AuthN/AuthZ)

* **Peer Signatures:** Tutte le comunicazioni chiave, in particolare gli annunci di servizio (`ServiceRegistration`), devono essere accompagnate da firme crittografiche del Peer ID. Questo garantisce l'autenticità dell'annunciante.
* **Tenant Isolation:** L'identificatore `tenant_id` (presente nel TunnelFrame) è il primo punto di controllo per l'autorizzazione, isolando logicamente i carichi di lavoro e prevenendo cross-talk tra clienti/servizi diversi.
* **Policy Enforcement:** Le politiche di accesso (`authz`) sono definite localmente su ogni Edge Gateway e validate in modo distribuito. Ogni Edge Gateway mantiene una policy locale che determina quali servizi/tenant può raggiungere.

### Protezione contro Attacchi P2P Comuni

* **Lease / Heartbeat Expiry:** I record di servizio hanno una durata limitata (TTL). Se un Connector smette di inviare heartbeat, il suo annuncio viene automaticamente revocato dal sistema di Discovery.
* **Rate Limiting & Quotas:** L'Edge Gateway implementa limiti sul numero di richieste per peer/tenant per prevenire Denial-of-Service (DoS).
* **Replay Protection:** Utilizzo di sequenziatori monotonici o timestamp crittografati sui messaggi di stato critici. Per Discovery V2 il subscriber mantiene anche una cache replay-bounded per nonce/annuncio e rifiuta messaggi duplicati.

### Autenticazione End-to-End

* **Bearer Token Auth:** Le richieste HTTP possono includere token di autenticazione che vengono trasmessi attraverso il tunnel e validati sia dall'Edge Gateway (per l'autorizzazione al tunnel) che dal servizio origin.
* **Connect Proof Data-Plane AuthZ:** in namespace-v2 il bridge deve presentare un connect proof firmato prima del forwarding upstream; il service verifica cluster/namespace/service, peer subject, expiry e replay prima di accettare il flusso.
* **Namespace-Scoped Service Listing:** `get services`, `get service/...`, `describe`, `inspect` e `watch` richiedono membership capability valida per il namespace selezionato; `-A` funziona solo se ogni namespace è autorizzato o se la capability è broad (`NamespaceID="*"`).
* **Peer Identity Binding:** Ogni Connector è associato a un'identità peer verificata. Gli Edge Gateway rifiutano connessioni da peer non riconosciuti o non autorizzati per il tenant richiesto.

## 🛡️ Rischi Specifici da Affrontare

1. **Compromissione del Connector:** Se un Attaccante riesce a compromettere un Connector, questo può agire come punto di ingresso malevolo al servizio locale (Origin Service). È cruciale isolare strettamente l'accesso al `localhost` e implementare autenticazione end-to-end.
2. **Masquerading:** L'attacco in cui un peer si spaccia per un altro. Questo viene mitigato dall'uso rigoroso di firme crittografiche basate sul Peer ID noto.
3. **Pubsub Spam:** Un attaccante può pubblicare annunci malevoli sul topic pubsub. Mitigazione: validazione firma su ogni annuncio, rate limiting sulle pubblicazioni, verifica cross-reference tra peer ID dell'annuncio e peer ID del mittente, e per Discovery V2 verifica di topic/scope, membership capability, `ServiceClaim` obbligatoria legata a `service_id`, e decryption fallita.
4. **Service Share Bearer Tokens:** i token generati da `tubo share service/...` sono connect-only, firmati dall'autorità del cluster e rifiutano token scaduti o alterati. Vengono convertiti in connect proof sul bridge path e vanno comunque trattati come credenziali sensibili.

## 📦 Stack Tecnologico e Dipendenze

* **Crittografia:** Utilizzo delle primitive crittografiche standard del libp2p Go stack (Curve, Noise/TLS).
* **Key Management:** Si prevede l'integrazione di un sistema di gestione delle chiavi sicuro (es. Vault) nelle fasi successive.
