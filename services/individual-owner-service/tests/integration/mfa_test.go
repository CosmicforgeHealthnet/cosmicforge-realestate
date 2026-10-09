package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/credentials"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func totp(secret []byte, at time.Time, offset int64) string {
	return credentials.TOTP{}.Code(secret, credentials.Step(at)+offset)
}

func TestTOTPEnrolmentMFALoginRecoveryAndStepUp(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	// Enrolment requires fresh step-up: the sign-in counts for 5 minutes.
	h.clock.advance(6 * time.Minute)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/mfa/totp/setup", token: o.access}), 403, "STEP_UP_REQUIRED")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/step-up", token: o.access,
		body: map[string]any{"method": "PASSWORD", "password": "wrong"}}), 401, "INVALID_CREDENTIALS")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/step-up", token: o.access,
		body: map[string]any{"method": "PASSWORD", "password": o.password}}), 200, "")
	r := h.do(call{method: "POST", path: "/api/v1/owner/me/mfa/totp/setup", token: o.access})
	h.expect(r, 201, "")
	var setup usecases.TOTPSetupView
	r.data(t, &setup)
	if !strings.HasPrefix(setup.OTPAuthURI, "otpauth://totp/") || setup.Secret == "" {
		t.Fatalf("%+v", setup)
	}
	secret := h.totpSecret(o)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/mfa/totp/confirm", token: o.access, body: map[string]any{"code": "123456"}}), 422, "INVALID_VERIFICATION_CODE")
	r = h.do(call{method: "POST", path: "/api/v1/owner/me/mfa/totp/confirm", token: o.access, body: map[string]any{"code": totp(secret, h.clock.now(), 0)}})
	h.expect(r, 200, "")
	var rc usecases.RecoveryCodesView
	r.data(t, &rc)
	if len(rc.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes %d", len(rc.RecoveryCodes))
	}
	if h.count(`SELECT count(*) FROM mfa_recovery_codes WHERE account_id=$1 AND code_hash LIKE '%-%'`, o.id) != 0 {
		t.Fatal("recovery codes must be hashed")
	}

	// Password sign-in now requires a second factor.
	r, a := h.login(o, o.device)
	h.expect(r, 200, "")
	if a.Status != "MFA_REQUIRED" || a.Tokens != nil || len(a.MFA.Methods) < 2 {
		t.Fatalf("%+v", a)
	}
	verify := func(tok, method, code string) result {
		return h.do(call{method: "POST", path: "/api/v1/owner/auth/mfa/verify", headers: map[string]string{"X-Device-ID": o.device},
			body: map[string]any{"mfa_token": tok, "method": method, "code": code}})
	}
	h.expect(verify(a.MFA.MFAToken, "TOTP", "000000"), 401, "INVALID_CREDENTIALS")
	h.expect(verify(a.MFA.MFAToken[:40]+"x.bad", "TOTP", "000000"), 422, "INVALID_MFA_TOKEN")
	// TOTP codes are single-use: the confirmation step already used step 0.
	h.expect(verify(a.MFA.MFAToken, "TOTP", totp(secret, h.clock.now(), 0)), 401, "INVALID_CREDENTIALS")
	h.clock.advance(30 * time.Second)
	r = verify(a.MFA.MFAToken, "TOTP", totp(secret, h.clock.now(), 0))
	h.expect(r, 200, "")
	var done authResult
	r.data(t, &done)
	if done.Status != "AUTHENTICATED" || done.Tokens == nil {
		t.Fatalf("%+v", done)
	}
	if h.count(`SELECT count(*) FROM auth_sessions WHERE id=$1 AND mfa_method='TOTP'`, done.Tokens.SessionID) != 1 {
		t.Fatal("session must record the MFA method")
	}
	// The MFA token is single-use.
	h.expect(verify(a.MFA.MFAToken, "TOTP", totp(secret, h.clock.now(), 1)), 422, "INVALID_MFA_TOKEN")

	// Recovery codes work once.
	_, b := h.login(o, o.device)
	h.expect(verify(b.MFA.MFAToken, "RECOVERY_CODE", strings.ToUpper(rc.RecoveryCodes[0])), 200, "")
	_, c := h.login(o, o.device)
	h.expect(verify(c.MFA.MFAToken, "RECOVERY_CODE", rc.RecoveryCodes[0]), 401, "INVALID_CREDENTIALS")

	// Email second factor through notification-service.
	r = h.do(call{method: "POST", path: "/api/v1/owner/auth/mfa/send", body: map[string]any{"mfa_token": c.MFA.MFAToken, "method": "EMAIL"}})
	h.expect(r, 202, "")
	h.expect(verify(c.MFA.MFAToken, "EMAIL", h.notify.lastCode(t, o.email)), 200, "")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/mfa/send", body: map[string]any{"mfa_token": c.MFA.MFAToken, "method": "SMS"}}), 422, "INVALID_MFA_TOKEN")

	// MFA challenge attempts are bounded.
	_, d := h.login(o, o.device)
	for i := 0; i < 5; i++ {
		verify(d.MFA.MFAToken, "TOTP", "999999")
	}
	h.clock.advance(16 * time.Minute) // let the lockout from failures lapse
	h.clock.advance(30 * time.Second)
	h.expect(verify(d.MFA.MFAToken, "TOTP", totp(secret, h.clock.now(), 0)), 422, "INVALID_MFA_TOKEN")

	// With MFA enabled a password is not a valid step-up; TOTP is.
	_, e := h.login(o, o.device)
	r = verify(e.MFA.MFAToken, "RECOVERY_CODE", rc.RecoveryCodes[1])
	var sess authResult
	r.data(t, &sess)
	h.clock.advance(6 * time.Minute)
	tok := sess.Tokens.AccessToken
	h.expect(h.do(call{method: "DELETE", path: "/api/v1/owner/me/mfa/totp", token: tok}), 403, "STEP_UP_REQUIRED")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/step-up", token: tok, body: map[string]any{"method": "PASSWORD", "password": o.password}}), 422, "STEP_UP_METHOD_UNAVAILABLE")
	r = h.do(call{method: "POST", path: "/api/v1/owner/auth/step-up", token: tok, body: map[string]any{"method": "TOTP", "code": totp(secret, h.clock.now(), 0)}})
	h.expect(r, 200, "")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/mfa/recovery-codes", token: tok}), 200, "")
	if h.count(`SELECT count(*) FROM mfa_recovery_codes WHERE account_id=$1 AND used_at IS NULL AND invalidated_at IS NULL`, o.id) != 10 {
		t.Fatal("regeneration must replace codes")
	}
	h.expect(h.do(call{method: "DELETE", path: "/api/v1/owner/me/mfa/totp", token: tok}), 204, "")
	r, f := h.login(o, o.device)
	if f.Status != "AUTHENTICATED" {
		t.Fatalf("MFA should be off: %s", r.body)
	}
	if n := h.count(`SELECT count(*) FROM notification_dispatches WHERE account_id=$1`, o.id); n < 3 {
		t.Fatalf("security alerts queued: %d", n)
	}
}

