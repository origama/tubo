# Tests / E2E deterministic runner

Questo harness esegue scenari Docker-based, uno per volta, con container separati per attori e stato persistente per actor sotto `generated/e2e/<scenario>-<run-id>/`.

Scenario iniziale:

- `001-default-cluster-default-namespace`

Uso:

```bash
tests/e2e/run.sh 001-default-cluster-default-namespace
tests/e2e/run.sh all
tests/e2e/run.sh clean
```

Target Make disponibili:

```bash
make e2e-default
make e2e
make e2e-clean
```

Il runner:

- compila `tubo` e `dummy-api-server` dal checkout corrente;
- costruisce una piccola immagine Docker locale con i binari appena compilati;
- crea una rete Docker isolata per scenario;
- avvia gli attori in container distinti (`admin`, `alice`, `bob`);
- conserva log e artefatti nel workdir dello scenario;
- rimuove rete e container a fine esecuzione, salvo `KEEP_WORK=1`.

Il primo scenario valida il happy path base:

- relay container `admin`;
- Alice pubblica un servizio `e2e-echo` e genera il token `tubo share service/...`;
- Bob parte da config pulita, fa implicit public join e si collega direttamente con `tubo connect --token`, senza `tubo join cluster/home`.
