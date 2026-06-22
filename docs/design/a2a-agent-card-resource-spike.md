# Spike: model A2A Agent Card as a Tubo resource (#306)

## Context

This spike checks whether an A2A Agent Card fits Tubo’s existing model:

`primary resource + capabilities + discovery metadata`

The goal is conceptual only. It should not introduce a new A2A runtime, a new CLI surface, or a new discovery protocol.

## Hypothesis

Yes: an agent maps naturally to a Tubo resource, not to a special process category.

Today, Tubo already models other runtime-backed resources with `primary_ref` and `capabilities`.
This spike proposes reusing that shape for agent resources, without adding an agent-specific runtime class.

Preferred ref:

- `agent/<name>`

Why:

- it matches the existing resource-style naming used by Tubo;
- it keeps the `primary_ref` simple and stable;
- it avoids inventing an `agent process` special case;
- the runtime process that hosts an agent stays separate from the agent resource itself.

## Proposed mapping

Conceptually:

```text
resource_kind: agent
primary_ref: agent/<name>
protocol: a2a
```

Example:

```yaml
agent/reviewer:
  primary_ref: agent/reviewer
  protocol: a2a
  endpoint: http://127.0.0.1:9000
  card_path: /.well-known/agent-card.json
  capabilities:
    - publish
    - agent.a2a
    - discovery.cache
    - discovery.query
    - discovery.sync
```

## Discovery metadata vs lazy Agent Card fetch

Discovery metadata should stay small, routeable, and verifiable. It should answer: “what is this agent, where do I reach it, and how do I fetch the full card?”

Copy into Tubo discovery metadata:

- `name`
- `resource_kind=agent`
- `primary_ref`
- `protocol=a2a`
- `endpoint`
- `card_path`
- short description / display name
- published capability summary
- scope metadata already used by Tubo (cluster / namespace / ids / signed record context)

Fetch lazily from the agent endpoint:

- full Agent Card payload
- large/descriptive fields
- tool / skill detail
- model / routing hints that are not needed for indexing
- any fast-changing or bulky metadata
- auth-specific details that only matter when opening a session

In short: discovery indexes the agent; the endpoint provides the full card.

## Example resource record

Conceptual record:

```yaml
kind: agent
name: reviewer
primary_ref: agent/reviewer
protocol: a2a
endpoint: http://127.0.0.1:9000
card_path: /.well-known/agent-card.json
capabilities:
  - publish
  - agent.a2a
  - discovery.cache
  - discovery.query
  - discovery.sync
```

This is a Tubo resource record, not the full Agent Card.

## Security notes

- capability != authority
- discovery cache/query peers are not trusted registries
- Agent Card payload is descriptive metadata, not a source of authority
- Tubo must still verify the record it stores or relays
- if an endpoint-supplied card is fetched, Tubo should treat it as data to validate, not as truth by default

The important boundary is:

- capabilities describe runtime reach;
- signed/verifiable record data describes what Tubo can trust;
- the Agent Card can enrich the record, but should not replace verification.

## Out of scope

- no new A2A runtime
- no definitive new CLI command
- no new discovery protocol
- no record-based query/sync redesign
- no special “agent process” category

## Implemented today vs proposed

Implemented today in Tubo:

- process state can carry `primary_ref` and `capabilities`
- discovery can already carry resource metadata and capabilities for existing resource kinds

Proposed in this spike:

- `resource_kind=agent`
- `primary_ref=agent/<name>`
- `protocol=a2a`
- lazy Agent Card fetch from the agent endpoint

## Follow-up for #307

Minimal end-to-end follow-up:

1. publish one `agent/<name>` resource with `protocol=a2a`
2. fetch the Agent Card lazily from `card_path`
3. verify Tubo can list/inspect the resource without treating the card as authority
4. prove one demo flow end to end without adding a separate agent-process model

## Conclusion

Use `agent/<name>`.

A2A should be modeled as a Tubo resource with primary identity, runtime capabilities, and discovery metadata. The Agent Card stays a descriptive payload fetched on demand, while Tubo keeps the trust boundary in its own signed/verifiable resource record.
