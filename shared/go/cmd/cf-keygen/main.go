// Command cf-keygen generates Ed25519 key material for service identities and
// domain token issuers. Output goes to stdout; store the private part in the
// secrets manager immediately and never commit it.
//
//	go run ./cmd/cf-keygen -service verification-service -kid verification-2026-10
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
)

func main() {
	service := flag.String("service", "", "component name (e.g. api-gateway)")
	kid := flag.String("kid", "", "key ID (e.g. api-gateway-2026-10)")
	flag.Parse()
	if !platform.KnownService(*service) || *kid == "" {
		fmt.Fprintln(os.Stderr, "usage: cf-keygen -service <known-service> -kid <key-id>")
		os.Exit(2)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := map[string]any{
		"service":     *service,
		"kid":         *kid,
		"private_key": security.EncodeEd25519Seed(priv),
		"public_key":  security.EncodeEd25519PublicKey(pub),
		"registry_entry": map[string]any{
			"service": *service,
			"keys":    []map[string]string{{"kid": *kid, "public_key": security.EncodeEd25519PublicKey(pub)}},
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
