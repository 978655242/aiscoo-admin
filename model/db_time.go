package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// GetDBTimestamp returns a UNIX timestamp from database time.
// Falls back to application time on error.
func GetDBTimestamp() int64 {
	ts, err := getDBTimestamp(DB)
	if err != nil {
		return common.GetTimestamp()
	}
	return ts
}

// Use the caller's connection when reading time inside a transaction.
func getDBTimestamp(db *gorm.DB) (int64, error) {
	var ts int64
	var err error
	switch {
	case common.UsingMainDatabase(common.DatabaseTypePostgreSQL):
		err = db.Raw("SELECT EXTRACT(EPOCH FROM clock_timestamp())::bigint").Scan(&ts).Error
	case common.UsingMainDatabase(common.DatabaseTypeSQLite):
		err = db.Raw("SELECT strftime('%s','now')").Scan(&ts).Error
	default:
		err = db.Raw("SELECT UNIX_TIMESTAMP()").Scan(&ts).Error
	}
	if err != nil {
		return 0, err
	}
	if ts <= 0 {
		return 0, errors.New("database clock is unavailable")
	}
	return ts, nil
}
