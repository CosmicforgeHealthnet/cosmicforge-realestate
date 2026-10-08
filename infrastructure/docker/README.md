# Docker deployment

Docker is used for deployment, not required for local development.

- `staging/` — staging Docker Compose deployment.
- `production/` — production Docker Compose deployment.

CI builds immutable images and publishes them to GHCR. Production promotes an image digest that has already passed staging certification.
