// Package validation collects field-level validation problems into the
// platform error envelope (400 INVALID_REQUEST with details).
package validation

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Detail codes.
const (
	CodeRequired   = "REQUIRED"
	CodeInvalid    = "INVALID"
	CodeTooLong    = "TOO_LONG"
	CodeTooShort   = "TOO_SHORT"
	CodeRange      = "OUT_OF_RANGE"
	CodeNotAllowed = "NOT_ALLOWED"
)

// Validator accumulates problems. The zero value is ready to use.
type Validator struct{ details []apperrors.Detail }

// Add records a problem.
func (v *Validator) Add(field, code, message string) {
	v.details = append(v.details, apperrors.Detail{Field: field, Code: code, Message: message})
}

// Check records a problem when ok is false.
func (v *Validator) Check(ok bool, field, code, message string) bool {
	if !ok {
		v.Add(field, code, message)
	}
	return ok
}

// Required checks a non-blank string.
func (v *Validator) Required(field, value string) bool {
	return v.Check(strings.TrimSpace(value) != "", field, CodeRequired, field+" is required")
}

// MaxLen checks a maximum length in code points.
func (v *Validator) MaxLen(field, value string, max int) bool {
	return v.Check(utf8.RuneCountInString(value) <= max, field, CodeTooLong, fmt.Sprintf("%s must be at most %d characters", field, max))
}

// OneOf checks membership.
func (v *Validator) OneOf(field, value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	v.Add(field, CodeNotAllowed, fmt.Sprintf("%s must be one of %s", field, strings.Join(allowed, ", ")))
	return false
}

// Range checks lo <= n <= hi.
func (v *Validator) Range(field string, n, lo, hi int64) bool {
	return v.Check(n >= lo && n <= hi, field, CodeRange, fmt.Sprintf("%s must be between %d and %d", field, lo, hi))
}

// UUIDv7 parses a required UUIDv7 reference.
func (v *Validator) UUIDv7(field, value string) ids.ID {
	if !v.Required(field, value) {
		return ids.Nil
	}
	id, err := ids.ParseV7(value)
	if err != nil {
		v.Add(field, CodeInvalid, field+" must be a UUIDv7")
		return ids.Nil
	}
	return id
}

// ID validates an already-decoded ID is a non-nil UUIDv7.
func (v *Validator) ID(field string, id ids.ID) bool {
	if id.IsZero() {
		v.Add(field, CodeRequired, field+" is required")
		return false
	}
	return v.Check(id.ValidateV7() == nil, field, CodeInvalid, field+" must be a UUIDv7")
}

// Email checks a syntactically valid address.
func (v *Validator) Email(field, value string) bool {
	a, err := mail.ParseAddress(value)
	return v.Check(err == nil && a.Address == value && len(value) <= 254, field, CodeInvalid, field+" must be a valid email address")
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// PhoneE164 checks an E.164 phone number.
func (v *Validator) PhoneE164(field, value string) bool {
	return v.Check(e164.MatchString(value), field, CodeInvalid, field+" must be an E.164 phone number")
}

var slug = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)

// Slug checks a lowercase machine identifier.
func (v *Validator) Slug(field, value string, max int) bool {
	return v.Check(len(value) <= max && slug.MatchString(value), field, CodeInvalid, field+" must be a lowercase identifier")
}

// Valid reports whether no problems were recorded.
func (v *Validator) Valid() bool { return len(v.details) == 0 }

// Details returns recorded problems.
func (v *Validator) Details() []apperrors.Detail {
	return append([]apperrors.Detail(nil), v.details...)
}

// Err returns nil or a 400 INVALID_REQUEST error with details.
func (v *Validator) Err() error {
	if v.Valid() {
		return nil
	}
	return apperrors.InvalidRequest("Request validation failed.", v.details...)
}
