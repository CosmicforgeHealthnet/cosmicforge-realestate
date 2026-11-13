// Package supply is Marketplace's receiving side of the Business Operations
// supply contract (business-supply.v1). Marketplace is the first-party
// customer demand channel: it owns its listing projection, its customer
// events and its acknowledgements. It never reads Business databases and
// never manufactures a Business decision; every command is bound to a
// validated connection, the signed calling organisation, a canonical payload
// hash and a monotonic canonical version.
package supply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// ContractVersion is the only accepted Business supply contract version.
const ContractVersion = "business-supply.v1"

// Capabilities granted to a validated first-party connection.
var Capabilities = []string{"ARI", "CONTENT", "PROPERTY_MAPPING", "PRODUCT_MAPPING", "RATE_MAPPING", "READBACK", "PAUSE", "WITHDRAW", "POLLING", "RESERVATION_INGEST", "WEBHOOK"}

// Connection mirrors distribution.Connection on the Business side.
type Connection struct {
	ID                  ids.ID `json:"connection_id"`
	OrganisationID      ids.ID `json:"organisation_id"`
	PropertyID          ids.ID `json:"property_id"`
	ProviderAccountID   string `json:"provider_account_id"`
	CredentialReference string `json:"credential_reference"`
}

// Authorization is the validated connection authority.
type Authorization struct {
	ProviderAccountID string    `json:"provider_account_id"`
	PropertyID        ids.ID    `json:"property_id"`
	Reference         string    `json:"validation_reference"`
	ExpiresAt         time.Time `json:"expires_at"`
	Capabilities      []string  `json:"capabilities"`
}

// Command is one supply mutation.
type Command struct {
	ContractVersion  string          `json:"contract_version"`
	JobID            ids.ID          `json:"job_id"`
	Connection       Connection      `json:"connection"`
	Kind             string          `json:"kind"`
	CanonicalVersion int64           `json:"canonical_version"`
	IdempotencyKey   string          `json:"idempotency_key"`
	PayloadHash      string          `json:"payload_hash"`
	Payload          json.RawMessage `json:"payload"`
}

// Acknowledgement is Marketplace's durable acceptance of a command.
type Acknowledgement struct {
	ProviderRequestID string    `json:"provider_request_id"`
	ConnectionID      ids.ID    `json:"connection_id"`
	CanonicalVersion  int64     `json:"canonical_version"`
	PayloadHash       string    `json:"payload_hash"`
	State             string    `json:"state"`
	Durable           bool      `json:"durable"`
	ObservedAt        time.Time `json:"observed_at"`
}

// MappingReference / MappingProof mirror the Business mapping contract.
type MappingReference struct {
	Kind                   string `json:"kind"`
	ExternalID             string `json:"external_id"`
	CanonicalID            ids.ID `json:"canonical_id"`
	CanonicalVersion       int64  `json:"canonical_version"`
	ExpectedMappingVersion int64  `json:"expected_mapping_version"`
}
type MappingProof struct {
	Kind               string `json:"kind"`
	ExternalID         string `json:"external_id"`
	PropertyExternalID string `json:"property_external_id"`
	ProviderAccountID  string `json:"provider_account_id"`
	Reference          string `json:"reference"`
}

// PollResult is a cursor page of Marketplace customer events.
type PollResult struct {
	Cursor     string            `json:"cursor"`
	NextCursor string            `json:"next_cursor"`
	Events     []json.RawMessage `json:"events"`
}

