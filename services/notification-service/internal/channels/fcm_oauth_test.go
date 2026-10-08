package channels

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The service-account flow signs a JWT assertion with the private key and
// exchanges it at token_uri; the resulting access token authorizes sends.
func TestFCMServiceAccountTokenFlow(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || strings.Count(r.Form.Get("assertion"), ".") != 2 {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"sa-access-token","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	creds, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "proj-1", "private_key_id": "k1",
		"private_key": pemKey, "client_email": "fcm@proj-1.iam.gserviceaccount.com", "token_uri": tokenSrv.URL})
	ts, err := FCMTokenSourceFromJSON(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	fcmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sa-access-token" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"name":"projects/proj-1/messages/1"}`)
	}))
	defer fcmSrv.Close()
	f, _ := NewFCM(FCMConfig{ProjectID: "proj-1", BaseURL: fcmSrv.URL, Tokens: ts})
	if r, err := f.SendPush(context.Background(), Message{Destination: "device-token-abcdef"}); err != nil || r.ProviderMessageID == "" {
		t.Fatalf("%+v %v", r, err)
	}
	// Other credential types (e.g. external_account) are refused.
	other, _ := json.Marshal(map[string]string{"type": "external_account", "audience": "x"})
	if _, err := FCMTokenSourceFromJSON(context.Background(), other); err == nil {
		t.Fatal("only service_account credentials are accepted")
	}
}
