package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
)

func etag(v int64) string { return `"` + strconv.FormatInt(v, 10) + `"` }

// ifMatch parses If-Match: "<version>" (weak validators are not accepted).
func ifMatch(r *http.Request) (int64, error) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return 0, domain.ErrPreconditionRequired()
	}
	n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
	if err != nil || n <= 0 {
		return 0, apperrors.InvalidRequest("If-Match must be the quoted account version, e.g. \"3\".")
	}
	return n, nil
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.GetAccount(r.Context(), ownerSubject(r))
	if err == nil {
		w.Header().Set("ETag", etag(v.Version))
	}
	write(w, r, http.StatusOK, v, err)
}

type patchMeRequest struct {
	DisplayName *string `json:"display_name"`
	Locale      *string `json:"locale"`
}

func (h *Handler) patchMe(w http.ResponseWriter, r *http.Request) {
	ver, err := ifMatch(r)
	var b patchMeRequest
	if err == nil {
		err = decode(w, r, &b)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.UpdateProfile(r.Context(), ownerSubject(r), usecases.UpdateProfileInput{DisplayName: b.DisplayName, Locale: b.Locale, ExpectedVersion: ver})
	if err == nil {
		w.Header().Set("ETag", etag(v.Version))
	}
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) startEmailVerification(w http.ResponseWriter, r *http.Request) {
	if err := decode(w, r, &struct{}{}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.StartEmailVerification(r.Context(), ownerSubject(r))
	write(w, r, http.StatusAccepted, v, err)
}

type emailRequest struct {
	Email string `json:"email"`
}

func (h *Handler) startEmailChange(w http.ResponseWriter, r *http.Request) {
	var b emailRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.StartEmailChange(r.Context(), ownerSubject(r), b.Email)
	write(w, r, http.StatusAccepted, v, err)
}

func (h *Handler) startPhoneVerification(w http.ResponseWriter, r *http.Request) {
	var b otpRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.StartPhoneVerification(r.Context(), ownerSubject(r), b.Phone)
	write(w, r, http.StatusAccepted, v, err)
}

func (h *Handler) confirmContact(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b codeRequest
		err := decode(w, r, &b)
		if err == nil {
			err = b.validate()
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		v, err := h.svc.ConfirmContact(r.Context(), ownerSubject(r), kind, b.ChallengeID, b.Code, clientInfo(r))
		write(w, r, http.StatusOK, v, err)
	}
}

func (h *Handler) confirmEmail(w http.ResponseWriter, r *http.Request) {
	h.confirmContact("email")(w, r)
}
func (h *Handler) confirmPhone(w http.ResponseWriter, r *http.Request) {
	h.confirmContact("phone")(w, r)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var b changePasswordRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	write(w, r, http.StatusNoContent, nil, h.svc.ChangePassword(r.Context(), ownerSubject(r), b.CurrentPassword, b.NewPassword, clientInfo(r)))
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.ListSessions(r.Context(), ownerSubject(r))
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "session_id", "SESSION_NOT_FOUND", "Session not found.")
	if err == nil {
		err = h.svc.RevokeSession(r.Context(), ownerSubject(r), id, clientInfo(r))
	}
	write(w, r, http.StatusNoContent, nil, err)
}

func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.ListDevices(r.Context(), ownerSubject(r))
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) trustDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "device_id", "DEVICE_NOT_FOUND", "Device not found.")
	var v usecases.DeviceView
	if err == nil {
		v, err = h.svc.TrustDevice(r.Context(), ownerSubject(r), id, clientInfo(r))
	}
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "device_id", "DEVICE_NOT_FOUND", "Device not found.")
	if err == nil {
		err = h.svc.RevokeDevice(r.Context(), ownerSubject(r), id, clientInfo(r))
	}
	write(w, r, http.StatusNoContent, nil, err)
}

func (h *Handler) listSecurityEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := pagination.Limit(r.URL.Query().Get("limit"), 20, 100)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.ListSecurityEvents(r.Context(), ownerSubject(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) getMFA(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.MFAStatus(r.Context(), ownerSubject(r))
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) setupTOTP(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.SetupTOTP(r.Context(), ownerSubject(r))
	write(w, r, http.StatusCreated, v, err)
}

type totpConfirmRequest struct {
	Code string `json:"code"`
}

func (h *Handler) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	var b totpConfirmRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.svc.ConfirmTOTP(r.Context(), ownerSubject(r), b.Code, clientInfo(r))
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) disableTOTP(w http.ResponseWriter, r *http.Request) {
	write(w, r, http.StatusNoContent, nil, h.svc.DisableTOTP(r.Context(), ownerSubject(r), clientInfo(r)))
}

func (h *Handler) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.RegenerateRecoveryCodes(r.Context(), ownerSubject(r), clientInfo(r))
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) requestClosure(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.RequestClosure(r.Context(), ownerSubject(r), clientInfo(r))
	write(w, r, http.StatusOK, v, err)
}

func (h *Handler) cancelClosure(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.CancelClosure(r.Context(), ownerSubject(r), clientInfo(r))
	write(w, r, http.StatusOK, v, err)
}
