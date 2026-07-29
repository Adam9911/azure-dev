// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package grpcserver

import (
	"context"
	"errors"

	"github.com/azure/azure-dev/cli/azd/internal/tracing"
	"github.com/azure/azure-dev/cli/azd/internal/tracing/events"
	"github.com/azure/azure-dev/cli/azd/internal/tracing/fields"
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Bounds on a reported usage event. These are shape limits, not content
// review: the host does not judge what an attribute means, only that it
// cannot corrupt the shared telemetry pipeline.
const (
	// maxUsageAttributes caps attributes per event. The exporter enforces
	// no such limit, so this guards against a loop inflating span size.
	maxUsageAttributes = 32
	// maxUsageEventNameLength bounds the extension.event value, which is a
	// query dimension and should stay low cardinality.
	maxUsageEventNameLength = 128
	// maxUsageKeyLength bounds a caller key. The App Insights contracts
	// library truncates and renames property names longer than 150
	// characters, which would silently merge two distinct keys into one
	// column. Prefixed with "ext.", 128 stays well clear of that.
	maxUsageKeyLength = 128
	// maxUsageValueLength bounds a caller value. The exporter truncates at
	// 8192 with only a diagnostic, so this is a cardinality and cost
	// policy: a payload dump fails loudly here instead of quietly landing
	// in the pipeline.
	maxUsageValueLength = 512
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
// manager resolves the calling extension's installed record so the host can
// stamp identity the caller never supplies. Returning the interface type lets
// the IoC container satisfy the azdext.TelemetryServiceServer parameter on
// NewServer without an adapter.
func NewTelemetryService(manager *extensions.Manager) azdext.TelemetryServiceServer {
	return newTelemetryService(manager)
}

func newTelemetryService(lookup installedExtensionLookup) *telemetryService {
	return &telemetryService{extensions: lookup}
}

// ReportUsage records one usage event for the calling extension. The
// extension supplies only an event name and attribute map; the host derives
// identity from the signed claims and the installed record, so a caller
// cannot assert which extension it is. Install source is recorded as a
// dimension rather than a gate, matching ext.run and ext.install.
//
// It fails closed on the whole request: exceeding any bound records nothing,
// because dropping the offending attribute alone would ship data that looks
// complete but is not. Rejected caller text is never echoed back.
func (s *telemetryService) ReportUsage(
	ctx context.Context,
	req *azdext.ReportUsageRequest,
) (*azdext.ReportUsageResponse, error) {
	claims, err := extensions.GetClaimsFromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "validated extension claims are required")
	}

	if req == nil || req.EventName == "" || len(req.EventName) > maxUsageEventNameLength {
		return nil, status.Errorf(codes.InvalidArgument,
			"event name is required and must be at most %d characters", maxUsageEventNameLength)
	}

	if len(req.Attributes) > maxUsageAttributes {
		return nil, status.Errorf(codes.InvalidArgument,
			"event declares more than %d attributes", maxUsageAttributes)
	}

	extension, err := s.extensions.GetInstalled(extensions.FilterOptions{Id: claims.Subject})
	if err != nil {
		if errors.Is(err, extensions.ErrInstalledExtensionNotFound) {
			return nil, status.Error(codes.PermissionDenied, "extension is not installed")
		}

		return nil, status.Error(codes.Internal, "failed to verify installed extension")
	}

	attributes := []attribute.KeyValue{
		fields.ExtensionId.String(extension.Id),
		fields.ExtensionVersion.String(extension.Version),
		fields.ExtensionSource.String(extension.Source),
		fields.ExtensionEvent.String(req.EventName),
	}

	for key, value := range req.Attributes {
		if key == "" || len(key) > maxUsageKeyLength {
			return nil, status.Errorf(codes.InvalidArgument,
				"attribute keys are required and must be at most %d characters", maxUsageKeyLength)
		}

		if len(value) > maxUsageValueLength {
			return nil, status.Errorf(codes.InvalidArgument,
				"attribute values must be at most %d characters", maxUsageValueLength)
		}

		attributes = append(attributes, fields.ExtensionUsageAttribute(key).String(value))
	}

	// Record a dedicated span rather than augmenting the command span. The
	// extension's trace context arrives over gRPC metadata, so this span
	// shares the command's trace and joins on operation_Id downstream.
	_, span := tracing.Start(ctx, events.ExtensionUsageEvent)
	span.SetAttributes(attributes...)
	span.End()

	return &azdext.ReportUsageResponse{Accepted: true}, nil
}
