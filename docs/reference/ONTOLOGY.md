# Ontology

**Normative status:** [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md) defines future Tubo ontology. Prototype-specific terms below are retained only where explicitly labeled.

## Service

A Service is a local application endpoint known only to owner machine. Examples: HTTP, TCP, Unix socket. It is declarative, inert, local-only, and never automatically advertised or discoverable. Local target is secret operational metadata.

A2A and MCP are product/application capabilities, not Service transport kinds.

## Pipe

A Pipe is stable directed connectivity resource:

```text
Ingress -> Pipe -> Target
```

Every Pipe has exactly one Target: `service/...` or `pipe/...`. Pipe-to-Pipe composition is fundamental. Pipe owns stable network identity and intended connectivity policy; a Service never gains network identity merely by existing.

Ingress is local, gateway, or Tubo/libp2p. `attach` and `connect` are use-case presets that compose resources; they are not runtime kinds.

## Session

A Session is concrete active traversal of one or more Pipes. It owns live byte flow and continuity semantics. It records distinct logical, authorization, and transport paths without treating any one as proof of another. Each remote hop independently requires Access Lease authorization.

## Identity

Identity is stable cryptographic participant/resource-owner identity. `pipe_id` is secure Pipe identity and authorization audience; display names are non-unique labels; peer ID is runtime reachability identity; session ID identifies one traversal.

## Scope

Scope is optional collaboration, coordination, and security domain. It can govern membership, scoped policy, publishing, discovery, and revocation. It is not definition or prerequisite of connectivity. Current prototype represents Scope as `cluster + namespace`; that storage/model detail is reference, not future constraint.

## Invite and Access Lease

Invite is exact bootstrap/redemption credential. Access Lease is short-lived cryptographic authorization to open exact remote Pipe. Invite, record, route, Pipe ID, and membership alone do not authorize remote open.

## PipeRecord and Descriptor

PipeRecord is signed bounded network advertisement for a Pipe. It carries network/routing/connectivity metadata and never local Service target or arbitrary application metadata. Descriptor is higher-level discoverable metadata, for example AgentDescriptor, referencing connectivity resources such as Pipes.

## Prototype reference: Run

Prototype uses **Run** for execution of a primary resource, with PID/readiness/status/log/control/counters. Future A0 may retain or replace this concept only after explicit runtime-ownership design; it is not canonical connectivity identity.
