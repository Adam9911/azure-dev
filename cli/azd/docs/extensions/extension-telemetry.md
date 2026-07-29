# Extension Telemetry

This guide is for extension authors who need `azd` to record a usage signal on
their behalf — for example, which deployment mode a user picked.

Telemetry is a service `azd` offers to any extension. There is no capability to
declare and nothing to register: call `ReportUsage` with an event name and the
attributes you care about. `azd` core owns the identity fields, namespaces your
attributes, and bounds their size. It does not inspect what they mean.

See [ADR-001](../../../../docs/architecture/adr-001-extension-telemetry-events.md)
for the reasoning behind this design.

## What you own and what `azd` owns

| | |
|---|---|
| You choose | The event name and every attribute key and value |
| `azd` writes | `extension.id`, `extension.version`, `extension.source`, `extension.event` |
| `azd` enforces | The `ext.` prefix on your keys, and the size bounds below |

`azd` cannot write your attribute keys unprefixed, and you cannot overwrite the
identity fields — a key of `extension.id` is recorded as `ext.extension.id`.

## Reporting an event

```go
_, err := client.Telemetry().ReportUsage(
    ctx,
    &azdext.ReportUsageRequest{
        EventName: "deploy.completed",
        Attributes: map[string]string{
            "deploy.mode": "container",
            "retries":     "2",
        },
    },
)
```

Treat the call as best-effort. Older `azd` hosts return `Unimplemented`, and
every rejection is a plain error. Never let the result change command behavior,
and never retry. Report as soon as the values are known so a later failure in
your command still keeps the signal.

An event with no attributes is valid — "this happened" is a legitimate signal.

## Bounds

| Rule | Limit |
|---|---|
| Attributes per event | 32 |
| Event name length | 1–128 characters |
| Attribute key length | 1–128 characters |
| Attribute value length | 512 characters |

There are no charset rules. Exceeding any bound rejects the whole call and
records nothing, so a partially-valid event never lands as a complete-looking
one.

## Your responsibility for content

`azd` does not review your values at runtime, which makes the content rules
your responsibility as the extension author:

- **Never send customer content.** No file paths, resource names, connection
  strings, prompts, URLs, or anything a user typed. If you are unsure whether a
  value qualifies, it does.
- **Keep values low cardinality.** Prefer a small enum such as
  `code | container | unknown`. Unbounded values make the data expensive and
  unusable for aggregation.
- **Document your events** the way `azd` core documents its own fields: what
  each event and attribute means and why you need it.
- **Get a privacy review** as part of reviewing your extension, following the
  [telemetry privacy review checklist](../../../../docs/specs/metrics-audit/privacy-review-checklist.md).

## Why a call might be rejected

| Status | Cause |
|---|---|
| `Unauthenticated` | The request did not carry the host-issued extension token |
| `PermissionDenied` | The calling extension is not installed |
| `InvalidArgument` | The event name is missing, or a bound was exceeded |
| `Unimplemented` | The `azd` host predates this service |

Error messages never echo the event name, key, or value you sent, so use the
status code plus your own call site to diagnose.

## Where the data lands

Each accepted event becomes an `ext.usage` span carrying `extension.id`,
`extension.version`, `extension.source`, `extension.event`, and one `ext.<key>`
attribute per entry in your map. The span shares the command's trace, so it
joins to the originating command on `operation_Id` in Application Insights:

```kusto
dependencies
| where name == "ext.usage"
| where customDimensions["extension.id"] == "contoso.tools"
| where customDimensions["extension.event"] == "deploy.completed"
| summarize count() by tostring(customDimensions["ext.deploy.mode"])
```
