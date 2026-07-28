// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package grpcserver

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/azure/azure-dev/cli/azd/internal/tracing"
	"github.com/azure/azure-dev/cli/azd/internal/tracing/events"
	"github.com/azure/azure-dev/cli/azd/internal/tracing/fields"
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// installedExtensionLookup resolves the installed extension record for a
// signed extension id. *extensions.Manager satisfies it.
type installedExtensionLookup interface {
	GetInstalled(options extensions.FilterOptions) (*extensions.Extension, error)
}

// telemetryService implements azdext.TelemetryServiceServer.
type telemetryService struct {
	azdext.UnimplementedTelemetryServiceServer
	extensions installedExtensionLookup
}

// NewTelemetryService creates the telemetry gRPC service. The extension
// manager supplies the telemetry declarations an extension published in the
// registry it was installed from; the host owns no product-specific fields.
// Returning the interface type lets the IoC container satisfy the
// azdext.TelemetryServiceServer parameter on NewServer without an adapter.
func NewTelemetryService(manager *extensions.Manager) azdext.TelemetryServiceServer {
	return newTelemetryService(manager)
}

func newTelemetryService(lookup installedExtensionLookup) *telemetryService {
	return &telemetryService{extensions: lookup}
}

// ReportUsageAttribute records one usage attribute value that the calling
// extension declared in the official azd registry. It fails closed: callers
// without validated claims, extensions installed from any other source,
// extensions without the telemetry capability, undeclared keys, and values
// outside the declared set are all rejected before anything is recorded.
// Rejected caller text is never echoed into the returned error.
func (s *telemetryService) ReportUsageAttribute(
	ctx context.Context,
	req *azdext.ReportUsageAttributeRequest,
) (*azdext.ReportUsageAttributeResponse, error) {
	claims, err := extensions.GetClaimsFromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "validated extension claims are required")
	}

	if req == nil ||
		req.Key == "" || req.Value == "" ||
		len(req.Key) > extensions.MaxTelemetryKeyLength ||
		len(req.Value) > extensions.MaxTelemetryValueLength {
		return nil, status.Error(codes.InvalidArgument, "telemetry key and value are required")
	}

	// Gate on the recorded install source. The claim is
	// host-signed, so an extension cannot raise its own source per
	// request, but the record lives in user config. This keeps
	// well-behaved extensions inside reviewed declarations; it is
	// not proof of provenance.
	if !strings.EqualFold(claims.Source, extensions.MainRegistryName) {
		return nil, status.Error(codes.PermissionDenied,
			"telemetry requires an extension installed from the official registry")
	}

	if !slices.Contains(claims.Capabilities, extensions.TelemetryCapability) {
		return nil, status.Error(codes.PermissionDenied, "extension lacks the telemetry capability")
	}

	extension, err := s.extensions.GetInstalled(extensions.FilterOptions{Id: claims.Subject})
	if err != nil {
		if errors.Is(err, extensions.ErrInstalledExtensionNotFound) {
			return nil, status.Error(codes.PermissionDenied, "extension is not installed")
		}

		return nil, status.Error(codes.Internal, "failed to verify installed extension")
	}

	// Re-validate the stored declaration. The installed record lives in user
	// config, so shape is enforced again here rather than trusted from disk.
	if issues := extensions.ValidateTelemetryDeclarations(
		extension.Id, extension.Telemetry); len(issues) > 0 {
		return nil, status.Error(codes.FailedPrecondition, "telemetry declarations are invalid")
	}

	if !isDeclaredValue(extension.Telemetry, req.Key, req.Value) {
		return nil, status.Error(codes.InvalidArgument, "telemetry value is not declared")
	}

	// Record a dedicated span rather than augmenting the command span. The
	// extension's trace context arrives over gRPC metadata, so this span
	// shares the command's trace and joins on operation_Id downstream.
	_, span := tracing.Start(ctx, events.ExtensionUsageEvent)
	span.SetAttributes(
		fields.ExtensionId.String(extension.Id),
		fields.ExtensionVersion.String(extension.Version),
		fields.ExtensionUsageKey(req.Key).String(req.Value),
	)
	span.End()

	return &azdext.ReportUsageAttributeResponse{Accepted: true}, nil
}

// isDeclaredValue reports whether key is declared and value is in that key's
// declared closed set.
func isDeclaredValue(
	declarations []extensions.TelemetryFieldDeclaration,
	key string,
	value string,
) bool {
	for _, declaration := range declarations {
		if declaration.Key == key {
			return slices.Contains(declaration.AllowedValues, value)
		}
	}

	return false
}
