package http

import (
	"net/http"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/domain"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	Phone       string `json:"phone"`
	DisplayName string `json:"display_name"`
	Locale      string `json:"locale"`
}

type registerResponse struct {
	usecases.AuthResult
	EmailVerification *usecases.ChallengeView `json:"email_verification"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var b registerRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, ch, err := h.svc.Register(r.Context(), usecases.RegisterInput{Email: b.Email, Password: b.Password, Phone: b.Phone,
		DisplayName: b.DisplayName, Locale: b.Locale}, clientInfo(r))
	write(w, r, http.StatusCreated, registerResponse{AuthResult: res, EmailVerification: ch}, err)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var b loginRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Login(r.Context(), b.Email, b.Password, clientInfo(r))
	write(w, r, http.StatusOK, res, err)
}

type otpRequest struct {
	Phone string `json:"phone"`
}

func (h *Handler) requestOTP(w http.ResponseWriter, r *http.Request) {
	var b otpRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.RequestLoginOTP(r.Context(), b.Phone)
	write(w, r, http.StatusAccepted, res, err)
}

type codeRequest struct {
	ChallengeID ids.ID `json:"challenge_id"`
	Code        string `json:"code"`
}

func (b codeRequest) validate() error {
	if b.ChallengeID.ValidateV7() != nil || b.Code == "" || len(b.Code) > 32 {
		return apperrors.InvalidRequest("challenge_id (UUIDv7) and code are required.")
	}
	return nil
}

func (h *Handler) verifyOTP(w http.ResponseWriter, r *http.Request) {
	var b codeRequest
	err := decode(w, r, &b)
	if err == nil {
		err = b.validate()
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.VerifyLoginOTP(r.Context(), b.ChallengeID, b.Code, clientInfo(r))
	write(w, r, http.StatusOK, res, err)
}

type mfaRequest struct {
	MFAToken string `json:"mfa_token"`
	Method   string `json:"method"`
	Code     string `json:"code"`
}

func (h *Handler) sendMFA(w http.ResponseWriter, r *http.Request) {
	var b mfaRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.SendMFACode(r.Context(), b.MFAToken, b.Method)
	write(w, r, http.StatusAccepted, res, err)
}

func (h *Handler) verifyMFA(w http.ResponseWriter, r *http.Request) {
	var b mfaRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if b.Code == "" || len(b.Code) > 32 {
		httpx.WriteError(w, r, apperrors.InvalidRequest("code is required."))
		return
	}
	res, err := h.svc.VerifyMFA(r.Context(), b.MFAToken, b.Method, b.Code, clientInfo(r))
	write(w, r, http.StatusOK, res, err)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var b refreshRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Refresh(r.Context(), b.RefreshToken, clientInfo(r))
	write(w, r, http.StatusOK, res, err)
}

type forgotRequest struct {
	Email string `json:"email"`
	Phone string `json:"phone"`
}

func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var b forgotRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.ForgotPassword(r.Context(), b.Email, b.Phone)
	write(w, r, http.StatusAccepted, res, err)
}

type resetRequest struct {
	ChallengeID ids.ID `json:"challenge_id"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var b resetRequest
	err := decode(w, r, &b)
	if err == nil {
		err = codeRequest{ChallengeID: b.ChallengeID, Code: b.Code}.validate()
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	err = h.svc.ResetPassword(r.Context(), b.ChallengeID, b.Code, b.NewPassword, clientInfo(r))
	write(w, r, http.StatusOK, map[string]string{"status": "PASSWORD_RESET"}, err)
}

func (h *Handler) oauthProviders(w http.ResponseWriter, r *http.Request) {
	httpx.WriteData(w, http.StatusOK, map[string]any{"providers": h.svc.OAuthProviders()})
}

// oauthUnavailable: no OAuth/OIDC adapter is certified or configured, so the
// flow is explicitly unavailable rather than pretending to work.
func (h *Handler) oauthUnavailable(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, domain.ErrFeatureUnavailable("OAUTH_PROVIDER_UNAVAILABLE", "This sign-in provider is not available."))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	write(w, r, http.StatusNoContent, nil, h.svc.Logout(r.Context(), ownerSubject(r), clientInfo(r)))
}

func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.LogoutAll(r.Context(), ownerSubject(r), clientInfo(r))
	write(w, r, http.StatusOK, map[string]int64{"revoked_sessions": n}, err)
}

type stepUpRequest struct {
	Method      string `json:"method"`
	Password    string `json:"password"`
	Code        string `json:"code"`
	ChallengeID ids.ID `json:"challenge_id"`
}

func (h *Handler) stepUp(w http.ResponseWriter, r *http.Request) {
	var b stepUpRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.StepUp(r.Context(), ownerSubject(r), usecases.StepUpInput{Method: b.Method, Password: b.Password, Code: b.Code, ChallengeID: b.ChallengeID}, clientInfo(r))
	write(w, r, http.StatusOK, res, err)
}

type methodRequest struct {
	Method string `json:"method"`
}

func (h *Handler) stepUpChallenge(w http.ResponseWriter, r *http.Request) {
	var b methodRequest
	if err := decode(w, r, &b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.StartStepUpCode(r.Context(), ownerSubject(r), b.Method)
	write(w, r, http.StatusAccepted, res, err)
}
