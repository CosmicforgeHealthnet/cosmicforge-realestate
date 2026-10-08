// Package platform names the approved deployable components and user domains
// (FINAL_ARCHITECTURE.md). It is the single place where the 12 component
// names and 7 user-domain names are spelled, so typos cannot silently create
// an unknown caller or issuer.
package platform

// Service is the stable name of a deployable backend component. It is used as
// the service-JWT issuer/audience and as the event producer name.
type Service string

const (
	MarketplaceService          Service = "marketplace-service"
	IndividualOwnerService      Service = "individual-owner-service"
	BusinessOperationsService   Service = "business-operations-service"
	PropertyManagerService      Service = "property-manager-service"
	ArtisanService              Service = "artisan-service"
	ProfessionalServicesService Service = "professional-services-service"
	AdminService                Service = "admin-service"

	VerificationService  Service = "verification-service"
	PaymentService       Service = "payment-service"
	NotificationService  Service = "notification-service"
	MediaDocumentService Service = "media-document-service"

	APIGateway Service = "api-gateway"
)

// Domain is a user domain. Each user domain is its own account authority.
type Domain string

const (
	DomainMarketplace          Domain = "marketplace"
	DomainIndividualOwner      Domain = "individual-owner"
	DomainBusinessOperations   Domain = "business-operations"
	DomainPropertyManager      Domain = "property-manager"
	DomainArtisan              Domain = "artisan"
	DomainProfessionalServices Domain = "professional-services"
	DomainAdmin                Domain = "admin"
)

var domainServices = map[Domain]Service{
	DomainMarketplace:          MarketplaceService,
	DomainIndividualOwner:      IndividualOwnerService,
	DomainBusinessOperations:   BusinessOperationsService,
	DomainPropertyManager:      PropertyManagerService,
	DomainArtisan:              ArtisanService,
	DomainProfessionalServices: ProfessionalServicesService,
	DomainAdmin:                AdminService,
}

var serviceDomains = func() map[Service]Domain {
	m := make(map[Service]Domain, len(domainServices))
	for d, s := range domainServices {
		m[s] = d
	}
	return m
}()

var allServices = []Service{
	MarketplaceService, IndividualOwnerService, BusinessOperationsService,
	PropertyManagerService, ArtisanService, ProfessionalServicesService, AdminService,
	VerificationService, PaymentService, NotificationService, MediaDocumentService,
	APIGateway,
}

// Services returns all 12 deployable components.
func Services() []Service { return append([]Service(nil), allServices...) }

// Domains returns the 7 user domains.
func Domains() []Domain {
	return []Domain{
		DomainMarketplace, DomainIndividualOwner, DomainBusinessOperations,
		DomainPropertyManager, DomainArtisan, DomainProfessionalServices, DomainAdmin,
	}
}

// KnownService reports whether s is an approved component name.
func KnownService(s string) bool {
	for _, k := range allServices {
		if string(k) == s {
			return true
		}
	}
	return false
}

// KnownDomain reports whether d is an approved user domain.
func KnownDomain(d string) bool { _, ok := domainServices[Domain(d)]; return ok }

// ServiceForDomain returns the account-authority service of a user domain.
func ServiceForDomain(d Domain) (Service, bool) { s, ok := domainServices[d]; return s, ok }

// DomainForService returns the user domain a service is authority for.
// Shared platform services and the gateway have no domain.
func DomainForService(s Service) (Domain, bool) { d, ok := serviceDomains[s]; return d, ok }
