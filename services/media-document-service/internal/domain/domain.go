// Package domain holds the media/document model: lifecycle state machine,
// class policies (MIME types, size limits, allowed classifications), access
// rules and content inspection.
package domain

import (
	"bytes"
	"path"
	"strings"
	"unicode"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/fsm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Status is a media lifecycle state.
type Status string

const (
	StatusInitiated     Status = "INITIATED"
	StatusUploadPending Status = "UPLOAD_PENDING"
	StatusUploaded      Status = "UPLOADED"
	StatusScanning      Status = "SCANNING"
	StatusReady         Status = "READY"
	StatusRejected      Status = "REJECTED"
	StatusDeletePending Status = "DELETE_PENDING"
	StatusDeleted       Status = "DELETED"
)

// Machine is the approved media state machine:
//
//	INITIATED -> UPLOAD_PENDING -> UPLOADED -> SCANNING -> READY | REJECTED
//	READY -> DELETE_PENDING -> DELETED
//	REJECTED -> DELETE_PENDING -> DELETED
var Machine = fsm.New(map[Status][]Status{
	StatusInitiated:     {StatusUploadPending},
	StatusUploadPending: {StatusUploaded},
	StatusUploaded:      {StatusScanning},
	StatusScanning:      {StatusReady, StatusRejected},
	StatusReady:         {StatusDeletePending},
	StatusRejected:      {StatusDeletePending},
	StatusDeletePending: {StatusDeleted},
})

// Classification controls who may access a file.
type Classification string

const (
	Public           Classification = "PUBLIC"
	Private          Classification = "PRIVATE"
	DomainRestricted Classification = "DOMAIN_RESTRICTED"
	AdminRestricted  Classification = "ADMIN_RESTRICTED"
)

// Class is a media/document class with its own policy.
type Class string

const (
	ClassImage                Class = "IMAGE"
	ClassVideo                Class = "VIDEO"
	ClassDocument             Class = "DOCUMENT"
	ClassVerificationDocument Class = "VERIFICATION_DOCUMENT"
)

// ClassPolicy constrains uploads of a class.
type ClassPolicy struct {
	MimeTypes       map[string][]string // allowed MIME -> allowed filename extensions
	MaxBytes        int64
	Classifications []Classification
	Default         Classification
}

// DefaultPolicies is the platform baseline. Executable and archive formats
// are not allowed in any class.
func DefaultPolicies() map[Class]ClassPolicy {
	img := map[string][]string{
		"image/jpeg": {".jpg", ".jpeg"}, "image/png": {".png"}, "image/webp": {".webp"}, "image/heic": {".heic", ".heif"},
	}
	doc := map[string][]string{"application/pdf": {".pdf"}, "image/jpeg": {".jpg", ".jpeg"}, "image/png": {".png"}}
	return map[Class]ClassPolicy{
		ClassImage: {MimeTypes: img, MaxBytes: 15 << 20, Classifications: []Classification{Public, Private, DomainRestricted}, Default: Private},
		ClassVideo: {MimeTypes: map[string][]string{"video/mp4": {".mp4"}}, MaxBytes: 200 << 20,
			Classifications: []Classification{Public, Private, DomainRestricted}, Default: Private},
		ClassDocument: {MimeTypes: doc, MaxBytes: 25 << 20, Classifications: []Classification{Private, DomainRestricted, AdminRestricted}, Default: Private},
		ClassVerificationDocument: {MimeTypes: doc, MaxBytes: 15 << 20,
			Classifications: []Classification{DomainRestricted, AdminRestricted}, Default: AdminRestricted},
	}
}

// Error codes owned by media-document-service.
const (
	CodeNotFound                 = "MEDIA_NOT_FOUND"
	CodeInvalidClass             = "INVALID_MEDIA_CLASS"
	CodeMimeNotAllowed           = "MIME_TYPE_NOT_ALLOWED"
	CodeExtensionMismatch        = "FILE_EXTENSION_MISMATCH"
	CodeTooLarge                 = "FILE_TOO_LARGE"
	CodeInvalidChecksum          = "INVALID_CHECKSUM"
	CodeClassificationNotAllowed = "ACCESS_CLASSIFICATION_NOT_ALLOWED"
	CodeUploadIncomplete         = "UPLOAD_NOT_RECEIVED"
	CodeUploadMismatch           = "UPLOAD_MISMATCH"
	CodeUploadExpired            = "UPLOAD_SESSION_EXPIRED"
	CodeNotReady                 = "MEDIA_NOT_READY"
	CodeRetentionActive          = "RETENTION_PERIOD_ACTIVE"
	CodeOwnerDomain              = "OWNER_DOMAIN_MISMATCH"
)

// ErrNotFound hides missing or inaccessible media.
func ErrNotFound() *apperrors.Error { return apperrors.NotFound(CodeNotFound, "Media not found.") }

// Validate checks declared metadata against the class policy. The filename
// extension is checked for consistency but never trusted on its own: the
// content is sniffed again during scanning.
func (p ClassPolicy) Validate(mime, filename string, size int64, classification Classification) error {
	exts, ok := p.MimeTypes[mime]
	if !ok {
		return apperrors.Unprocessable(CodeMimeNotAllowed, "This file type is not allowed for this media class.")
	}
	ext := strings.ToLower(path.Ext(filename))
	match := false
	for _, e := range exts {
		if e == ext {
			match = true
		}
	}
	if !match {
		return apperrors.Unprocessable(CodeExtensionMismatch, "The filename extension does not match the declared type.")
	}
	if size <= 0 || size > p.MaxBytes {
		return apperrors.Unprocessable(CodeTooLarge, "The file exceeds the size limit for this media class.")
	}
	for _, c := range p.Classifications {
		if c == classification {
			return nil
		}
	}
	return apperrors.Unprocessable(CodeClassificationNotAllowed, "This access classification is not allowed for this media class.")
}

// SanitizeFilename keeps a safe display name: base name only, no control or
// path characters, at most 255 bytes. Storage keys never use it.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r), r == '/', r == '"', r == '<', r == '>', r == '|', r == ':', r == '*', r == '?':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.TrimLeft(out, ".")
	if len(out) > 255 {
		ext := path.Ext(out)
		out = out[:255-len(ext)] + ext
	}
	if out == "" || out == "." {
		out = "file"
	}
	return out
}

