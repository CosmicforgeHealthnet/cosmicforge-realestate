package usecases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/storage"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/audit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Rejection reasons.
const (
	ReasonInfected          = "INFECTED"
	ReasonChecksumMismatch  = "CHECKSUM_MISMATCH"
	ReasonSizeMismatch      = "SIZE_MISMATCH"
	ReasonMimeMismatch      = "MIME_MISMATCH"
	ReasonExecutableContent = "EXECUTABLE_CONTENT"
	ReasonObjectMissing     = "OBJECT_MISSING"
	ReasonScanFailed        = "SCAN_FAILED"
)

// ScanDue claims uploaded media and scans them. An upload becomes READY only
// after a CLEAN verdict AND matching size, checksum and detected content
// type — storage acceptance alone never makes a file usable.
func (s *Service) ScanDue(ctx context.Context, owner string, batch int) (int, error) {
	ctx = jobCtx(ctx, "media-scan")
	now := s.now()
	claimed, err := s.q.ClaimForScan(ctx, s.db, db.ClaimForScanParams{Now: now, RowLimit: int32(batch), LeaseOwner: &owner, LeaseExpiresAt: now.Add(s.opts.ScanLease)})
	if err != nil {
		return 0, err
	}
	for _, m := range claimed {
		if err := s.scanOne(ctx, m); err != nil {
			s.log.ErrorContext(ctx, "scan failed", "media_id", m.MediaID.String(), "error", err.Error())
		}
	}
	return len(claimed), nil
}

type inspection struct {
	size     int64
	sha256   string
	detected string
	exec     bool
	verdict  bool // clean
	sig      string
}

func (s *Service) scanOne(ctx context.Context, m db.MediaObject) error {
	// UPLOADED -> SCANNING (visible as in-progress until the scan lease ends).
	if m.Status == string(domain.StatusUploaded) {
		until := s.now().Add(s.opts.ScanLease)
		err := database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			cur, err := s.lock(ctx, tx, m.MediaID)
			if err != nil {
				return err
			}
			m, err = s.transition(ctx, tx, cur, change{to: domain.StatusScanning, scanStatus: "PENDING", nextWorkAt: &until,
				event: EventScanning, actorType: audit.ActorSystem})
			return err
		})
		if err != nil {
			return err
		}
	}
	start := time.Now()
	res, err := s.inspect(ctx, m)
	dur := time.Since(start)
	if errors.Is(err, storage.ErrNotFound) {
		return s.finishScan(ctx, m, res, ReasonObjectMissing, dur)
	}
	if err != nil {
		if int(m.ScanAttempts) >= s.opts.MaxScanTries {
			return s.finishScan(ctx, m, res, ReasonScanFailed, dur)
		}
		backoff := time.Duration(m.ScanAttempts) * 30 * time.Second
		s.log.WarnContext(ctx, "scan attempt failed; will retry", "media_id", m.MediaID.String(), "attempt", m.ScanAttempts, "error", err.Error())
		return s.q.RescheduleWork(ctx, s.db, db.RescheduleWorkParams{MediaID: m.MediaID, NextWorkAt: timePtr(s.now().Add(backoff)),
			UpdatedAt: s.now(), Status: string(domain.StatusScanning)})
	}
	reason := ""
	switch {
	case !res.verdict:
		reason = ReasonInfected
	case res.size != m.SizeBytes:
		reason = ReasonSizeMismatch
	case res.sha256 != m.ChecksumSha256:
		reason = ReasonChecksumMismatch
	case res.exec:
		reason = ReasonExecutableContent
	case res.detected != m.MimeType:
		reason = ReasonMimeMismatch
	}
	return s.finishScan(ctx, m, res, reason, dur)
}

// inspect streams the object once through SHA-256, a size counter, a header
// sniffer and the malware scanner. No transaction is held.
func (s *Service) inspect(ctx context.Context, m db.MediaObject) (inspection, error) {
	rc, _, err := s.store.GetObject(ctx, m.StorageKey)
	if err != nil {
		return inspection{}, err
	}
	defer rc.Close()
	h := sha256.New()
	head := &headCapture{max: 512}
	counter := &countWriter{}
	limited := io.LimitReader(rc, m.SizeBytes+1) // never read beyond declared size + 1
	tee := io.TeeReader(limited, io.MultiWriter(h, head, counter))
	v, err := s.scanner.Scan(ctx, tee)
	if err != nil {
		return inspection{}, err
	}
	_, _ = io.Copy(io.Discard, tee) // ensure the hash covers the whole object
	detected, exec := domain.DetectMIME(head.buf)
	return inspection{size: counter.n, sha256: hex.EncodeToString(h.Sum(nil)), detected: detected, exec: exec, verdict: v.Clean, sig: v.Signature}, nil
}

