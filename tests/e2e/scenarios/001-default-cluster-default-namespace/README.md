# 001-default-cluster-default-namespace

Happy path base del runner E2E:

- relay container `admin`
- Alice crea un cluster locale, pubblica `e2e-echo` e genera un token `tubo share service/...`
- Bob si unisce al cluster, scopre il servizio via discovery e ci si connette con `tubo connect --token`
