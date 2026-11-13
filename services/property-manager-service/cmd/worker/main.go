// Command worker runs the property-manager-service worker process.
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/property-manager-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.PropertyManagerService, domainkit.ModeWorker, usecases.New) }