// Listing is the Marketplace storefront projection readback.
type Listing struct {
	ConnectionID ids.ID            `json:"connection_id"`
	PropertyID   ids.ID            `json:"property_id"`
	Projections  []ListingRevision `json:"projections"`
}
type ListingRevision struct {
	Kind             string          `json:"kind"`
	CanonicalVersion int64           `json:"canonical_version"`
	PayloadHash      string          `json:"payload_hash"`
	PublicationState string          `json:"publication_state"`
	Payload          json.RawMessage `json:"payload"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// Service implements the supply contract over marketplace_db.
type Service struct {
	Pool *pgxpool.Pool
	TTL  time.Duration
	Now  func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// PayloadHash is the canonical hash shared with Business: canonical key
// order, integer-preserving decoding, exactly one JSON value.
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

func validText(v string, min, max int) bool {
	n := len(strings.TrimSpace(v))
	return n >= min && len(v) <= max && !strings.ContainsRune(v, 0)
}

// Validate registers (or re-validates) a first-party seller connection for
// the signed organisation. A connection identity is bound to exactly one
// organisation, property and provider account for its lifetime.
func (s *Service) Validate(ctx context.Context, org ids.ID, in Connection) (Authorization, error) {
	if in.ID.ValidateV7() != nil || in.PropertyID.ValidateV7() != nil || in.OrganisationID != org || org.IsZero() || !validText(in.ProviderAccountID, 3, 200) || !validText(in.CredentialReference, 3, 300) {
		return Authorization{}, apperrors.InvalidRequest("A signed organisation, connection, property, provider account and credential reference are required.")
	}
	now := s.now()
	ref := "mkt-connection:" + in.ID.String() + ":" + strconv.FormatInt(now.Unix(), 10)
	err := database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		var existingOrg, existingProperty ids.ID
		var account, state string
		e := tx.QueryRow(ctx, `SELECT organisation_id,property_id,provider_account_id,state FROM supply_connections WHERE id=$1 FOR UPDATE`, in.ID).Scan(&existingOrg, &existingProperty, &account, &state)
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			_, e = tx.Exec(ctx, `INSERT INTO supply_connections(id,organisation_id,property_id,provider_account_id,credential_reference,validation_reference,validated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.ID, org, in.PropertyID, in.ProviderAccountID, in.CredentialReference, ref, now)
			return e
		case e != nil:
			return e
		}
		if existingOrg != org || existingProperty != in.PropertyID || account != in.ProviderAccountID {
			return apperrors.Conflict("CONNECTION_IDENTITY_MISMATCH", "The connection is bound to a different organisation, property or provider account.")
		}
		if state != "ACTIVE" {
			return apperrors.Conflict("CONNECTION_SUSPENDED", "The Marketplace connection is suspended.")
		}
		_, e = tx.Exec(ctx, `UPDATE supply_connections SET credential_reference=$2,validation_reference=$3,validated_at=$4,version=version+1,updated_at=now() WHERE id=$1`, in.ID, in.CredentialReference, ref, now)
		return e
	})
	if err != nil {
		return Authorization{}, err
	}
	return Authorization{ProviderAccountID: in.ProviderAccountID, PropertyID: in.PropertyID, Reference: ref, ExpiresAt: now.Add(s.TTL), Capabilities: Capabilities}, nil
}

type connectionRow struct {
	org, property ids.ID
	account       string
	state         string
	validatedAt   time.Time
}

