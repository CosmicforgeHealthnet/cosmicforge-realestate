// Command api serves marketplace-service HTTP APIs: health/readiness/version,
// the customer API (/api/v1/marketplace) and the internal Business supply
// contract (business-supply.v1).
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/customer"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.MarketplaceService, domainkit.ModeAPI, customer.New) }
