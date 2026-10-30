package server

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/routing"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestBusinessGatewaySecurityAndTimeout(t *testing.T) {
	up := newUpstream(t)
	key := testkit.SigningKey(t, "business-1")
	e := setup(t, func(c *config.Config) {
		var err error
		c.Routes, err = routing.Build(map[platform.Service]*url.URL{platform.BusinessOperationsService: up.url()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.TrustedIssuers = append(c.TrustedIssuers, auth.TrustedIssuer{Issuer: string(platform.BusinessOperationsService), Domain: platform.DomainBusinessOperations, Keys: jwt.NewStaticKeys().Add(string(platform.BusinessOperationsService), key.KID, key.PublicKey())})
		c.UpstreamTimeout = 50 * time.Millisecond
	})
	issuer, _ := auth.NewIssuer(auth.IssuerConfig{Issuer: string(platform.BusinessOperationsService), Key: key})
	token, actor := e.userToken(issuer)
	registry := serviceauth.NewRegistry()
	_ = registry.Add(platform.APIGateway, e.gwKey.KID, e.gwKey.PublicKey())
	verifier, _ := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.BusinessOperationsService, Registry: registry})
	for _, prefix := range []string{"/api/v1/business/", "/api/v1/business-operations/"} {
		resp, _ := e.do("GET", prefix+"organisation", token, map[string]string{"X-Request-ID": "business-request", "X-Service-Authorization": "Bearer forged"}, "")
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", prefix, resp.StatusCode)
		}
		req, _, _ := up.snapshot()
		p, err := verifier.Verify(context.Background(), strings.TrimPrefix(req.Header.Get(serviceauth.HeaderServiceAuthorization), "Bearer "))
		if err != nil || p.Service != platform.APIGateway || p.Delegation.ActorID != actor || p.Delegation.ActorDomain != platform.DomainBusinessOperations || p.Delegation.OriginalRequestID != "business-request" || req.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("invalid forwarding: %+v %v", p, err)
		}
	}
	wrong, _ := e.userToken(e.io)
	if resp, _ := e.do("GET", "/api/v1/business/organisation", wrong, nil, ""); resp.StatusCode != 403 {
		t.Fatalf("cross-domain token: %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/internal/v1/business/organisation", token, nil, ""); resp.StatusCode != 404 {
		t.Fatalf("internal exposed: %d", resp.StatusCode)
	}
	up.mu.Lock()
	up.delay = 200 * time.Millisecond
	up.mu.Unlock()
	if resp, _ := e.do("GET", "/api/v1/business/organisation", token, nil, ""); resp.StatusCode != 503 {
		t.Fatalf("timeout: %d", resp.StatusCode)
	}
}
