module github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/svc-a

go 1.26.0

require github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/svc-b v0.0.0

replace github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/svc-b => ../svc-b
