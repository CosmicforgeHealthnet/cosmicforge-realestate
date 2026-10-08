# ADR-0002: Initial Deployment Model

**Status:** Accepted

Docker packages backend services for staging and production deployment. Docker Compose is the initial server-side orchestrator behind Nginx.

Docker is not required for local development because local disk capacity is intentionally conserved.

Terraform provisions infrastructure. Kubernetes is intentionally deferred until operational scale justifies its additional complexity.
