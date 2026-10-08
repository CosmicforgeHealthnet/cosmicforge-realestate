package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

// DefaultMaxBodyBytes bounds JSON command bodies.
const DefaultMaxBodyBytes = 1 << 20

// DecodeJSON strictly decodes a JSON object body into dst:
// Content-Type must be application/json, the body is size-limited, unknown
// fields are rejected and trailing data is an error. Errors are client-safe.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	ct := r.Header.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || mt != "application/json" {
		return apperrors.UnsupportedMediaType()
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body must contain a single JSON object.")
	}
	return nil
}

func decodeError(err error) error {
	var maxErr *http.MaxBytesError
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &maxErr):
		return apperrors.PayloadTooLarge()
	case errors.Is(err, io.EOF):
		return apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body must not be empty.")
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body is not valid JSON.")
	case errors.As(err, &typeErr):
		field := typeErr.Field
		return apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body has a field of the wrong type.").
			WithDetails(apperrors.Detail{Field: field, Code: "INVALID_TYPE", Message: field + " has the wrong type"})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body contains an unknown field.").
			WithDetails(apperrors.Detail{Field: field, Code: "UNKNOWN_FIELD", Message: field + " is not allowed"})
	}
	// Errors from custom UnmarshalJSON (ids, money) are safe validation messages.
	return apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body is invalid.")
}

// ReadBody reads a JSON command body (Content-Type checked, size-limited)
// and returns the raw bytes, e.g. for idempotency fingerprinting.
func ReadBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return nil, apperrors.UnsupportedMediaType()
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, apperrors.PayloadTooLarge()
		}
		return nil, apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body could not be read.")
	}
	return b, nil
}

// DecodeJSONBytes strictly decodes one JSON object from b into dst.
func DecodeJSONBytes(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "Request body must contain a single JSON object.")
	}
	return nil
}
