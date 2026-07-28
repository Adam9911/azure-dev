// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package extensions

import (
	"fmt"
	"regexp"
	"strings"
)

// Bounds keep registry entries small and are re-checked at runtime so
// malformed installed metadata cannot bypass them.
const (
	// MaxTelemetryFields is the number of fields one extension version may
	// declare.
	MaxTelemetryFields = 16
	// MaxTelemetryKeyLength bounds a declared attribute key.
	MaxTelemetryKeyLength = 128
	// MaxTelemetryAllowedValues bounds the closed value set of one field.
	MaxTelemetryAllowedValues = 32
	// MaxTelemetryValueLength bounds a single declared value.
	MaxTelemetryValueLength = 64
)

// TelemetryKeyPrefix namespaces every extension-declared telemetry key so
// extension values can never collide with, or masquerade as, a core field.
const TelemetryKeyPrefix = "ext."

// telemetryKeySegmentRegex matches one dot-separated segment of a key.
var telemetryKeySegmentRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// telemetryValueRegex bounds values to a conservative charset. It excludes
// common URL and path separators as a defense in depth measure.
var telemetryValueRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9_.-]*[a-z0-9])?$`)

// TelemetryFieldDeclaration declares one usage attribute that an extension
// version may report at runtime. The registry entry is the source of truth,
// so azd core carries no product-specific telemetry semantics: adding a field
// is a registry change, not a core release.
type TelemetryFieldDeclaration struct {
	// Key is the telemetry attribute name. It must be "ext." followed by the
	// declaring extension id and at least one more dot-separated segment.
	Key string `json:"key"`
	// AllowedValues is the closed set of values the extension may report for
	// Key. Free-form values are never accepted.
	AllowedValues []string `json:"allowedValues"`
}

// TelemetryKeyPrefixFor returns the key prefix an extension must use for
// every telemetry field it declares.
func TelemetryKeyPrefixFor(extensionId string) string {
	return TelemetryKeyPrefix + extensionId + "."
}

// ValidateTelemetryDeclarations reports every way declarations violate the
// host telemetry contract: namespaced keys, bounded closed value sets, and a
// conservative charset. It runs at publish time against the registry and
// again at runtime against the installed record, so it must stay free of any
// product-specific knowledge.
func ValidateTelemetryDeclarations(
	extensionId string,
	declarations []TelemetryFieldDeclaration,
) []string {
	if len(declarations) == 0 {
		return nil
	}

	var issues []string

	if len(declarations) > MaxTelemetryFields {
		issues = append(issues, fmt.Sprintf(
			"telemetry declares %d fields (max %d)", len(declarations), MaxTelemetryFields))
	}

	prefix := TelemetryKeyPrefixFor(extensionId)
	seenKeys := map[string]struct{}{}

	for i, declaration := range declarations {
		at := fmt.Sprintf("telemetry[%d]", i)

		if _, duplicate := seenKeys[declaration.Key]; duplicate {
			issues = append(issues, fmt.Sprintf("%s: duplicate key '%s'", at, declaration.Key))
		}
		seenKeys[declaration.Key] = struct{}{}

		issues = append(issues, validateTelemetryKey(at, prefix, declaration.Key)...)
		issues = append(issues, validateTelemetryValues(at, declaration.AllowedValues)...)
	}

	return issues
}

func validateTelemetryKey(at string, prefix string, key string) []string {
	if key == "" {
		return []string{fmt.Sprintf("%s: missing required field 'key'", at)}
	}

	if len(key) > MaxTelemetryKeyLength {
		return []string{fmt.Sprintf(
			"%s: key exceeds %d characters", at, MaxTelemetryKeyLength)}
	}

	if !strings.HasPrefix(key, prefix) {
		return []string{fmt.Sprintf(
			"%s: key '%s' must start with '%s'", at, key, prefix)}
	}

	suffix := strings.TrimPrefix(key, prefix)
	if suffix == "" {
		return []string{fmt.Sprintf(
			"%s: key '%s' needs at least one segment after '%s'", at, key, prefix)}
	}

	for segment := range strings.SplitSeq(suffix, ".") {
		if !telemetryKeySegmentRegex.MatchString(segment) {
			return []string{fmt.Sprintf(
				"%s: key '%s' has invalid segment '%s' "+
					"(lowercase alphanumeric and hyphens)", at, key, segment)}
		}
	}

	return nil
}

func validateTelemetryValues(at string, values []string) []string {
	if len(values) == 0 {
		return []string{fmt.Sprintf("%s: allowedValues must declare at least one value", at)}
	}

	var issues []string

	if len(values) > MaxTelemetryAllowedValues {
		issues = append(issues, fmt.Sprintf(
			"%s: declares %d allowed values (max %d)",
			at, len(values), MaxTelemetryAllowedValues))
	}

	seen := map[string]struct{}{}

	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			issues = append(issues, fmt.Sprintf("%s: duplicate allowed value '%s'", at, value))
		}
		seen[value] = struct{}{}

		if len(value) > MaxTelemetryValueLength {
			issues = append(issues, fmt.Sprintf(
				"%s: allowed value exceeds %d characters", at, MaxTelemetryValueLength))
			continue
		}

		if !telemetryValueRegex.MatchString(value) {
			issues = append(issues, fmt.Sprintf(
				"%s: invalid allowed value '%s' "+
					"(lowercase alphanumeric, '_', '-', and '.')", at, value))
		}
	}

	return issues
}
