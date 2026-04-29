# Security multihost testbench

This directory contains a reproducible Docker Compose testbench for security auditing `tubo` in a multi-host-like private overlay.

## Run

```bash
./tests/security/security-multihost-testbench.sh
```

Keep the stack running for manual inspection:

```bash
KEEP_STACK=1 ./tests/security/security-multihost-testbench.sh
```

## What it exercises

- baseline private-overlay routing for multiple services;
- unauthenticated edge admin route injection;
- admin API exposure from the Docker host;
- duplicate service-name takeover by a rogue peer inside the private swarm;
- anonymous access to edge ingress;
- missing `ServiceName -> PeerID` binding.

## Output

The script writes:

```text
tests/security/artifacts/security-multihost-report.txt
```

The audit notes are tracked in:

```text
issues/SECURITY-AUDIT-MULTIHOST.md
```

## Important

The compose file intentionally uses a fixed private swarm key and intentionally exposes edge ingress/admin ports. Do not copy this configuration into production.