func TestPasswordChangeForgotResetAndNoEnumeration(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	_, other := h.login(o, "laptop-device-1")
	// Change: wrong current password counts as a failure.
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/password", token: o.access,
		body: map[string]any{"current_password": "nope", "new_password": "brand new password"}}), 401, "INVALID_CREDENTIALS")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/password", token: o.access,
		body: map[string]any{"current_password": o.password, "new_password": "short"}}), 400, "INVALID_REQUEST")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/password", token: o.access,
		body: map[string]any{"current_password": o.password, "new_password": "brand new password"}}), 204, "")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: other.Tokens.AccessToken}), 401, "SESSION_REVOKED")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access}), 200, "")
	o.password = "brand new password"

	// Forgot: identical responses for unknown accounts.
	unknown := h.do(call{method: "POST", path: "/api/v1/owner/auth/password/forgot", body: map[string]any{"email": "ghost@example.com"}})
	known := h.do(call{method: "POST", path: "/api/v1/owner/auth/password/forgot", body: map[string]any{"email": o.email}})
	h.expect(unknown, 202, "")
	h.expect(known, 202, "")
	var ku, kk usecases.ChallengeView
	unknown.data(t, &ku)
	known.data(t, &kk)
	if ku.Channel != kk.Channel || ku.DeliveryStatus != kk.DeliveryStatus || (ku.DestinationHint == "") != (kk.DestinationHint == "") {
		t.Fatalf("responses differ: %+v vs %+v", ku, kk)
	}
	h.app.Service.Wait() // background delivery
	code := h.notify.lastCode(t, o.email)
	reset := func(id ids.ID, code string) result {
		return h.do(call{method: "POST", path: "/api/v1/owner/auth/password/reset",
			body: map[string]any{"challenge_id": id, "code": code, "new_password": "recovered password 1"}})
	}
	h.expect(reset(ku.ChallengeID, code), 422, "INVALID_VERIFICATION_CODE")
	h.expect(reset(kk.ChallengeID, "000000"), 422, "INVALID_VERIFICATION_CODE")
	h.expect(reset(kk.ChallengeID, code), 200, "")
	h.expect(reset(kk.ChallengeID, code), 422, "INVALID_VERIFICATION_CODE")
	h.expect(h.do(call{method: "GET", path: "/api/v1/owner/me", token: o.access}), 401, "SESSION_REVOKED")
	o.password = "recovered password 1"
	r, _ := h.login(o, o.device)
	h.expect(r, 200, "")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/password/forgot", body: map[string]any{"email": o.email, "phone": "+2348000000000"}}), 400, "INVALID_REQUEST")
}