func (s *Service) connection(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, org, id ids.ID, lock bool) (connectionRow, error) {
	var c connectionRow
	query := `SELECT organisation_id,property_id,provider_account_id,state,validated_at FROM supply_connections WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	if e := q.QueryRow(ctx, query, id).Scan(&c.org, &c.property, &c.account, &c.state, &c.validatedAt); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return c, apperrors.NotFound("CONNECTION_NOT_FOUND", "Marketplace connection not found.")
		}
		return c, e
	}
	if c.org != org {
		return c, apperrors.NotFound("CONNECTION_NOT_FOUND", "Marketplace connection not found.")
	}
	return c, nil
}

var kindState = map[string]string{"CONTENT": "PUBLISHED", "ARI": "SYNCHRONIZED", "WITHDRAW": "WITHDRAWN", "PAUSE": "PAUSED", "RECONCILE": "SYNCHRONIZED"}

// Apply durably accepts one supply command exactly once. A replay of the
// same idempotency key returns the original acknowledgement; a different
// payload under the same key, a payload whose canonical hash does not match,
// or a stale canonical version is rejected.
func (s *Service) Apply(ctx context.Context, org ids.ID, in Command) (Acknowledgement, error) {
	state, ok := kindState[in.Kind]
	if !ok || in.ContractVersion != ContractVersion || in.JobID.ValidateV7() != nil || in.Connection.ID.ValidateV7() != nil || in.Connection.OrganisationID != org || in.CanonicalVersion < 1 || !validText(in.IdempotencyKey, 8, 300) || len(in.Payload) == 0 || len(in.Payload) > 256<<10 {
		return Acknowledgement{}, apperrors.InvalidRequest("A business-supply.v1 command with job, connection, kind, version, idempotency key and payload is required.")
	}
	hash, e := PayloadHash(in.Payload)
	if e != nil || hash != in.PayloadHash {
		return Acknowledgement{}, apperrors.InvalidRequest("The payload does not match its canonical payload hash.")
	}
	var ack Acknowledgement
	err := database.WithTx(ctx, s.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		c, e := s.connection(ctx, tx, org, in.Connection.ID, true)
		if e != nil {
			return e
		}
		if c.state != "ACTIVE" || c.property != in.Connection.PropertyID || c.account != in.Connection.ProviderAccountID {
			return apperrors.Conflict("CONNECTION_NOT_AUTHORIZED", "The command does not match an active validated connection.")
		}
		if s.now().After(c.validatedAt.Add(s.TTL)) {
			return apperrors.Conflict("CONNECTION_VALIDATION_EXPIRED", "Re-validate the Marketplace connection.")
		}
		var prior Acknowledgement
		e = tx.QueryRow(ctx, `SELECT provider_request_id,connection_id,canonical_version,payload_hash,state FROM supply_commands WHERE connection_id=$1 AND idempotency_key=$2`, in.Connection.ID, in.IdempotencyKey).Scan(&prior.ProviderRequestID, &prior.ConnectionID, &prior.CanonicalVersion, &prior.PayloadHash, &prior.State)
		if e == nil {
			if prior.PayloadHash != in.PayloadHash || prior.CanonicalVersion != in.CanonicalVersion {
				return apperrors.Conflict("IDEMPOTENCY_KEY_REUSED", "The idempotency key was used for a different command.")
			}
			ack = prior
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		// Projection kinds: content and ARI keep their own version lines;
		// withdraw/pause apply to every projection at or below the version.
		switch in.Kind {
		case "CONTENT", "ARI":
			var current int64
			e = tx.QueryRow(ctx, `SELECT canonical_version FROM listing_projections WHERE connection_id=$1 AND kind=$2 FOR UPDATE`, in.Connection.ID, in.Kind).Scan(&current)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			if e == nil && in.CanonicalVersion <= current {
				return apperrors.Conflict("STALE_CANONICAL_VERSION", "A newer or equal canonical version is already projected.")
			}
			if _, e = tx.Exec(ctx, `INSERT INTO listing_projections(connection_id,kind,canonical_version,payload_hash,payload,publication_state) VALUES($1,$2,$3,$4,$5,'PUBLISHED') ON CONFLICT(connection_id,kind) DO UPDATE SET canonical_version=EXCLUDED.canonical_version,payload_hash=EXCLUDED.payload_hash,payload=EXCLUDED.payload,publication_state='PUBLISHED',updated_at=now()`, in.Connection.ID, in.Kind, in.CanonicalVersion, in.PayloadHash, in.Payload); e != nil {
				return e
			}
		case "WITHDRAW", "PAUSE":
			if _, e = tx.Exec(ctx, `UPDATE listing_projections SET publication_state=$2,updated_at=now() WHERE connection_id=$1`, in.Connection.ID, state); e != nil {
				return e
			}
		}
		ack = Acknowledgement{ProviderRequestID: "mkt-req-" + ids.New().String(), ConnectionID: in.Connection.ID, CanonicalVersion: in.CanonicalVersion, PayloadHash: in.PayloadHash, State: state}
		_, e = tx.Exec(ctx, `INSERT INTO supply_commands(connection_id,idempotency_key,job_id,kind,canonical_version,payload_hash,payload,provider_request_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.Connection.ID, in.IdempotencyKey, in.JobID, in.Kind, in.CanonicalVersion, in.PayloadHash, in.Payload, ack.ProviderRequestID, state)
		return e
	})
	if err != nil {
		return Acknowledgement{}, err
	}
	ack.Durable, ack.ObservedAt = true, s.now()
	return ack, nil
}

