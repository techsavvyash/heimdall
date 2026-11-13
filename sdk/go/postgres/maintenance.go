package postgres

import (
	"database/sql"

	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

// RecordMaintenance is the explicit bounded cleanup port for expired sessions and login records.
type RecordMaintenance = internal.RecordMaintenance

func NewRecordMaintenance(db *sql.DB, productID, environment string) (*RecordMaintenance, error) {
	return internal.NewRecordMaintenance(db, productID, environment, nil)
}
