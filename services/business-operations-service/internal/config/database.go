package config

import (
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
)

const DatabaseName = "business_operations_db"

// ValidateOwnedDatabase validates the driver's effective database, including
// libpq URI query aliases that can override a superficially correct URL path.
// Parse errors are deliberately replaced to avoid exposing DSN credentials.
func ValidateOwnedDatabase(raw string) error {
	uri, err := url.Parse(raw)
	if err != nil || uri.Scheme != "postgres" && uri.Scheme != "postgresql" || uri.Path != "/"+DatabaseName {
		return errors.New("DATABASE_URL must connect to the service-owned business_operations_db")
	}
	parsed, err := pgxpool.ParseConfig(raw)
	if err != nil || parsed.ConnConfig.Database != DatabaseName {
		return errors.New("DATABASE_URL must connect to the service-owned business_operations_db")
	}
	return nil
}
