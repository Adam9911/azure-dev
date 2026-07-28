# ADR-001: Extension telemetry is declared in the registry, not in azd core

**Status:** Proposed

**Date:** 2026-07-27

## Context

Extensions need to report a small number of bounded usage signals so the team
can answer product questions like which deployment mode people actually pick.
Two things made this awkward:

1. The first design put an allowlist of product fields (key, allowed values,
   eligible commands, required capability) inside `azd` core. That means every
   new field an extension wants is a core PR, a core release, and a wait for
   users to upgrade `azd` before any data arrives. It also puts product
   semantics from one Foundry product into the CLI that hosts all of them.
2. Extensions cannot simply be trusted to send arbitrary telemetry. The values
   need a privacy review, and a bounded set is what makes that review possible.

The constraint is therefore: reviewable and bounded, but not on the core
release path, and with no product-specific knowledge in core.

## Decision

**Field declarations move to the extension registry entry; `azd` core only
enforces them.**

- `ExtensionVersion` gains `telemetry: [{ key, allowedValues }]`, following the
  same "registry declares, core enforces" pattern already used for
  `capabilities`, `providers`, and `mcp`.
- Publish-time validation (`ValidateTelemetryDeclarations`) enforces the shape:
  keys namespaced as `ext.<extension id>.<segment>`, a non-empty closed value
  set, and a conservative charset that excludes common URL and path
  separators. Bounds cap the number of fields, values, and lengths. The exact
  allowed values still require privacy review.
- A dedicated `telemetry` capability gates the feature, and the extension's
  recorded install `Source` is carried in the host-signed JWT claims, so an
  extension cannot raise either on the request itself. Only extensions whose
  recorded source is the official `azd` registry are allowed to report.
- At runtime the host re-validates the stored declaration before use, because
  the installed record lives in user-writable config.
- Accepted values are recorded on a new `ext.usage` span rather than being
  appended to the command span.
- The RPC is named `ReportUsageAttribute`, deliberately free of any span
  wording, so the host can change where values land without a breaking SDK
  change.

The privacy gate does not disappear: the official registry lives in this
repository, so adding a field is still a reviewed PR by the same people. It
just no longer occupies a core release slot.

## Consequences

**Easier**

- Adding a telemetry field is a registry PR. Users get it by upgrading the
  extension; `azd` core does not ship.
- `azd` core contains zero product semantics for extension telemetry. The
  schema documents one class of field rather than one row per product concept.
- The trust boundary is explicit: the capability and the install `Source` are
  carried in host-signed claims, so an extension cannot assert either of them
  on the request itself.

**More difficult**

- Reviewers must read registry changes as telemetry changes. This needs to be
  part of the registry review checklist, not just tribal knowledge.
- The new span does not sit on the same row as the command in App Insights.
  Queries must join on `operation_Id`. This is reliable because `azd` does not
  sample and the exporter writes the trace ID to `operation_Id`.
- Trace context has to cross the gRPC boundary for that join to work, so the
  extension SDK now forwards the W3C trace context headers and the server
  extracts them.
- Those claims are signed from the installed record, which lives in
  user-writable config. This is the same trust level every existing capability
  gate already depends on, so the feature does not weaken it, but it is not
  proof of provenance. Client-side checks bound what a non-tampered install
  emits; governing the data authoritatively means filtering on
  `(extension.id, extension.version, key)` downstream.

## Alternatives Considered

**Keep the allowlist in core.** Simplest to review, and it keeps every value in
one Go file. Rejected because it puts one product's vocabulary in the CLI that
hosts all products, and because it forces a core release for each new field —
the maintenance overhead this design was asked to remove.

**Let extensions send free-form key/value pairs, validated only by shape**
(length, charset, cardinality). Rejected: an extension could pack a container
image reference into a value and leak a private registry name. A closed,
declared value set is what makes the privacy review meaningful.

**Put declarations in the signed JWT claims instead of looking them up.**
Rejected: the token travels on every RPC, so declarations would add constant
per-call overhead for a feature used a handful of times per command.

**Augment the existing command span instead of creating `ext.usage`.**
Rejected: it requires a process-global command-usage scope stack that has to be
opened and closed by middleware and kept correct across nested and concurrent
commands. Supporting both models would mean maintaining that machinery *and*
the new span path.

**Verify a signed registry manifest on every report** — ship a public key in
`azd`, sign each version's capabilities and telemetry declarations at publish
time, and re-verify before accepting a value. Rejected as disproportionate. It
adds key management, rotation, and an offline story to a best-effort telemetry
path, and it would still only harden one client-side hop: telemetry ingestion
is not authenticated per client, so no amount of local verification makes the
reviewed set enforceable from the client. The reviewed-set guarantee belongs
downstream, where `(extension.id, extension.version, key)` can be filtered
against the known registry. Editing the installed record also already implies
local write access, which is a strictly larger problem than telemetry.
