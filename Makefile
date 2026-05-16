E2E_DEFAULT_SCENARIO=001-default-cluster-default-namespace

.PHONY: e2e e2e-default e2e-clean

e2e:
	tests/e2e/run.sh all

e2e-default:
	tests/e2e/run.sh $(E2E_DEFAULT_SCENARIO)

e2e-clean:
	tests/e2e/run.sh clean
