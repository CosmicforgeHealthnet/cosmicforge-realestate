// Package hospitality owns physical stays and operational obligations. Canonical
// reservations own stock; Payment owns receipts; device providers own access truth.
package hospitality

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type RoomStatus struct {
	ID                ids.ID `json:"id"`
	OrganisationID    ids.ID `json:"organisation_id"`
	PropertyID        ids.ID `json:"property_id"`
	RoomID            ids.ID `json:"room_id"`
	State             string `json:"state"`
	EvidenceReference string `json:"evidence_reference"`
	Reason            string `json:"reason"`
	Version           int64  `json:"version"`
}
type Assignment struct {
	ID                ids.ID `json:"id"`
	OrganisationID    ids.ID `json:"organisation_id"`
	PropertyID        ids.ID `json:"property_id"`
	ReservationID     ids.ID `json:"reservation_id"`
	ReservationRoomID ids.ID `json:"reservation_room_id"`
	RoomID            ids.ID `json:"room_id"`
	Slot              int    `json:"slot"`
	DateFrom          string `json:"date_from"`
	DateTo            string `json:"date_to"`
	State             string `json:"state"`
	Version           int64  `json:"version"`
}
type Stay struct {
	ID                    ids.ID     `json:"id"`
	OrganisationID        ids.ID     `json:"organisation_id"`
	PropertyID            ids.ID     `json:"property_id"`
	ReservationID         ids.ID     `json:"reservation_id"`
	AssignmentID          ids.ID     `json:"assignment_id"`
	GuestID               ids.ID     `json:"guest_id"`
	State                 string     `json:"state"`
	RegistrationReference string     `json:"registration_reference"`
	CheckedInAt           time.Time  `json:"checked_in_at"`
	CheckedOutAt          *time.Time `json:"checked_out_at,omitempty"`
	Version               int64      `json:"version"`
}
type Folio struct {
	ID             ids.ID `json:"id"`
	OrganisationID ids.ID `json:"organisation_id"`
	PropertyID     ids.ID `json:"property_id"`
	ReservationID  ids.ID `json:"reservation_id"`
	StayID         ids.ID `json:"stay_id"`
	State          string `json:"state"`
	Currency       string `json:"currency"`
	Version        int64  `json:"version"`
}
type Item struct {
	ID              ids.ID          `json:"id"`
	OrganisationID  ids.ID          `json:"organisation_id"`
	PropertyID      ids.ID          `json:"property_id"`
	FolioID         ids.ID          `json:"folio_id"`
	Kind            string          `json:"kind"`
	SourceReference string          `json:"source_reference"`
	Quantity        int             `json:"quantity"`
	AmountMinor     int64           `json:"amount_minor"`
	Currency        string          `json:"currency"`
	PolicySnapshot  json.RawMessage `json:"policy_snapshot"`
	AdjustsItemID   *ids.ID         `json:"adjusts_item_id,omitempty"`
	ApprovalID      *ids.ID         `json:"approval_id,omitempty"`
	Reason          string          `json:"reason"`
	CreatedAt       time.Time       `json:"created_at"`
}

func Load[T any](ctx context.Context, q database.Querier, sql string, args ...any) (v T, e error) {
	var b []byte
	e = q.QueryRow(ctx, sql, args...).Scan(&b)
	if e == pgx.ErrNoRows {
		return v, apperrors.NotFound("HOSPITALITY_RESOURCE_NOT_FOUND", "Hospitality resource not found.")
	}
	if e == nil {
		e = json.Unmarshal(b, &v)
	}
	return
}
func GetAssignment(ctx context.Context, q database.Querier, org, id ids.ID) (Assignment, error) {
	raw, e := (&db.Queries{}).GetPhysicalRoomAssignment(ctx, q, db.GetPhysicalRoomAssignmentParams{OrganisationID: org, ID: id})
	return decodeRecord[Assignment](raw, e)
}
func GetStay(ctx context.Context, q database.Querier, org, id ids.ID) (Stay, error) {
	raw, e := (&db.Queries{}).GetHospitalityStay(ctx, q, db.GetHospitalityStayParams{OrganisationID: org, ID: id})
	return decodeRecord[Stay](raw, e)
}
func GetFolio(ctx context.Context, q database.Querier, org, id ids.ID) (Folio, error) {
	raw, e := (&db.Queries{}).GetHospitalityFolio(ctx, q, db.GetHospitalityFolioParams{OrganisationID: org, ID: id})
	return decodeRecord[Folio](raw, e)
}
func Ready(state string) bool { return state == "INSPECTED" || state == "VACANT_CLEAN" }
func StatusTransition(from, to string) bool {
	switch from {
	case "":
		return to == "VACANT_DIRTY" || to == "BLOCKED" || to == "OUT_OF_ORDER"
	case "VACANT_DIRTY":
		return to == "VACANT_CLEAN" || to == "BLOCKED" || to == "OUT_OF_ORDER"
	case "VACANT_CLEAN":
		return to == "INSPECTED" || to == "VACANT_DIRTY" || to == "BLOCKED" || to == "OUT_OF_ORDER"
	case "INSPECTED":
		return to == "VACANT_CLEAN" || to == "VACANT_DIRTY" || to == "BLOCKED" || to == "OUT_OF_ORDER"
	case "BLOCKED", "OUT_OF_ORDER":
		return to == "VACANT_DIRTY"
	}
	return false
}

