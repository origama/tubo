# 001-default-cluster-default-namespace

Happy path base del runner E2E:

- relay container `admin`
- Alice crea un cluster locale, pubblica `e2e-echo` e genera un token `tubo share service/...`
- Bob parte da config pulita, fa implicit public join e si connette con `tubo connect --token`, senza `tubo join cluster/home`
