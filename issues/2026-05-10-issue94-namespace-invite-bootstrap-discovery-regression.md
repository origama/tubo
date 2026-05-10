# Issue #94 — Namespace invite bootstrap and cross-machine service discovery regression

Status: DONE
Updated: 2026-05-10 20:30 UTC

## Summary
- `create namespace/<name>` does not leave the local config in a state where the namespace is immediately usable for `attach` publishing.
- `join cluster/<name> --token <invite>` stores the invite grant, but remote `get services` still does not reliably expose services announced from the invited namespace.

## Notes
- The fix should materialize a signed membership capability for newly created namespaces on the authority machine.
- Service runtime should publish the namespace-scoped membership bytes for the current namespace, not the cluster-level default capability.
- Invite grants should be able to authorize remote namespace queries without requiring a separate capability file on the receiving machine.
- Keep the public overlay join flow unchanged.
- Root cause in final validation was operational: the public relay on `relay.tubo.click` was not running with a working current deployment during checks.
- Resolution: deployed current `tubo` binary to `172.232.189.160`, installed `/etc/tubo/relay.yaml` + public swarm key, and started managed service `tubo-relay.service` (systemd).
- After relay restart, clean two-machine repro passed: machine A `attach` published discovery v2 heartbeat and machine B `get services` returned `received 1 services` with `dummyservice`.
