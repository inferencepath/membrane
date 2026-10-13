// Error normalization. A single, provider-neutral mapping turns
// transport/provider errors into Caller-facing results and identity-free metrics
// outcomes. The Caller never learns which Provider family, endpoint, region, or
// deployment was involved — UNLESS it set Reveal, in which case the responsible
// Provider family (and nothing finer) may be named, mirroring the Reveal gating
// of the success path.
package chat

import (
	"errors"
	"net/http"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
)

// ErrorModelVersion is the version of the provider-neutral error contract
// implemented here. Adding a new code is a MINOR change; changing the status for
// an existing condition or removing/renaming a code is MAJOR.
const ErrorModelVersion = 1

// Stable, machine-handleable error codes. These are the single source of truth
// for every Caller-facing surface and Interior gate; they are versioned by
// ErrorModelVersion and mirrored in the API contract (chat-api.yaml) and
// error-model.md. Keep this list and those documents in lockstep.
const (
	codeMethodNotAllowed    = "method_not_allowed"
	codeUnauthorized        = "unauthorized"
	codeForbidden           = "forbidden"
	codeBadRequest          = "bad_request"
	codeUnresolvedPathway   = "unresolved_pathway"
	codeTargetNotAllowed    = "target_not_allowed"
	codeRateLimited         = "rate_limited"
	codeInternal            = "internal"
	codeNoProviderAvailable = "no_provider_available"
	codeUpstreamTimeout     = "upstream_timeout"
	codeUpstreamUnavailable = "upstream_unavailable"
	codeProviderError       = "provider_error"
	codeStreamError         = "stream_error"
	codeUnavailable         = "unavailable"
)

// neutralError is a provider-neutral, Caller-facing error.
type neutralError struct {
	status  int
	code    string
	message string
}

// mapError is the single source of truth for turning a transport/provider error
// into a Caller-facing neutralError. It discloses the Provider family only when
// reveal is set and the family is known.
func mapError(err error, reveal bool) neutralError {
	// "No healthy target" (all breakers open / nothing resolvable) is a
	// service-availability condition, distinct from an upstream-returned error.
	if errors.Is(err, transporter.ErrNoProvider) {
		return neutralError{http.StatusServiceUnavailable, codeNoProviderAvailable,
			"no provider is currently available for this pathway"}
	}

	// Tool calling requested for a pathway whose provider does not support it:
	// a request-level error, mapped to 400 (never a provider-identifying 502).
	if errors.Is(err, providers.ErrToolCallingUnsupported) {
		return neutralError{http.StatusBadRequest, codeBadRequest,
			"tool calling is not supported for this pathway's provider"}
	}

	var ge neutralError
	switch providers.ClassifyFailure(err) {
	case providers.FailureTimeout:
		ge = neutralError{http.StatusGatewayTimeout, codeUpstreamTimeout,
			"the upstream provider did not respond in time"}
	case providers.FailureUnavailable:
		ge = neutralError{http.StatusGatewayTimeout, codeUpstreamUnavailable,
			"the upstream provider is temporarily unavailable"}
	default: // providers.FailureProvider
		ge = neutralError{http.StatusBadGateway, codeProviderError,
			"the upstream provider returned an error"}
	}

	// Reveal gating: name the responsible family only when the Caller asked.
	if reveal {
		if p := providers.RevealableProvider(err); p != "" {
			ge.message += " (provider: " + string(p) + ")"
		}
	}
	return ge
}

// outcomeFor classifies an error into an identity-free metrics outcome, derived
// from the same neutral FailureKind used for the HTTP mapping.
func outcomeFor(err error) metrics.Outcome {
	if errors.Is(err, transporter.ErrNoProvider) {
		return metrics.OutcomeInternalError
	}
	switch providers.ClassifyFailure(err) {
	case providers.FailureTimeout, providers.FailureUnavailable:
		return metrics.OutcomeTimeout
	default:
		return metrics.OutcomeUpstreamError
	}
}

// writeTransportError maps and writes a transport/provider error. It logs only
// the neutral error string (never a secret or caller/provider identity).
func (h *Handler) writeTransportError(w http.ResponseWriter, err error, reveal bool) {
	ge := mapError(err, reveal)
	if ge.status >= 500 && ge.code != "provider_error" && ge.code != "upstream_unavailable" {
		h.logger.Error("transport error", "code", ge.code, "error", err.Error())
	}
	writeError(w, ge.status, ge.code, ge.message)
}