func TestCodeAttemptsAreBounded(t *testing.T) {
	h := newHarness(t)
	o := h.register(false)
	r := h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification", token: o.access})
	var ch usecases.ChallengeView
	r.data(t, &ch)
	for i := 0; i < 5; i++ {
		h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access, body: map[string]any{"challenge_id": ch.ChallengeID, "code": "000000"}})
	}
	code := h.notify.lastCode(t, o.email)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": code}}), 422, "INVALID_VERIFICATION_CODE")
	// Expiry.
	r = h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification", token: o.access})
	r.data(t, &ch)
	code = h.notify.lastCode(t, o.email)
	h.clock.advance(11 * time.Minute)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification/confirm", token: o.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": code}}), 422, "INVALID_VERIFICATION_CODE")
}

func TestPhoneVerificationAndOTPLogin(t *testing.T) {
	h := newHarness(t)
	o := h.register(true)
	phone := "+2348031234567"
	r := h.do(call{method: "POST", path: "/api/v1/owner/me/phone/verification", token: o.access, body: map[string]any{"phone": phone}})
	h.expect(r, 202, "")
	var ch usecases.ChallengeView
	r.data(t, &ch)
	if ch.Channel != "SMS" || !strings.HasSuffix(ch.DestinationHint, "4567") || strings.Contains(ch.DestinationHint, "803") {
		t.Fatalf("%+v", ch)
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/phone/verification/confirm", token: o.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": h.notify.lastCode(t, phone)}}), 200, "")
	// A second account cannot claim the same verified phone.
	p := h.register(true)
	r = h.do(call{method: "POST", path: "/api/v1/owner/me/phone/verification", token: p.access, body: map[string]any{"phone": phone}})
	r.data(t, &ch)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/phone/verification/confirm", token: p.access,
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": h.notify.lastCode(t, phone)}}), 409, "PHONE_ALREADY_REGISTERED")

	// OTP sign-in; unknown numbers get an indistinguishable response.
	ghost := h.do(call{method: "POST", path: "/api/v1/owner/auth/otp/request", body: map[string]any{"phone": "+2348099999999"}})
	h.expect(ghost, 202, "")
	r = h.do(call{method: "POST", path: "/api/v1/owner/auth/otp/request", body: map[string]any{"phone": phone}})
	h.expect(r, 202, "")
	r.data(t, &ch)
	h.app.Service.Wait()
	var g usecases.ChallengeView
	ghost.data(t, &g)
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/otp/verify", body: map[string]any{"challenge_id": g.ChallengeID, "code": "123456"}}), 422, "INVALID_VERIFICATION_CODE")
	r = h.do(call{method: "POST", path: "/api/v1/owner/auth/otp/verify", headers: map[string]string{"X-Device-ID": o.device},
		body: map[string]any{"challenge_id": ch.ChallengeID, "code": h.notify.lastCode(t, phone)}})
	h.expect(r, 200, "")
	var a authResult
	r.data(t, &a)
	if a.Status != "AUTHENTICATED" || h.count(`SELECT count(*) FROM auth_sessions WHERE id=$1 AND auth_method='PHONE_OTP'`, a.Tokens.SessionID) != 1 {
		t.Fatalf("%s", r.body)
	}
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/auth/otp/request", body: map[string]any{"phone": "0803"}}), 400, "INVALID_REQUEST")
}

func TestNotificationOutageIsReportedNotFaked(t *testing.T) {
	h := newHarness(t)
	o := h.register(false)
	h.notify.set("down")
	r := h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification", token: o.access})
	h.expect(r, 503, "NOTIFICATION_UNAVAILABLE")
	if h.count(`SELECT count(*) FROM auth_challenges WHERE account_id=$1 AND delivery_status='UNKNOWN'`, o.id) != 1 {
		t.Fatal("unknown outcome must be recorded")
	}
	h.notify.set("reject")
	h.expect(h.do(call{method: "POST", path: "/api/v1/owner/me/email/verification", token: o.access}), 503, "NOTIFICATION_UNAVAILABLE")
	if h.count(`SELECT count(*) FROM auth_challenges WHERE account_id=$1 AND delivery_status='FAILED'`, o.id) != 1 {
		t.Fatal("definite failure must be recorded")
	}
	// Definitely rejected alerts are dead-lettered, not retried forever.
	h.notify.set("")
	h.login(o, "other-device-123")
	h.notify.set("reject")
	if err := h.app.WorkerRunner(nil, "w").Once(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.count(`SELECT count(*) FROM notification_dispatches WHERE account_id=$1 AND status='DEAD'`, o.id) != 1 {
		t.Fatal("rejected alert must be dead-lettered")
	}
	st, _ := h.app.Service.DispatchQueueStats(t.Context())
	if st.Dead != 1 {
		t.Fatalf("%+v", st)
	}
}