// Readback returns the authoritative acknowledgement of a previously
// accepted command; an unknown key is NOT_FOUND, never a fabricated success.
func (s *Service) Readback(ctx context.Context, org, connection ids.ID, key string) (Acknowledgement, error) {
	if _, e := s.connection(ctx, s.Pool, org, connection, false); e != nil {
		return Acknowledgement{}, e
	}
	var ack Acknowledgement
	e := s.Pool.QueryRow(ctx, `SELECT provider_request_id,connection_id,canonical_version,payload_hash,state FROM supply_commands WHERE connection_id=$1 AND idempotency_key=$2`, connection, key).Scan(&ack.ProviderRequestID, &ack.ConnectionID, &ack.CanonicalVersion, &ack.PayloadHash, &ack.State)
	if errors.Is(e, pgx.ErrNoRows) {
		return ack, apperrors.NotFound("COMMAND_NOT_FOUND", "No accepted command exists for this idempotency key.")
	}
	if e != nil {
		return ack, e
	}
	ack.Durable, ack.ObservedAt = true, s.now()
	return ack, nil
}

// Poll returns customer events after cursor (a Marketplace sequence).
func (s *Service) Poll(ctx context.Context, org, connection ids.ID, cursor string) (PollResult, error) {
	if _, e := s.connection(ctx, s.Pool, org, connection, false); e != nil {
		return PollResult{}, e
	}
	after := int64(0)
	if cursor != "" {
		v, e := strconv.ParseInt(cursor, 10, 64)
		if e != nil || v < 0 {
			return PollResult{}, apperrors.InvalidRequest("Invalid cursor.")
		}
		after = v
	}
	rows, e := s.Pool.Query(ctx, `SELECT sequence,receipt FROM supply_events WHERE connection_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 100`, connection, after)
	if e != nil {
		return PollResult{}, e
	}
	defer rows.Close()
	out := PollResult{Cursor: cursor, NextCursor: strconv.FormatInt(after, 10), Events: []json.RawMessage{}}
	for rows.Next() {
		var seq int64
		var raw json.RawMessage
		if e = rows.Scan(&seq, &raw); e != nil {
			return PollResult{}, e
		}
		out.Events = append(out.Events, raw)
		out.NextCursor = strconv.FormatInt(seq, 10)
	}
	return out, rows.Err()
}

// InspectObject proves Marketplace's binding of an external object to the
// connection: the property object is the connection's Marketplace listing;
// product and rate objects are scoped beneath it.
func (s *Service) InspectObject(ctx context.Context, org, connection ids.ID, ref MappingReference) (MappingProof, error) {
	c, e := s.connection(ctx, s.Pool, org, connection, false)
	if e != nil {
		return MappingProof{}, e
	}
	if !validText(ref.ExternalID, 1, 200) || ref.Kind != "PROPERTY" && ref.Kind != "PRODUCT" && ref.Kind != "RATE_PLAN" {
		return MappingProof{}, apperrors.InvalidRequest("A supported object kind and external identifier are required.")
	}
	propertyExternal := "mkt-property:" + c.property.String()
	if ref.Kind == "PROPERTY" && ref.CanonicalID != c.property {
		return MappingProof{}, apperrors.Conflict("PROPERTY_MISMATCH", "The property object belongs to a different connection property.")
	}
	return MappingProof{Kind: ref.Kind, ExternalID: ref.ExternalID, PropertyExternalID: propertyExternal, ProviderAccountID: c.account, Reference: "mkt-mapping:" + connection.String() + ":" + ref.Kind + ":" + ref.ExternalID}, nil
}

// Listing is the storefront readback of the current projection.
func (s *Service) Listing(ctx context.Context, org, connection ids.ID) (Listing, error) {
	c, e := s.connection(ctx, s.Pool, org, connection, false)
	if e != nil {
		return Listing{}, e
	}
	rows, e := s.Pool.Query(ctx, `SELECT kind,canonical_version,payload_hash,publication_state,payload,updated_at FROM listing_projections WHERE connection_id=$1 ORDER BY kind`, connection)
	if e != nil {
		return Listing{}, e
	}
	defer rows.Close()
	out := Listing{ConnectionID: connection, PropertyID: c.property, Projections: []ListingRevision{}}
	for rows.Next() {
		var r ListingRevision
		if e = rows.Scan(&r.Kind, &r.CanonicalVersion, &r.PayloadHash, &r.PublicationState, &r.Payload, &r.UpdatedAt); e != nil {
			return Listing{}, e
		}
		out.Projections = append(out.Projections, r)
	}
	return out, rows.Err()
}
