// Command worker runs the admin-service worker process.
package main

import (
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/admin-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/domainkit"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() { domainkit.Main(platform.AdminService, domainkit.ModeWorker, usecases.New) }