// LocalInstant chooses the latest occurrence in a DST fold. A nonexistent wall
// time advances to the first valid local minute. This avoids a second rollover
// or a business-date regression when clocks move backwards.
func LocalInstant(day, clock, zone string) (time.Time, error) {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, e
	}
	d, e := time.Parse(time.DateOnly, day)
	if e != nil {
		return time.Time{}, e
	}
	fields := strings.Split(clock, ":")
	if len(fields) != 2 {
		return time.Time{}, fmt.Errorf("invalid local clock")
	}
	hour, e := strconv.Atoi(fields[0])
	if e != nil || hour < 0 || hour > 23 {
		return time.Time{}, fmt.Errorf("invalid hour")
	}
	minute, e := strconv.Atoi(fields[1])
	if e != nil || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("invalid minute")
	}
	target := day + " " + fmt.Sprintf("%02d:%02d", hour, minute)
	// Civil offsets span -12 through +14; scan covers both date-line extremes.
	begin := d.Add(-15 * time.Hour)
	var selected time.Time
	best := ""
	for i := 0; i <= 54*60; i++ {
		v := begin.Add(time.Duration(i) * time.Minute)
		wall := v.In(loc).Format("2006-01-02 15:04")
		if wall < target || wall[:10] != day {
			continue
		}
		if best == "" || wall < best || wall == best && v.After(selected) {
			best = wall
			selected = v
		}
	}
	if selected.IsZero() {
		return selected, fmt.Errorf("civil date has no valid instant")
	}
	return selected, nil
}
func BusinessDate(now time.Time, clock, zone string) (string, error) {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return "", e
	}
	day := now.In(loc).Format(time.DateOnly)
	cutoff, e := LocalInstant(day, clock, zone)
	if e != nil {
		return "", e
	}
	if now.Before(cutoff) {
		d, _ := time.Parse(time.DateOnly, day)
		day = d.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	return day, nil
}

func decodeRecord[T any](raw []byte, e error) (v T, err error) {
	if e == pgx.ErrNoRows {
		return v, apperrors.NotFound("HOSPITALITY_RESOURCE_NOT_FOUND", "Hospitality resource not found.")
	}
	if e != nil {
		return v, e
	}
	err = json.Unmarshal(raw, &v)
	return
}

type FolioCloseReview struct {
	ID                      ids.ID    `json:"id"`
	FolioID                 ids.ID    `json:"folio_id"`
	FolioVersion            int64     `json:"folio_version"`
	PostingDigest           string    `json:"posting_digest"`
	AutomaticClose          bool      `json:"automatic_close"`
	AuthorizingMembershipID ids.ID    `json:"authorizing_membership_id"`
	AuthorizingRoleID       ids.ID    `json:"authorizing_role_id"`
	ExpiresAt               time.Time `json:"expires_at"`
}

func LatestFolioCloseReview(ctx context.Context, q database.Querier, org, folio ids.ID) (FolioCloseReview, error) {
	raw, e := (&db.Queries{}).GetLatestFolioCloseReview(ctx, q, db.GetLatestFolioCloseReviewParams{OrganisationID: org, FolioID: folio})
	return decodeRecord[FolioCloseReview](raw, e)
}
