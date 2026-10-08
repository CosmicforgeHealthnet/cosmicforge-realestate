// Package money implements the platform money primitive (ADR-0008).
//
// Money is an integer amount in minor units plus an uppercase ISO 4217
// currency code. Binary floating point is never used: JSON decoding rejects
// fractional or exponent-form amounts, arithmetic is overflow-checked and
// mixed-currency arithmetic is an error.
//
// This package is a value type only. payment-service remains the authority
// for ledger and payment execution state.
package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

var (
	ErrInvalidCurrency  = errors.New("money: invalid ISO 4217 currency code")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	ErrOverflow         = errors.New("money: arithmetic overflow")
	ErrInvalidAmount    = errors.New("money: amount_minor must be an integer")
	ErrInvalidRatios    = errors.New("money: allocation ratios must be non-negative and sum to > 0")
)

// Currency is a validated, uppercase ISO 4217 alphabetic code.
type Currency string

// ParseCurrency validates code. Lowercase input is rejected: the canonical
// representation is uppercase at every boundary.
func ParseCurrency(code string) (Currency, error) {
	if len(code) != 3 {
		return "", ErrInvalidCurrency
	}
	if _, ok := iso4217[code]; !ok {
		return "", ErrInvalidCurrency
	}
	return Currency(code), nil
}

// MinorUnits returns the number of decimal places for the currency.
func (c Currency) MinorUnits() int { return iso4217[string(c)] }

// Valid reports whether c is a supported ISO 4217 code.
func (c Currency) Valid() bool { _, ok := iso4217[string(c)]; return ok }

func (c Currency) String() string { return string(c) }

// Money is an amount in minor units of a currency.
type Money struct {
	AmountMinor int64    `json:"amount_minor"`
	Currency    Currency `json:"currency"`
}

// New validates the currency and returns a Money value.
func New(amountMinor int64, currency string) (Money, error) {
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{AmountMinor: amountMinor, Currency: c}, nil
}

// MustNew is New that panics; intended for constants and tests.
func MustNew(amountMinor int64, currency string) Money {
	m, err := New(amountMinor, currency)
	if err != nil {
		panic(err)
	}
	return m
}

// Zero returns a zero amount in currency c.
func Zero(c Currency) Money { return Money{Currency: c} }

// Validate checks the currency.
func (m Money) Validate() error {
	if !m.Currency.Valid() {
		return ErrInvalidCurrency
	}
	return nil
}

func (m Money) IsZero() bool     { return m.AmountMinor == 0 }
func (m Money) IsNegative() bool { return m.AmountMinor < 0 }
func (m Money) IsPositive() bool { return m.AmountMinor > 0 }

func (m Money) sameCurrency(o Money) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Currency != o.Currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.Currency, o.Currency)
	}
	return nil
}

// Add returns m+o. Different currencies or int64 overflow are errors.
func (m Money) Add(o Money) (Money, error) {
	if err := m.sameCurrency(o); err != nil {
		return Money{}, err
	}
	s := m.AmountMinor + o.AmountMinor
	if (s > m.AmountMinor) != (o.AmountMinor > 0) {
		return Money{}, ErrOverflow
	}
	return Money{AmountMinor: s, Currency: m.Currency}, nil
}

// Sub returns m-o.
func (m Money) Sub(o Money) (Money, error) {
	if o.AmountMinor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return m.Add(Money{AmountMinor: -o.AmountMinor, Currency: o.Currency})
}

// Neg returns -m.
func (m Money) Neg() (Money, error) {
	if m.AmountMinor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{AmountMinor: -m.AmountMinor, Currency: m.Currency}, nil
}

// MulInt multiplies by an integer quantity with overflow checking.
func (m Money) MulInt(q int64) (Money, error) {
	if m.AmountMinor == 0 || q == 0 {
		return Money{Currency: m.Currency}, nil
	}
	r := m.AmountMinor * q
	if r/q != m.AmountMinor || (m.AmountMinor == -1 && q == math.MinInt64) || (q == -1 && m.AmountMinor == math.MinInt64) {
		return Money{}, ErrOverflow
	}
	return Money{AmountMinor: r, Currency: m.Currency}, nil
}

// Cmp compares two amounts of the same currency.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.sameCurrency(o); err != nil {
		return 0, err
	}
	switch {
	case m.AmountMinor < o.AmountMinor:
		return -1, nil
	case m.AmountMinor > o.AmountMinor:
		return 1, nil
	}
	return 0, nil
}

// Allocate splits m across ratios without losing or creating minor units.
// Remainders are distributed one unit at a time to the earliest shares.
func (m Money) Allocate(ratios ...int64) ([]Money, error) {
	if len(ratios) == 0 {
		return nil, ErrInvalidRatios
	}
	var total int64
	for _, r := range ratios {
		if r < 0 {
			return nil, ErrInvalidRatios
		}
		total += r
		if total < 0 {
			return nil, ErrOverflow
		}
	}
	if total == 0 {
		return nil, ErrInvalidRatios
	}
	neg := m.AmountMinor < 0
	amount := uint64(m.AmountMinor)
	if neg {
		amount = uint64(-m.AmountMinor)
	}
	out := make([]Money, len(ratios))
	var allocated uint64
	for i, r := range ratios {
		hi, lo := bits.Mul64(amount, uint64(r))
		share, _ := bits.Div64(hi, lo, uint64(total))
		out[i] = Money{AmountMinor: int64(share), Currency: m.Currency}
		allocated += share
	}
	remainder := amount - allocated
	for i := 0; remainder > 0; i = (i + 1) % len(out) {
		if ratios[i] == 0 {
			continue
		}
		out[i].AmountMinor++
		remainder--
	}
	if neg {
		for i := range out {
			out[i].AmountMinor = -out[i].AmountMinor
		}
	}
	return out, nil
}

// String renders a human-readable decimal form such as "NGN 25000.50".
// It is for logs/display only; never parse it back.
func (m Money) String() string {
	exp := m.Currency.MinorUnits()
	neg := m.AmountMinor < 0
	a := uint64(m.AmountMinor)
	if neg {
		a = uint64(-m.AmountMinor)
	}
	digits := strconv.FormatUint(a, 10)
	if exp > 0 {
		if len(digits) <= exp {
			digits = strings.Repeat("0", exp-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-exp] + "." + digits[len(digits)-exp:]
	}
	if neg {
		digits = "-" + digits
	}
	return string(m.Currency) + " " + digits
}

type wireMoney struct {
	AmountMinor json.RawMessage `json:"amount_minor"`
	Currency    *string         `json:"currency"`
}

// UnmarshalJSON requires both fields, an integer amount (no fraction, no
// exponent, no string) and a valid uppercase currency.
func (m *Money) UnmarshalJSON(b []byte) error {
	var w wireMoney
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return fmt.Errorf("money: %w", err)
	}
	if w.AmountMinor == nil || w.Currency == nil {
		return errors.New("money: amount_minor and currency are required")
	}
	raw := string(w.AmountMinor)
	if strings.ContainsAny(raw, ".eE\"") {
		return ErrInvalidAmount
	}
	amt, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ErrInvalidAmount
	}
	c, err := ParseCurrency(*w.Currency)
	if err != nil {
		return err
	}
	*m = Money{AmountMinor: amt, Currency: c}
	return nil
}
