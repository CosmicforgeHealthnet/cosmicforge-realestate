// Command api runs the artisan-service api process.
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/artisan-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.ArtisanService, domainkit.ModeAPI, usecases.New) }
