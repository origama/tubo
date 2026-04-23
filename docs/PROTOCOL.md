# Protocol Definition

Questo documento definisce il wire protocol utilizzato per le comunicazioni all'interno della piattaforma libp2p-native API Tunnel. È la base di tutte le interazioni tra Client Peer, Connector Agent e Control Plane.

## 📜 Scope
Il protocollario deve gestire tre tipi principali di messaggi:
1.  **Service Registration & Discovery:** Come un servizio annuncia la sua presenza (Tenant ID, Hostname supportato, Peer ID).
2.  **Request/Response Framing:** Come viene incapsulata una richiesta HTTP (o SOCKS5) e come vengono gestiti i dati di risposta (streaming/chunked).
3.  **Control & State Management:** Messaggi per heartbeats, aggiornamenti di lease, errori e politiche di sicurezza.

## 📦 Struttura del Frame Message (Proposta Iniziale)

Ogni messaggio trasmesso via libp2p sarà un frame strutturato:

```protobuf
message TunnelFrame {
    // Header fixed size
    uint32 message_type = 1; // Tipo di messaggio (e.g., REGISTRATION, REQUEST, RESPONSE, HEARTBEAT)
    bytes tenant_id = 2;   // Identificatore del Tenant (per isolamento e policy)
    bytes correlation_id = 3; // ID univoco per tracciare la richiesta end-to-end

    // Payload specifici al tipo di messaggio
    oneof payload {
        ServiceRegistration registration = 4;
        HttpRequest request = 5;
        HttpResponse response = 6;
        ControlSignal signal = 7; // Heartbeat, Lease expiry, Error Code
    }
}

message ServiceRegistration {
    bytes peer_id = 1;
    string supported_hostnames = 2;
    uint32 lease_duration_s = 3;
    // ... altri dati di capacità e stato
}

message HttpRequest {
    string method = 1; // GET, POST, etc.
    string path = 2;  // Path API
    map<string, string> headers = 3;
    bytes body_chunk = 4; // Usato per richieste chunked o corpo binario
}

message HttpResponse {
    uint32 status_code = 1;
    map<string, string> response_headers = 2;
    bytes body_chunk = 3; // Frammenti di risposta (essenziale per streaming)
    bool is_final = 4;   // Indica la fine della risposta
}

message ControlSignal {
    enum SignalType { HEARTBEAT, LEASE_EXPIRED, AUTH_ERROR }
    SignalType type = 1;
    string message = 2;
}
```

## ✅ Obiettivi del Milestone 1 (Protocollo)
*   Implementare la codifica e decodifica base di `TunnelFrame`.
*   Definire gli schemi per la registrazione dei servizi.
*   Stabilire il meccanismo di *heartbeat* e gestione dell'expiry/lease.

## ⚠️ Note sui Rischi
La sfida maggiore è garantire che il ciclo di vita dello streaming HTTP (chunking, backpressure) sia mappato correttamente sul ciclo di vita del libp2p stream. I frammenti (`body_chunk` in `HttpResponse`) sono critici per questo.