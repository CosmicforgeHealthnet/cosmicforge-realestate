// Command worker runs marketplace-service background work: outbox relay,
// checkout reconciliation with Business, quote expiry and funnel delivery.
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/customer"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.MarketplaceService, domainkit.ModeWorker, customer.New) }
