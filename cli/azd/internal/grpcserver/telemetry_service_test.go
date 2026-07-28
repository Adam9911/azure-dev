// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package grpcserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/azure/azure-dev/cli/azd/internal/tracing/events"
	"github.com/azure/azure-dev/cli/azd/internal/tracing/fields"
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testTelemetryKey = "ext.azd.internal.telemetry.deploy.mode"

// stubExtensionLookup returns a configured error or installed record.
type stubExtensionLookup struct {
	extension *extensions.Extension
	err       error
}

func (s stubExtensionLookup) GetInstalled(
	options extensions.FilterOptions,
) (*extensions.Extension, error) {
	if s.err != nil {
		return nil, s.err
	}

	if s.extension == nil || s.extension.Id != options.Id {
		return nil, extensions.ErrInstalledExtensionNotFound
	}

	return s.extension, nil
}

func testExtension() *extensions.Extension {
	return &extensions.Extension{
		Id:      "azd.internal.telemetry",
		Version: "1.0.0",
		Source:  extensions.MainRegistryName,
		Capabilities: []extensions.CapabilityType{
			extensions.TelemetryCapability,
		},
		Telemetry: []extensions.TelemetryFieldDeclaration{
			{
				Key:           testTelemetryKey,
				AllowedValues: []string{"code", "container"},
			},
		},
	}
}

// callWith runs the handler as the given extension, mirroring how the auth
// interceptor injects host-signed claims.
func callWith(
	t *testing.T,
	extension *extensions.Extension,
	req *azdext.ReportUsageAttributeRequest,
) (*azdext.ReportUsageAttributeResponse, error) {
	t.Helper()

	return callWithContext(t, t.Context(), extension, req)
}

// callWithContext is callWith with a caller-supplied context, so a test can
// place the call inside an existing trace.
func callWithContext(
	t *testing.T,
	ctx context.Context,
	extension *extensions.Extension,
	req *azdext.ReportUsageAttributeRequest,
) (*azdext.ReportUsageAttributeResponse, error) {
	t.Helper()

	service := newTelemetryService(stubExtensionLookup{extension: extension})
	ctx = extensions.WithClaimsContext(ctx, &extensions.ExtensionClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: extension.Id},
		Capabilities:     extension.Capabilities,
		Source:           extension.Source,
	})

	return service.ReportUsageAttribute(ctx, req)
}

// usageSpansIn returns the recorded ext.usage spans belonging to traceId.
// The recorder is shared by the whole package, so filtering on the trace
// started by a single test keeps it independent of every other test.
func usageSpansIn(traceId trace.TraceID) []tracesdk.ReadOnlySpan {
	matched := []tracesdk.ReadOnlySpan{}

	for _, span := range testSpanRecorder.Ended() {
		if span.Name() != events.ExtensionUsageEvent {
			continue
		}

		if span.SpanContext().TraceID() == traceId {
			matched = append(matched, span)
		}
	}

	return matched
}

func requireCode(t *testing.T, err error, expected codes.Code) {
	t.Helper()

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, expected, st.Code())
}

func Test_TelemetryService_AcceptsDeclaredValue(t *testing.T) {
	t.Parallel()

	// Start a command span first so the usage span can be checked to share
	// its trace, which is what makes the operation_Id join work downstream.
	ctx, command := testTracerProvider.Tracer("test").Start(t.Context(), "cmd.deploy")
	defer command.End()

	extension := testExtension()
	resp, err := callWithContext(t, ctx, extension, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "container",
	})

	require.NoError(t, err)
	require.True(t, resp.Accepted)

	recorded := usageSpansIn(command.SpanContext().TraceID())
	require.Len(t, recorded, 1)

	// Spans also carry process-global attributes, so assert only the ones
	// this feature owns rather than matching the whole set.
	attributes := map[attribute.Key]attribute.Value{}
	for _, attr := range recorded[0].Attributes() {
		attributes[attr.Key] = attr.Value
	}

	for _, expected := range []attribute.KeyValue{
		fields.ExtensionId.String(extension.Id),
		fields.ExtensionVersion.String(extension.Version),
		attribute.String(testTelemetryKey, "container"),
	} {
		require.Contains(t, attributes, expected.Key)
		require.Equal(t, expected.Value, attributes[expected.Key])
	}
}

