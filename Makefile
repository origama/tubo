E2E_DEFAULT_SCENARIO=001-default-cluster-default-namespace

.PHONY: test race build smoke smoke-cli-ux integration verify verify-repo-hygiene e2e e2e-default e2e-clean

test:
	go test ./...

race:
	go test -race ./...

build:
	go build ./...

smoke:
	./tests/smoke-compose.sh

smoke-cli-ux:
	./tests/smoke-cli-ux.sh

integration:
	RUN_INTEGRATION=1 go test -v ./tests/integration

verify: test race build smoke smoke-cli-ux e2e-default

verify-repo-hygiene:
	./tests/verify-repo-hygiene.sh

e2e:
	tests/e2e/run.sh all

e2e-default:
	tests/e2e/run.sh $(E2E_DEFAULT_SCENARIO)

e2e-clean:
	tests/e2e/run.sh clean
