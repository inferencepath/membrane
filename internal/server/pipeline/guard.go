package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrProtectionUnavailable is wrapped by a stage error when the Pathway's
// policy requires protection the stage cannot apply to this operation. It is a
// configuration condition, not a fault, and is reported to the Caller as such.
var ErrProtectionUnavailable = errors.New("protection unavailable for this operation")

// Caller-facing response for ErrProtectionUnavailable.
const (
	CodeProtectionUnavailable    = "protection_unavailable"
	MessageProtectionUnavailable = "this pathway requires data protection that is not available for this operation"
)

// ErrDetectionUnavailable is wrapped by a stage error when the detection
// engine cannot be reached or is failing. The request fails closed; it is a
// transient dependency outage, so the Caller gets a retryable 503.
var ErrDetectionUnavailable = errors.New("detection engine unavailable")

// Caller-facing response for ErrDetectionUnavailable.
const (
	CodeDetectionUnavailable    = "detection_unavailable"
	MessageDetectionUnavailable = "data protection is temporarily unavailable; retry later"
)

// ErrTokenStoreUnavailable is wrapped by a stage error when the token map store
// cannot be written or read. The request fails closed with a retryable 503.
var ErrTokenStoreUnavailable = errors.New("token map store unavailable")

// Caller-facing response for ErrTokenStoreUnavailable.
const CodeTokenStoreUnavailable = "token_store_unavailable"

// GuardFailure maps a GuardOperation error to the Caller-facing status, code
// and message: 503 for ErrProtectionUnavailable, ErrDetectionUnavailable and
// ErrTokenStoreUnavailable, and the generic 500 internal error for any other
// stage failure.
func GuardFailure(err error) (status int, code, message string) {
	if errors.Is(err, ErrProtectionUnavailable) {
		return http.StatusServiceUnavailable, CodeProtectionUnavailable, MessageProtectionUnavailable
	}
	if errors.Is(err, ErrDetectionUnavailable) {
		return http.StatusServiceUnavailable, CodeDetectionUnavailable, MessageDetectionUnavailable
	}
	if errors.Is(err, ErrTokenStoreUnavailable) {
		return http.StatusServiceUnavailable, CodeTokenStoreUnavailable, MessageDetectionUnavailable
	}
	return http.StatusInternalServerError, "internal", "internal error"
}

// GuardOperation submits a request whose content the handler does not pass to
// the stage (no Messages) so the stage can still decide whether the Pathway may
// be served for op. A non-nil error MUST fail the request closed: the stage's
// policy protects this Pathway, and the content would otherwise cross the
// Membrane unprotected. The Community NoOp stage always returns nil.
func GuardOperation(ctx context.Context, stage ContentStage, callerID, pathwayID string, op types.Operation) error {
	if stage == nil {
		return nil
	}
	return stage.Ingress(ctx, &RequestContext{
		RequestID: NewRequestID(),
		CallerID:  callerID,
		PathwayID: pathwayID,
		Operation: string(op),
	})
}

// LogGuardFailure logs a content-barrier failure: WARN when the Caller caused it
// (a 4xx such as a session conflict or oversized text), ERROR when Membrane
// could not protect the request (5xx), so operator alerts track real faults.
func LogGuardFailure(logger *slog.Logger, pathway string, status int, err error) {
	level := slog.LevelError
	if status < http.StatusInternalServerError {
		level = slog.LevelWarn
	}
	logger.Log(context.Background(), level, "content ingress failed", "pathway", pathway, "status", status, "error", err.Error())
}
