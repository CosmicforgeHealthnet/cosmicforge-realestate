// Package apperrors implements the platform error model (ERROR_STANDARD.md).
//
// Every HTTP API returns the same envelope:
//
//	{"error": {"code": "PROPERTY_NOT_FOUND", "message": "Property not found.", "request_id": "...", "details": []}}
//
// Codes are stable SCREAMING_SNAKE_CASE identifiers. Messages are safe for the
// caller. The wrapped cause (SQL errors, provider responses, stack context) is
// kept for logging only and is never serialised to clients. Any error that is
// not an *Error maps to INTERNAL_ERROR.
//
// The directory is named errors/ per the approved layout; the package is named
// apperrors to avoid shadowing the standard library.
package apperrors

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
)

// Stable platform-wide codes. Services add their own domain codes
// (e.g. VERIFICATION_NOT_FOUND) following the same shape.
const (
	CodeInvalidRequest         = "INVALID_REQUEST"
	CodeInvalidJSON            = "INVALID_JSON"
	CodeAuthenticationRequired = "AUTHENTICATION_REQUIRED"
	CodeInvalidToken           = "INVALID_TOKEN"
	CodePermissionDenied       = "PERMISSION_DENIED"
	CodeNotFound               = "NOT_FOUND"
	CodeRouteNotFound          = "ROUTE_NOT_FOUND"
	CodeMethodNotAllowed       = "METHOD_NOT_ALLOWED"
	CodeConflict               = "CONFLICT"
	CodeIdempotencyConflict    = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyInProgress  = "IDEMPOTENCY_IN_PROGRESS"
	CodeIdempotencyKeyRequired = "IDEMPOTENCY_KEY_REQUIRED"
	CodeInvalidStateTransition = "INVALID_STATE_TRANSITION"
	CodePayloadTooLarge        = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedMediaType   = "UNSUPPORTED_MEDIA_TYPE"
	CodeUnprocessable          = "UNPROCESSABLE_ENTITY"
	CodeRateLimited            = "RATE_LIMITED"
	CodeInternal               = "INTERNAL_ERROR"
	CodeDownstreamProvider     = "DOWNSTREAM_PROVIDER_ERROR"
	CodeServiceUnavailable     = "SERVICE_UNAVAILABLE"
)

// Outcome distinguishes failure semantics for financial/verification/provider
// operations as required by ERROR_STANDARD.md.
type Outcome string

const (
	OutcomeNone            Outcome = ""
	OutcomeDefiniteFailure Outcome = "DEFINITE_FAILURE"
	OutcomeRetryable       Outcome = "RETRYABLE"
	OutcomeUnknown         Outcome = "UNKNOWN" // requires read-back before repeating a side effect
)

