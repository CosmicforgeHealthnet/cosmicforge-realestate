// Package httpx contains the shared HTTP conventions: JSON success and error
// envelopes, strict request decoding, panic recovery, access logging, body
// limits, graceful server lifecycle and the standard internal HTTP client.
//
// The directory is named http/ per the approved layout; the package is named
// httpx to avoid shadowing net/http.
package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// ContentTypeJSON is the platform content type.
const ContentTypeJSON = "application/json; charset=utf-8"

// DataEnvelope wraps a single resource: {"data": {...}}.
type DataEnvelope struct {
	Data any `json:"data"`
}

// WriteJSON writes v as JSON with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b, _ = json.Marshal(apperrors.ToEnvelope(apperrors.Internal(err), ""))
	}
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// WriteData writes {"data": v}.
func WriteData(w http.ResponseWriter, status int, v any) {
	WriteJSON(w, status, DataEnvelope{Data: v})
}

// WriteRaw writes a pre-encoded JSON body (idempotent replays).
func WriteRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// WriteError writes the platform error envelope. Server-side errors are logged
// with their internal cause; the client sees only code/message/request_id.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e := apperrors.From(err)
	ctx := r.Context()
	if e.Status >= 500 {
		logging.FromContext(ctx).ErrorContext(ctx, "request failed",
			"code", e.Code, "status", e.Status, "error", errString(err),
			"method", r.Method, "path", r.URL.Path)
	}
	if e.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
	}
	reqID := requestcontext.RequestID(ctx)
	if reqID == "" {
		reqID = w.Header().Get(requestcontext.HeaderRequestID)
	}
	WriteJSON(w, e.Status, apperrors.ToEnvelope(e, reqID))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// NotFoundHandler returns ROUTE_NOT_FOUND in the platform envelope.
func NotFoundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, apperrors.NotFound(apperrors.CodeRouteNotFound, "The requested route does not exist."))
	})
}

// MethodNotAllowedHandler returns METHOD_NOT_ALLOWED in the platform envelope.
func MethodNotAllowedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, apperrors.New(http.StatusMethodNotAllowed, apperrors.CodeMethodNotAllowed, "Method not allowed."))
	})
}
