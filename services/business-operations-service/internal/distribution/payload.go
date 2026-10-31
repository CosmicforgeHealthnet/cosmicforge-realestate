package distribution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// PayloadHash uses canonical object-key order and integer-preserving decoding.
// PostgreSQL JSONB changes key order and whitespace; those representation
// differences must not invalidate a durable command after restart/readback.
func PayloadHash(payload []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if e := decoder.Decode(&value); e != nil {
		return "", e
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return "", errors.New("exactly one JSON payload required")
	}
	canonical, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