// Detail is a safe, field-level explanation (typically validation).
type Detail struct {
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is the platform error type.
type Error struct {
	Status  int
	Code    string
	Message string
	Details []Detail
	Outcome Outcome
	// RetryAfterSeconds, when > 0, is surfaced as a Retry-After header.
	RetryAfterSeconds int
	cause             error
}

var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$`)

// ValidCode reports whether code follows SCREAMING_SNAKE_CASE.
func ValidCode(code string) bool { return codePattern.MatchString(code) }

// New constructs an error. It panics on a malformed code or status because
// both are programmer errors that must be caught by tests.
func New(status int, code, message string) *Error {
	if !ValidCode(code) {
		panic(fmt.Sprintf("apperrors: invalid error code %q", code))
	}
	if status < 400 || status > 599 {
		panic(fmt.Sprintf("apperrors: invalid status %d", status))
	}
	return &Error{Status: status, Code: code, Message: message}
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.Message + ": " + e.cause.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap exposes the internal cause to errors.Is/As. It is never serialised.
func (e *Error) Unwrap() error { return e.cause }

// Cause returns the internal cause, if any.
func (e *Error) Cause() error { return e.cause }

func (e *Error) clone() *Error {
	c := *e
	c.Details = append([]Detail(nil), e.Details...)
	return &c
}

// WithCause returns a copy carrying an internal cause for logging.
func (e *Error) WithCause(err error) *Error { c := e.clone(); c.cause = err; return c }

// WithDetails returns a copy with additional safe details.
func (e *Error) WithDetails(d ...Detail) *Error {
	c := e.clone()
	c.Details = append(c.Details, d...)
	return c
}

// WithOutcome returns a copy with provider/financial outcome semantics.
func (e *Error) WithOutcome(o Outcome) *Error { c := e.clone(); c.Outcome = o; return c }

// WithRetryAfter returns a copy advertising Retry-After.
func (e *Error) WithRetryAfter(seconds int) *Error {
	c := e.clone()
	c.RetryAfterSeconds = seconds
	return c
}

// Is matches on Code so sentinel comparisons work across copies.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return t.Code == e.Code
	}
	return false
}

// Constructors for the common platform codes.

func InvalidRequest(message string, details ...Detail) *Error {
	return New(http.StatusBadRequest, CodeInvalidRequest, message).WithDetails(details...)
}

// Invalid creates a 400 error with a domain-specific INVALID_* code.
func Invalid(code, message string) *Error { return New(http.StatusBadRequest, code, message) }

func AuthenticationRequired() *Error {
	return New(http.StatusUnauthorized, CodeAuthenticationRequired, "Authentication is required.")
}

func InvalidToken() *Error {
	return New(http.StatusUnauthorized, CodeInvalidToken, "The access token is invalid or expired.")
}

func PermissionDenied(message string) *Error {
	if message == "" {
		message = "You do not have permission to perform this operation."
	}
	return New(http.StatusForbidden, CodePermissionDenied, message)
}

func NotFound(code, message string) *Error { return New(http.StatusNotFound, code, message) }

func Conflict(code, message string) *Error { return New(http.StatusConflict, code, message) }

func IdempotencyConflict() *Error {
	return New(http.StatusConflict, CodeIdempotencyConflict,
		"The Idempotency-Key was already used with a different request.")
}

func IdempotencyInProgress() *Error {
	return New(http.StatusConflict, CodeIdempotencyInProgress,
		"A request with this Idempotency-Key is still being processed.").WithRetryAfter(1)
}

func IdempotencyKeyRequired() *Error {
	return New(http.StatusBadRequest, CodeIdempotencyKeyRequired, "An Idempotency-Key header is required.")
}

func InvalidStateTransition(from, to string) *Error {
	return New(http.StatusConflict, CodeInvalidStateTransition,
		fmt.Sprintf("Cannot transition from %s to %s.", from, to))
}

func Unprocessable(code, message string) *Error {
	return New(http.StatusUnprocessableEntity, code, message)
}

func PayloadTooLarge() *Error {
	return New(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "The request body is too large.")
}

func UnsupportedMediaType() *Error {
	return New(http.StatusUnsupportedMediaType, CodeUnsupportedMediaType,
		"Content-Type must be application/json.")
}

func RateLimited(retryAfterSeconds int) *Error {
	return New(http.StatusTooManyRequests, CodeRateLimited, "Too many requests. Please retry later.").
		WithRetryAfter(retryAfterSeconds)
}

func Internal(cause error) *Error {
	return New(http.StatusInternalServerError, CodeInternal, "An internal error occurred.").WithCause(cause)
}

func DownstreamProvider(outcome Outcome, cause error) *Error {
	return New(http.StatusBadGateway, CodeDownstreamProvider, "A downstream provider failed.").
		WithOutcome(outcome).WithCause(cause)
}

func Unavailable(message string) *Error {
	if message == "" {
		message = "The service is temporarily unavailable."
	}
	return New(http.StatusServiceUnavailable, CodeServiceUnavailable, message)
}

// From converts any error into a platform *Error. Unknown errors become
// INTERNAL_ERROR with the original error preserved as the hidden cause.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}

// CodeOf returns the platform code for err ("" for nil).
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	return From(err).Code
}

// Body is the inner object of the error envelope.
type Body struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	RequestID string   `json:"request_id"`
	Details   []Detail `json:"details,omitempty"`
}

// Envelope is the serialised error response.
type Envelope struct {
	Error Body `json:"error"`
}

// ToEnvelope builds the client-safe envelope. The cause is never included.
func ToEnvelope(err error, requestID string) Envelope {
	e := From(err)
	return Envelope{Error: Body{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: requestID,
		Details:   e.Details,
	}}
}
