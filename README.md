# CosmicForge Real Estate

CosmicForge Real Estate is a Nigeria-first, microservice-based real-estate platform designed to support verified property discovery, ownership and authority verification, rentals, sales, property operations, maintenance, professional services, hospitality, protected payments, and internal trust operations.

## Technology stack

- **Backend:** Go
- **Mobile:** Flutter / Dart
- **Web:** Next.js / React / TypeScript
- **Primary data store:** PostgreSQL
- **Cache / coordination:** Redis
- **Containers:** Docker
- **Infrastructure as code:** Terraform
- **CI/CD:** GitHub Actions
- **API contracts:** OpenAPI + versioned event schemas
- **Communications:** CF Meet APIs for messaging, voice and video
- **Payments:** Paystack behind the platform payment service, with platform-owned transaction/ledger/reconciliation logic

## Repository model

This repository is a monorepo. Microservices are separated by business-domain ownership rather than by individual features.

### Mobile apps

- Marketplace
- Individual Property Owner
- Business Property Operations
- Property Manager
- Service Provider
- Professional Services

### Web apps

- Public Marketplace
- Owner Portal
- Business Operations Portal
- Property Manager Portal
- Admin Backoffice

### Backend services

- API Gateway
- Identity
- Property
- Verification
- Marketplace
- Transaction
- Agreement
- Payment
- Property Operations
- Maintenance
- Professional Services
- Hospitality
- Notification
- Trust & Safety
- Admin
- Analytics

## Communications

Do not build separate messaging, calling, video, or RTC microservices in this repository. CF Meet is the communications authority for chat, voice calls, video calls, and live video sessions. Real-estate services store only real-estate context, authorization and CF Meet session references where needed.

## Data ownership

Each service owns its own persistence boundary. One service must never query another service's tables directly. Cross-service behavior must use versioned APIs or durable events.

## Environments

- development
- staging
- production

Production data and secrets must never be shared with development or staging.

## Branch flow

`feature/* -> development -> staging -> main`

The exact branch protection policy will be added when the GitHub repository is provisioned.
