# ADR-001: Extensions report named usage events, not declared fields

**Status:** Proposed

**Date:** 2026-07-29

## Context

Extensions need to report usage signals so the team can answer product
questions like which deployment mode people actually pick. Two earlier designs
were tried and rejected.

1. An allowlist of product fields (key, allowed values, eligible commands)
   inside `azd` core. Every new field an extension wanted became a core PR, a
   core release, and a wait for users to upgrade `azd` before any data arrived.
   It also put one product's vocabulary into the CLI that hosts all of them.
2. Moving that allowlist into the extension's registry entry, so the registry
   declared fields and core enforced them. This removed the core release
   dependency but kept the overhead: authors maintained a parallel declaration,
   core carried validation code for it, and every value set had to be enumerated
   ahead of time. `azd` core has the same "document your telemetry" problem for
   its own fields and intends to solve it with linting rather than a runtime
   allowlist, so extensions should not carry a second, heavier mechanism.

The constraint is therefore: extensions report freely within a bounded shape,
core owns identity and the attribute namespace, and content is governed the way
core governs its own fields.

## Decision

**Extensions report a named event with a dynamic attribute map. `azd` core owns
identity and the attribute namespace and bounds only the shape.**

- `ReportUsage(event_name, map<string, string> attributes)` replaces
  `ReportUsageAttribute(key, value)`. The extension names the event and supplies
  the attributes; nothing is declared in advance.
- The host writes `extension.id`, `extension.version`, and `extension.source`
  from the signed claims and the installed record, and `extension.event` from
  the caller's event name. Because none of the identity fields are on the wire,
  an extension cannot assert which extension it is.
- Every caller-supplied key is prefixed with `ext.` before it reaches the span.
  OpenTelemetry attributes overwrite on a duplicate key, so without the prefix
  an extension sending `extension.id` would overwrite the host's own value.
- Bounds are shape-only: at most 32 attributes, event name and keys at most 128
  characters, values at most 512. No charset rules and no value enumeration.
- Telemetry is not a capability. Capabilities describe customer-facing features
  the host calls into; telemetry is a service the host offers, like every other
  service on the extension gRPC API.
- Install source is recorded as a dimension, not used as a gate. Core already
  emits `ext.run`, `ext.install`, and `extension.installed` for extensions from
  any source, and records `extension.source` on `ext.upgrade`. Gating only
  `ext.usage` would be inconsistent and would make the feature untestable for
  anyone developing an extension locally.
- Accepted events are recorded on an `ext.usage` span rather than being appended
  to the command span.

Removing the runtime allowlist does not remove content responsibility.
Extension telemetry is subject to the same rules as core telemetry: fields are
documented, classified, never carry customer content, and go through privacy
review. That review happens when the extension is reviewed, not on every call.

## Consequences

**Easier**

- Adding a signal is an extension change. Neither `azd` core nor the registry
  has to ship.
- `azd` core contains zero product semantics for extension telemetry, and no
  declaration validation code to maintain.
- A named event with individual attributes is directly queryable: filter on
  `extension.event`, then read `ext.*` from `customDimensions`. The previous
  one-key-per-call shape required stitching several spans together to
  reconstruct a single logical event.
- Extension authors can develop and test against a locally installed build.

**More difficult**

- Nothing stops a careless extension from putting high-cardinality or sensitive
  data in a value. This is now a review and documentation problem rather than a
  runtime one, which matches how core fields are handled and is the direction
  core linting is heading.
- The `ext.usage` span does not sit on the same row as the command in App
  Insights. Queries must join on `operation_Id`. This is reliable because `azd`
  does not sample and the exporter writes the trace ID to `operation_Id`.
- Trace context has to cross the gRPC boundary for that join to work, so the
  extension SDK forwards the W3C trace context headers and the server extracts
  them.
- Identity is derived from the installed record, which lives in user-writable
  config. An extension cannot assert its identity on the request, but this is
  not proof of provenance — it is the same trust level every existing capability
  gate already depends on.

## Alternatives Considered

**Keep the allowlist in core.** Simplest to review, and it keeps every value in
one Go file. Rejected because it puts one product's vocabulary in the CLI that
hosts all products, and because it forces a core release for each new field.

**Declare fields and allowed values in the registry entry.** The design this
ADR replaces. It removed the core release dependency but kept per-field
overhead in exchange for a guarantee that only holds for a non-tampered
install: telemetry ingestion is not authenticated per client, so a reviewed set
cannot be enforced from the client anyway. Governing content authoritatively
belongs downstream or in review, not in a runtime check.

**Gate reporting on the official registry install source.** Rejected as
inconsistent with existing core behavior — `ext.run`, `ext.install`, and
`extension.installed` are all emitted regardless of source — and because it
would block extension authors from exercising the feature during development.
Recording `extension.source` gives downstream queries the same filter without
the asymmetry.

**Make telemetry a capability.** Rejected per review feedback: capabilities
signal "this extension provides a customer-facing feature the host needs to
call", not "this extension consumes a host service".

**One RPC per attribute.** The previous shape. Rejected because a logical event
with several attributes arrived as several unrelated spans, which is both more
round trips and harder to query.

**Augment the existing command span instead of creating `ext.usage`.** Rejected:
it requires a process-global command-usage scope stack that has to be opened and
closed by middleware and kept correct across nested and concurrent commands.

**Verify a signed registry manifest on every report.** Rejected as
disproportionate. It adds key management, rotation, and an offline story to a
best-effort telemetry path, and would still only harden one client-side hop.
Editing the installed record already implies local write access, which is a
strictly larger problem than telemetry.
