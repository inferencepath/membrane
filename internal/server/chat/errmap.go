// Transport and provider errors map to provider-neutral Caller responses and
// identity-free metrics outcomes. The Caller never learns the Provider family,
// endpoint, region or deployment involved, except that with Reveal set the
// Provider family may be named, as on the success path.

package chat

import (
	"errors"
	"net/http"

	"github.com/inferencepath/membrane/internal/exterior/providers"
	"github.com/inferencepath/membrane/internal/membrane/transporter"
	"github.com/inferencepath/membrane/internal/observability/metrics"
)

// ErrorModelVersion is the version of the Caller-facing error contract. Adding
// a code is a minor change; changing the status for an existing condition or
// removing or renaming a code is a major one.
const ErrorModelVersion = 1

// Stable error codes returned to Callers, versioned by ErrorModelVersion. Keep
// the published API contract in sync when changing them.
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

// mapError turns a transport or provider error into a neutralError. It names
// the Provider family only when reveal is set and the family is known.
func mapError(err error, reveal bool) neutralError {
	// No healthy target (all breakers open, or nothing resolvable) is an
	// availability condition, not an upstream error.
	if errors.Is(err, transporter.ErrNoProvider) {
		return neutralError{http.StatusServiceUnavailable, codeNoProviderAvailable,
			"no provider is currently available for this pathway"}
	}

	// Unsupported tool calling is the request's fault, so it is a 400 rather
	// than a 502 that would hint at the provider.
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

	if reveal {
		if p := providers.RevealableProvider(err); p != "" {
			ge.message += " (provider: " + string(p) + ")"
		}
	}
	return ge
}

// outcomeFor classifies err into a metrics outcome using the same FailureKind
// as mapError.
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

// writeTransportError maps and writes a transport or provider error. The log
// line carries the neutral code and error string, never a secret or identity.
func (h *Handler) writeTransportError(w http.ResponseWriter, err error, reveal bool) {
	ge := mapError(err, reveal)
	if ge.status >= 500 && ge.code != "provider_error" && ge.code != "upstream_unavailable" {
		h.logger.Error("transport error", "code", ge.code, "error", err.Error())
	}
	writeError(w, ge.status, ge.code, ge.message)
}