// StorageKey builds the server-generated object key (no user input).
func StorageKey(ownerDomain string, class Class, id ids.ID) string {
	t := id.Time()
	return ownerDomain + "/" + strings.ToLower(string(class)) + "/" + t.Format("2006/01") + "/" + id.String()
}

// Owner is a domain-aware owner reference.
type Owner struct {
	Domain platform.Domain `json:"owner_domain"`
	Type   string          `json:"owner_type"`
	ID     ids.ID          `json:"owner_id"`
}

// Principal is the authenticated requester.
type Principal struct {
	Service     platform.Service
	ActorDomain string // empty when no delegated user
	ActorID     ids.ID
}

// HasActor reports whether a user is delegated.
func (p Principal) HasActor() bool { return p.ActorDomain != "" && !p.ActorID.IsZero() }

// IsAdmin reports admin-service acting for an authenticated admin.
func (p Principal) IsAdmin() bool {
	return p.Service == platform.AdminService && p.ActorDomain == string(platform.DomainAdmin) && !p.ActorID.IsZero()
}

// Grant is an explicit read grant.
type Grant struct {
	Service string
	Domain  string
	ActorID ids.ID
	IsActor bool
}

// MediaRef is the information access decisions need.
type MediaRef struct {
	Owner          Owner
	Class          Class
	Classification Classification
}

// ownerService reports whether the principal is the owner's domain service
// (acting for the domain, with or without a delegated user of that domain).
func ownerService(m MediaRef, p Principal) bool {
	d, ok := platform.DomainForService(p.Service)
	return ok && d == m.Owner.Domain && (!p.HasActor() || p.ActorDomain == string(m.Owner.Domain))
}

