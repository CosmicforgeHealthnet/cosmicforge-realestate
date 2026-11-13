// Command worker runs the artisan-service worker process.
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/artisan-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.ArtisanService, domainkit.ModeWorker, usecases.New) }
