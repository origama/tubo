# Versioning

Tubo Rebuilt versions three surfaces independently.

## Product version

Semantic Versioning for the whole binary:

```text
MAJOR.MINOR.PATCH
```

During greenfield development the product remains pre-1.0. All runtime capabilities ship in one binary.

## Protocol version

Shared wire version:

```text
major.minor
```

Initial protocol: `2.0`.

- major: incompatible tunnel, record, authority, or trust semantics;
- minor: explicitly negotiated additive capability;
- patch product fixes do not alter protocol semantics.

There is no compatibility with prior Tubo protocol 1.x and no fallback handler may be added without an explicit product decision.

## State schema version

The transactional store uses an integer schema version independent of product and protocol. Every migration documents:

- forward transformation;
- rollback/backup behavior;
- failure behavior;
- whether concurrent old binaries are rejected;
- operator action.

## Release notes

Every release records:

```text
Added
Changed
Fixed
Compatibility
```

Compatibility includes product version, protocol version, state schema version, mixed-node expectations, and operator action.

## Required decisions

Before merging a wire/state change, state explicitly:

1. product bump;
2. protocol bump or unchanged;
3. state schema bump or unchanged;
4. mixed-version behavior;
5. rollout and rollback instructions;
6. three-machine scenarios run.
