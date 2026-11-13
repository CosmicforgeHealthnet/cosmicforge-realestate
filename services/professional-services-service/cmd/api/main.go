// Command api runs the professional-services-service api process.
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/professional-services-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.ProfessionalServicesService, domainkit.ModeAPI, usecases.New) }
