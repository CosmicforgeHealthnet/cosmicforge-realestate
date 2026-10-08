// Package ids implements the platform's canonical UUIDv7 identifier.
//
// Every persistent public/entity identifier in CosmicForge Real Estate is a
// UUIDv7 (RFC 9562) generated locally by the owning service. There is no
// central ID service. IDs are rendered at API/event boundaries in canonical
// lowercase 8-4-4-4-12 form and stored in PostgreSQL as uuid.
package ids

import (
	"crypto/rand"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ID is a 128-bit RFC 9562 UUID. Values produced by New are version 7.
type ID [16]byte

// Nil is the zero ID. It is never a valid entity identifier.
var Nil ID

var (
	// ErrInvalidFormat is returned when a string is not a canonical UUID.
	ErrInvalidFormat = errors.New("ids: invalid UUID format")
	// ErrNotV7 is returned when a well-formed UUID is not a version 7 RFC 9562 UUID.
	ErrNotV7 = errors.New("ids: UUID is not version 7")
	// ErrNil is returned when the nil UUID is supplied where an ID is required.
	ErrNil = errors.New("ids: nil UUID")
)

// generator state guarantees per-process monotonic ordering: within the same
// millisecond the 12-bit rand_a field is used as a counter (RFC 9562 §6.2,
// method 1). If the counter overflows, the timestamp is advanced by one
// millisecond, which keeps IDs strictly increasing without blocking.
var gen struct {
	mu     sync.Mutex
	lastMs int64
	seq    uint16
}

// now is replaceable in tests.
var now = time.Now

// New returns a new UUIDv7. It never returns the nil ID.
func New() ID {
	var id ID
	// crypto/rand.Read never returns an error on supported platforms (Go >= 1.24).
	_, _ = rand.Read(id[:])

	gen.mu.Lock()
	ms := now().UnixMilli()
	if ms <= gen.lastMs {
		gen.seq++
		if gen.seq > 0x0FFF {
			gen.lastMs++
			gen.seq = binary.BigEndian.Uint16(id[6:8]) & 0x03FF
		}
		ms = gen.lastMs
	} else {
		gen.lastMs = ms
		// Start each millisecond in the lower half of the counter space so
		// that many IDs per millisecond fit before overflow.
		gen.seq = binary.BigEndian.Uint16(id[6:8]) & 0x07FF
	}
	seq := gen.seq
	gen.mu.Unlock()

	encode(&id, ms, seq)
	return id
}

// NewAt returns a UUIDv7 whose timestamp is t. It does not participate in the
// monotonic counter and is intended for deterministic tests and backfills.
func NewAt(t time.Time) ID {
	var id ID
	_, _ = rand.Read(id[:])
	encode(&id, t.UnixMilli(), binary.BigEndian.Uint16(id[6:8])&0x0FFF)
	return id
}

func encode(id *ID, ms int64, seq uint16) {
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	id[6] = 0x70 | byte(seq>>8)&0x0F
	id[7] = byte(seq)
	id[8] = (id[8] & 0x3F) | 0x80 // RFC 9562 variant 10xx
}

// Parse parses any RFC 9562 UUID in 8-4-4-4-12 form. Upper- and lower-case hex
// digits are accepted; String always renders lowercase.
func Parse(s string) (ID, error) {
	var id ID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return Nil, ErrInvalidFormat
	}
	var buf [32]byte
	j := 0
	for i := 0; i < 36; i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		buf[j] = s[i]
		j++
	}
	if _, err := hex.Decode(id[:], buf[:]); err != nil {
		return Nil, ErrInvalidFormat
	}
	return id, nil
}

// ParseV7 parses s and additionally requires a non-nil version 7 RFC 9562 UUID.
// Use it for every externally supplied entity identifier.
func ParseV7(s string) (ID, error) {
	id, err := Parse(s)
	if err != nil {
		return Nil, err
	}
	if err := id.ValidateV7(); err != nil {
		return Nil, err
	}
	return id, nil
}

// MustParse parses s or panics. Intended for constants in tests.
func MustParse(s string) ID {
	id, err := Parse(s)
	if err != nil {
		panic(fmt.Sprintf("ids.MustParse(%q): %v", s, err))
	}
	return id
}

// ValidateV7 reports whether id is a non-nil RFC 9562 version 7 UUID.
func (id ID) ValidateV7() error {
	if id.IsZero() {
		return ErrNil
	}
	if id.Version() != 7 || id[8]&0xC0 != 0x80 {
		return ErrNotV7
	}
	return nil
}

// IsZero reports whether id is the nil UUID.
func (id ID) IsZero() bool { return id == Nil }

// Version returns the UUID version nibble.
func (id ID) Version() int { return int(id[6] >> 4) }

// Time returns the millisecond-precision creation time embedded in a UUIDv7.
func (id ID) Time() time.Time {
	ms := int64(id[0])<<40 | int64(id[1])<<32 | int64(id[2])<<24 |
		int64(id[3])<<16 | int64(id[4])<<8 | int64(id[5])
	return time.UnixMilli(ms).UTC()
}

// String renders the canonical lowercase form.
func (id ID) String() string {
	var buf [36]byte
	hex.Encode(buf[0:8], id[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], id[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], id[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], id[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:], id[10:])
	return string(buf[:])
}

// MarshalText implements encoding.TextMarshaler.
func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. Any RFC 9562 UUID is
// accepted so that historical references remain decodable; callers that need
// a fresh entity reference validate with ValidateV7.
func (id *ID) UnmarshalText(b []byte) error {
	parsed, err := Parse(string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// MarshalJSON renders the nil ID as JSON null and other IDs as strings.
func (id ID) MarshalJSON() ([]byte, error) {
	if id.IsZero() {
		return []byte("null"), nil
	}
	b := make([]byte, 0, 38)
	b = append(b, '"')
	b = append(b, id.String()...)
	return append(b, '"'), nil
}

// UnmarshalJSON accepts a UUID string or null (which yields the nil ID).
func (id *ID) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*id = Nil
		return nil
	}
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return ErrInvalidFormat
	}
	return id.UnmarshalText(b[1 : len(b)-1])
}

// Value implements driver.Valuer. The nil ID is stored as SQL NULL.
func (id ID) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, nil
	}
	return id.String(), nil
}

// Scan implements sql.Scanner for uuid/text/bytea columns.
func (id *ID) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = Nil
		return nil
	case string:
		return id.UnmarshalText([]byte(v))
	case []byte:
		if len(v) == 16 {
			copy(id[:], v)
			return nil
		}
		return id.UnmarshalText(v)
	case [16]byte:
		*id = ID(v)
		return nil
	default:
		return fmt.Errorf("ids: cannot scan %T into ID", src)
	}
}

// Compare orders IDs bytewise; for UUIDv7 this is creation order.
func Compare(a, b ID) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
