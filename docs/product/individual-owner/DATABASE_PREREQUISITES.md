# Individual Owner — database prerequisites

## btree_gist (required)

Migration `00009_shortlet.sql` runs `CREATE EXTENSION IF NOT EXISTS btree_gist`.
The extension backs the exclusion constraint `shortlet_bookings_no_overlap`
(`EXCLUDE USING gist (listing_id WITH =, daterange(check_in, check_out) WITH &&)`),
which stops two short-let bookings for the same listing from overlapping, even
under concurrent requests. No other migration (00001–00016) creates an
extension. `TestRequiredExtensionsMatchMigrations` fails if a migration adds an
extension that is not declared in `migrations.RequiredExtensions`.

### What the deployment must provide

1. The PostgreSQL server must ship the `btree_gist` contrib module.
   - Debian/Ubuntu: install `postgresql-16-contrib`. The official
     `postgres:16` image already includes it.
   - Managed databases: allow `btree_gist` in the extension list. It is
     available on AWS RDS/Aurora, Cloud SQL and Azure Flexible Server.
2. One of the following must hold:
   - **Recommended:** before the first deploy, an administrator runs this once
     in `individual_owner_db`:
     ```sql
     CREATE EXTENSION IF NOT EXISTS btree_gist;
     ```
     The migration role then needs no extra privilege.
   - Or the migration role is a superuser.
   - Or the migration role holds `CREATE` on the database and on the target
     schema. `btree_gist` is a trusted extension on PostgreSQL 13+, so
     database owners qualify.

### Preflight (non-destructive)

`individual-owner-service-migrate up` checks the prerequisite before applying
any migration and exits with status 1 without changing the database if it is
missing. `individual-owner-service-migrate preflight` runs only the check; use
it as a deployment gate. The check reads `pg_extension`,
`pg_available_extensions`, `pg_available_extension_versions.trusted`,
`pg_roles.rolsuper` and `has_database_privilege` / `has_schema_privilege`.

Example output against real PostgreSQL 16, with a role that may create tables
but not the extension:

```
$ DATABASE_URL=postgres://cf_demo_mig:…@127.0.0.1:5432/cf_preflight_demo individual-owner-service-migrate up
migrate: database prerequisite missing: role "cf_demo_mig" cannot create extension btree_gist in database "cf_preflight_demo" (needs superuser, or CREATE on the database and schema for a trusted extension; trusted=true database_create=false schema_create=true); an administrator must run `CREATE EXTENSION IF NOT EXISTS btree_gist;` in database "cf_preflight_demo" before migrating
exit=1        # public tables afterwards: 0
# administrator: CREATE EXTENSION IF NOT EXISTS btree_gist;
$ individual-owner-service-migrate preflight
preflight ok
```

Without the preflight, the same role would apply migrations 00001–00008 and
then fail at 00009, leaving a partly migrated schema.

### Evidence (real PostgreSQL 16)

- `shared/go/migrate/preflight_test.go`: covers an installed extension, an
  unavailable extension, and a restricted role that is refused by both
  `preflight` and `up` with zero tables created; after the administrator step,
  the same role passes.
- `services/individual-owner-service/tests/integration/migrate_preflight_test.go`:
  the restricted role is refused before any owner migration. After the
  administrator step, the same non-superuser role applies all 16 owner
  migrations.
- Local and E2E runs use a superuser and pass the preflight automatically.
  Staging has not been verified: the staging migration role and its privileges
  still need to be confirmed there.
