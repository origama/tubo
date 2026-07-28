E2E_SCENARIO ?= private-http

.PHONY: fmt test race build contract integration hygiene verify verify-local e2e-three-machine e2e-three-machine-preflight clean

fmt:
	@test -z "$$(gofmt -l $$(find cmd internal -name '*.go' -type f) | tee /dev/stderr)"

test:
	go test ./...

race:
	go test -race ./...

build:
	go build ./...

contract:
	./tests/contracts/cli.sh

integration:
	./tests/integration/private-http.sh

hygiene:
	./tests/verify-repo-hygiene.sh

verify-local: fmt test race build contract integration hygiene

# The distributed gate is intentionally separate: it requires three machines and
# explicit acknowledgement because it creates isolated remote processes/state.
verify: verify-local

e2e-three-machine-preflight:
	./tests/e2e/three-machine/run.sh preflight

e2e-three-machine:
	./tests/e2e/three-machine/run.sh $(E2E_SCENARIO)

clean:
	rm -rf bin generated coverage.out