func (s *Service) finishScan(ctx context.Context, m db.MediaObject, res inspection, reason string, dur time.Duration) error {
	return database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := s.lock(ctx, tx, m.MediaID)
		if err != nil {
			return err
		}
		if cur.Status != string(domain.StatusScanning) {
			return nil // another worker finished first
		}
		now := s.now()
		verdict, scanStatus := "CLEAN", "CLEAN"
		switch reason {
		case "":
		case ReasonInfected:
			verdict, scanStatus = "INFECTED", "INFECTED"
		case ReasonScanFailed:
			verdict, scanStatus = "ERROR", "FAILED"
		default:
			verdict = "REJECTED"
		}
		if err := s.q.InsertScanResult(ctx, tx, db.InsertScanResultParams{ScanID: ids.New(), MediaID: m.MediaID, Scanner: s.scanner.Name(),
			Verdict: verdict, Reason: strPtr(reason), Signature: strPtr(res.sig), DetectedMimeType: strPtr(res.detected),
			ComputedSha256: strPtr(res.sha256), ComputedSize: &res.size, DurationMs: int32(dur.Milliseconds()), ScannedAt: now}); err != nil {
			return err
		}
		if reason == "" {
			rule, err := s.q.GetRetentionRule(ctx, tx, cur.MediaClass)
			if err != nil {
				return err
			}
			retention := now.Add(time.Duration(rule.MinRetentionDays) * 24 * time.Hour)
			_, err = s.transition(ctx, tx, cur, change{to: domain.StatusReady, scanStatus: scanStatus, detectedMime: res.detected,
				readyAt: &now, retention: &retention, event: EventReady, actorType: audit.ActorSystem,
				metadata: map[string]any{"scanner": s.scanner.Name()}})
			return err
		}
		_, err = s.transition(ctx, tx, cur, change{to: domain.StatusRejected, scanStatus: scanStatus, detectedMime: res.detected, reason: reason,
			rejectedAt: &now, event: EventRejected, actorType: audit.ActorSystem, metadata: map[string]any{"scanner": s.scanner.Name(), "signature": res.sig}})
		return err
	})
}

// DeleteDue removes objects of DELETE_PENDING media from storage (outside
// any transaction) and then records DELETED.
func (s *Service) DeleteDue(ctx context.Context, owner string, batch int) (int, error) {
	ctx = jobCtx(ctx, "media-delete")
	now := s.now()
	claimed, err := s.q.ClaimForDeletion(ctx, s.db, db.ClaimForDeletionParams{Now: now, RowLimit: int32(batch), LeaseOwner: &owner, LeaseExpiresAt: now.Add(s.opts.ScanLease)})
	if err != nil {
		return 0, err
	}
	done := 0
	for _, m := range claimed {
		if err := s.store.DeleteObject(ctx, m.StorageKey); err != nil {
			s.log.WarnContext(ctx, "object deletion failed; will retry", "media_id", m.MediaID.String(), "error", err.Error())
			_ = s.q.RescheduleWork(ctx, s.db, db.RescheduleWorkParams{MediaID: m.MediaID, NextWorkAt: timePtr(s.now().Add(time.Minute)),
				UpdatedAt: s.now(), Status: string(domain.StatusDeletePending)})
			continue
		}
		err := database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			cur, err := s.lock(ctx, tx, m.MediaID)
			if err != nil || cur.Status != string(domain.StatusDeletePending) {
				return err
			}
			t := s.now()
			if err := s.q.MarkStorageObjectDeleted(ctx, tx, db.MarkStorageObjectDeletedParams{StorageKey: cur.StorageKey, DeletedAt: &t}); err != nil {
				return err
			}
			if err := s.q.CompleteDeletionRequest(ctx, tx, db.CompleteDeletionRequestParams{MediaID: cur.MediaID, CompletedAt: &t}); err != nil {
				return err
			}
			if _, err := s.q.RevokeAllGrants(ctx, tx, db.RevokeAllGrantsParams{MediaID: cur.MediaID, RevokedAt: &t}); err != nil {
				return err
			}
			_, err = s.transition(ctx, tx, cur, change{to: domain.StatusDeleted, deletedAt: &t, event: EventDeleted, actorType: audit.ActorSystem})
			return err
		})
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// PurgeRejected schedules deletion of REJECTED media past the class's
// auto-delete window (retention rules).
func (s *Service) PurgeRejected(ctx context.Context, batch int) (int, error) {
	ctx = jobCtx(ctx, "media-purge")
	n := 0
	err := database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		n = 0
		rows, err := s.q.ClaimRejectedForPurge(ctx, tx, db.ClaimRejectedForPurgeParams{Now: s.now(), RowLimit: int32(batch)})
		if err != nil {
			return err
		}
		for _, m := range rows {
			if _, err := s.markDeletePending(ctx, tx, m, "media-document-service", domain.Principal{}, "AUTO_PURGE_REJECTED", audit.ActorSystem); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ExpireUploadSessions closes upload sessions past their deadline.
func (s *Service) ExpireUploadSessions(ctx context.Context) (int64, error) {
	return s.q.ExpireUploadSessions(ctx, s.db, s.now())
}

type headCapture struct {
	buf []byte
	max int
}

func (h *headCapture) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		h.buf = append(h.buf, p[:room]...)
	}
	return len(p), nil
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