func ownerUser(m MediaRef, p Principal) bool {
	return p.Service == platform.APIGateway && p.HasActor() && p.ActorDomain == string(m.Owner.Domain) &&
		m.Owner.Type == "USER" && p.ActorID == m.Owner.ID
}

func granted(p Principal, grants []Grant) bool {
	for _, g := range grants {
		if !g.IsActor && g.Service == string(p.Service) {
			return true
		}
		if g.IsActor && p.HasActor() && g.Domain == p.ActorDomain && g.ActorID == p.ActorID {
			return true
		}
	}
	return false
}

// CanReadMetadata decides metadata visibility. verification-service may read
// metadata of any media (evidence validation only needs metadata, never bytes).
func CanReadMetadata(m MediaRef, p Principal, grants []Grant) bool {
	if p.Service == platform.VerificationService || p.IsAdmin() || p.Service == platform.AdminService {
		return true
	}
	return CanDownload(m, p, grants)
}

// CanDownload decides whether a signed download URL may be issued. A signed
// URL never decides authorization by itself; this check does.
//
//	PUBLIC             any authenticated principal
//	PRIVATE            owner user, owner domain service, admin, explicit grants
//	DOMAIN_RESTRICTED  owner domain service, admin, explicit grants (not end users directly)
//	ADMIN_RESTRICTED   admin only, plus explicit service grants; verification-service
//	                   for VERIFICATION_DOCUMENT evidence
func CanDownload(m MediaRef, p Principal, grants []Grant) bool {
	switch m.Classification {
	case Public:
		return true
	case Private:
		return ownerUser(m, p) || ownerService(m, p) || p.IsAdmin() || granted(p, grants)
	case DomainRestricted:
		return ownerService(m, p) || p.IsAdmin() || granted(p, grants) ||
			(m.Class == ClassVerificationDocument && p.Service == platform.VerificationService)
	case AdminRestricted:
		if p.IsAdmin() {
			return true
		}
		if m.Class == ClassVerificationDocument && p.Service == platform.VerificationService {
			return true
		}
		for _, g := range grants {
			if !g.IsActor && g.Service == string(p.Service) {
				return true
			}
		}
	}
	return false
}

// CanManage decides lifecycle/policy changes: the owner domain's service or admin.
func CanManage(m MediaRef, p Principal) bool { return ownerService(m, p) || p.IsAdmin() }

// Privileged reports whether downloading requires a privileged-access audit.
func Privileged(c Classification) bool { return c == DomainRestricted || c == AdminRestricted }

// Content inspection --------------------------------------------------------

// DetectMIME identifies the real content type from leading bytes. It returns
// "" for unknown content and reports executables/archives explicitly.
func DetectMIME(head []byte) (mime string, executable bool) {
	switch {
	case bytes.HasPrefix(head, []byte("MZ")), bytes.HasPrefix(head, []byte("\x7fELF")),
		bytes.HasPrefix(head, []byte("#!")), bytes.HasPrefix(head, []byte("\xca\xfe\xba\xbe")),
		bytes.HasPrefix(head, []byte("\xfe\xed\xfa\xce")), bytes.HasPrefix(head, []byte("\xfe\xed\xfa\xcf")),
		bytes.HasPrefix(head, []byte("\xcf\xfa\xed\xfe")), bytes.HasPrefix(head, []byte("\xce\xfa\xed\xfe")):
		return "application/x-executable", true
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("Rar!")), bytes.HasPrefix(head, []byte("7z\xbc\xaf")):
		return "application/zip", true
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "application/pdf", false
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return "image/jpeg", false
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", false
	case len(head) >= 12 && bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp", false
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		brand := string(head[8:12])
		switch brand {
		case "heic", "heix", "hevc", "mif1", "msf1", "heim", "heis":
			return "image/heic", false
		case "isom", "iso2", "mp41", "mp42", "avc1", "M4V ", "dash":
			return "video/mp4", false
		}
	}
	return "", false
}