func Test_TelemetryService_RecordsNoSpanWhenRejected(t *testing.T) {
	t.Parallel()

	ctx, command := testTracerProvider.Tracer("test").Start(t.Context(), "cmd.deploy")
	defer command.End()

	_, err := callWithContext(t, ctx, testExtension(), &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "byo_image",
	})

	requireCode(t, err, codes.InvalidArgument)
	require.Empty(t, usageSpansIn(command.SpanContext().TraceID()))
}

func Test_TelemetryService_RequiresClaims(t *testing.T) {
	t.Parallel()

	service := newTelemetryService(stubExtensionLookup{
		extension: testExtension(),
	})
	_, err := service.ReportUsageAttribute(t.Context(), &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.Unauthenticated)
}

func Test_TelemetryService_RejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	tests := map[string]*azdext.ReportUsageAttributeRequest{
		"nil":         nil,
		"empty key":   {Key: "", Value: "code"},
		"empty value": {Key: testTelemetryKey, Value: ""},
		"long key": {
			Key:   strings.Repeat("k", extensions.MaxTelemetryKeyLength+1),
			Value: "code",
		},
		"long value": {
			Key:   testTelemetryKey,
			Value: strings.Repeat("v", extensions.MaxTelemetryValueLength+1),
		},
	}

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := callWith(t, testExtension(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func Test_TelemetryService_RejectsUnofficialSource(t *testing.T) {
	t.Parallel()

	extension := testExtension()
	extension.Source = "dev"

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_RejectsMissingSource(t *testing.T) {
	t.Parallel()

	// An install predating source tracking must fail closed rather than be
	// treated as coming from the official registry.
	extension := testExtension()
	extension.Source = ""

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_RejectsMissingCapability(t *testing.T) {
	t.Parallel()

	extension := testExtension()
	extension.Capabilities = []extensions.CapabilityType{
		extensions.CustomCommandCapability,
	}

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_RejectsUninstalledExtension(t *testing.T) {
	t.Parallel()

	service := newTelemetryService(stubExtensionLookup{})
	ctx := extensions.WithClaimsContext(t.Context(), &extensions.ExtensionClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "azd.internal.telemetry"},
		Capabilities:     []extensions.CapabilityType{extensions.TelemetryCapability},
		Source:           extensions.MainRegistryName,
	})

	_, err := service.ReportUsageAttribute(ctx, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_ReportsLookupFailure(t *testing.T) {
	t.Parallel()

	service := newTelemetryService(stubExtensionLookup{
		err: errors.New("failed to read installed config"),
	})
	ctx := extensions.WithClaimsContext(t.Context(), &extensions.ExtensionClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "azd.internal.telemetry"},
		Capabilities:     []extensions.CapabilityType{extensions.TelemetryCapability},
		Source:           extensions.MainRegistryName,
	})

	_, err := service.ReportUsageAttribute(ctx, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.Internal)
	require.NotContains(t, err.Error(), "installed config")
}

func Test_TelemetryService_RejectsUndeclaredKeyOrValue(t *testing.T) {
	t.Parallel()

	tests := map[string]*azdext.ReportUsageAttributeRequest{
		"undeclared key":   {Key: "ext.azd.internal.telemetry.other", Value: "code"},
		"undeclared value": {Key: testTelemetryKey, Value: "byo_image"},
		"core key":         {Key: "agent.deploy.mode", Value: "code"},
	}

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := callWith(t, testExtension(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func Test_TelemetryService_RejectsInvalidStoredDeclaration(t *testing.T) {
	t.Parallel()

	// The installed record lives in user config, so an invalid local
	// declaration must be rejected instead of trusted.
	extension := testExtension()
	extension.Telemetry = []extensions.TelemetryFieldDeclaration{
		{
			Key:           "agent.deploy.mode",
			AllowedValues: []string{"code"},
		},
	}

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   "agent.deploy.mode",
		Value: "code",
	})

	requireCode(t, err, codes.FailedPrecondition)
}
