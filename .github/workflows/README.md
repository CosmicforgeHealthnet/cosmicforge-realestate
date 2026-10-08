# CI/CD workflows

The architecture reserves workflows for backend, Flutter, web, contract, security, image build, staging deployment, production deployment, migrations and rollback.

Only the structural gate is active in the scaffold. Deployment workflows must be introduced with the first deployable service and must promote immutable Docker image digests from staging to production.
