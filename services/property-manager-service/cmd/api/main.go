// Command api runs the property-manager-service api process.
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/property-manager-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.PropertyManagerService, domainkit.ModeAPI, usecases.New) }
