# Extension Telemetry Fields

This guide is for extension authors who need `azd` to record a bounded usage
signal on their behalf — for example, which deployment mode a user picked.

`azd` core owns no product-specific telemetry fields. Every field an extension
can report is declared in that extension's entry in the official `azd`
registry, and `azd` only enforces the declaration. Adding a field is therefore
a registry pull request in this repository, not a core release.

See [ADR-001](../../../../docs/architecture/adr-001-extension-telemetry-declarations.md)
for the reasoning behind this design.

## What you can and cannot report

| | |
|---|---|
| You choose | The key name and the closed set of values it may take |
| `azd` chooses | Which span the value lands on, its classification, and how it is exported |
| Always rejected | Free-form values, values outside your declared set, keys you did not declare |

Values must be a closed enum because that is what makes a privacy review
possible. Shape-only rules cannot prove that a value is free of customer
content. The charset excludes common URL and path separators as defense in
depth, and the host also compares each value against your reviewed
declaration.

## Step 1: Declare the capability

Add `telemetry` to `capabilities` in your `extension.yaml`:

```yaml
capabilities:
  - custom-commands
  - telemetry
```

## Step 2: Declare the fields in the registry entry

Add a `telemetry` array to your version entry in the official registry
(`cli/azd/extensions/registry.json`). Unlike `capabilities`, this is not
copied from `extension.yaml` — it only exists in the reviewed registry entry:

```json
{
  "version": "1.0.0",
  "capabilities": ["custom-commands", "telemetry"],
  "telemetry": [
    {
      "key": "ext.contoso.tools.deploy.mode",
      "allowedValues": ["code", "container", "unknown"]
    }
  ]
}
```

Rules enforced when the registry is validated:

| Rule | Limit |
|---|---|
| Key namespace | Must start with `ext.<your extension id>.` |
| Key length | 128 characters |
| Fields per version | 16 |
| Values per field | 1–32, unique |
| Value length | 64 characters |
| Value charset | Lowercase alphanumerics plus `_`, `-`, `.` |
| Capability | `telemetry` must be present when fields are declared |

The pull request that adds these values must trigger a privacy review. Expect
reviewers to ask what each value means and why it is needed. Follow the
[telemetry privacy review checklist](../../../../docs/specs/metrics-audit/privacy-review-checklist.md).

## Step 3: Report from your extension

```go
_, err := client.Telemetry().ReportUsageAttribute(
    ctx,
    &azdext.ReportUsageAttributeRequest{
        Key:   "ext.contoso.tools.deploy.mode",
        Value: "container",
    },
)
```

Treat the call as best-effort. Older `azd` hosts return `Unimplemented`, and
every rejection is a plain error. Never let the result change command
behavior, and never retry. Report the value as soon as it is known so a later
failure in your command still keeps the signal.

## Why a call might be rejected

| Status | Cause |
|---|---|
| `Unauthenticated` | The request did not carry the host-issued extension token |
| `PermissionDenied` | The extension was not installed from the official `azd` registry, is missing the `telemetry` capability, or is not installed |
| `FailedPrecondition` | The stored declaration no longer passes validation |
| `InvalidArgument` | The key was not declared, or the value is not in the declared set |

Error messages never echo the key or value you sent, so use the status code
plus your own declaration to diagnose.

## Where the data lands

Each accepted value becomes an `ext.usage` span carrying `extension.id`,
`extension.version`, and your attribute. The span shares the command's trace,
so it joins to the originating command on `operation_Id` in Application
Insights.
