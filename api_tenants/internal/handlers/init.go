package handlers

import (
	"database/sql"

	"frameworks/pkg/logging"
)

var (
	db     *sql.DB
	logger logging.Logger
)

// Init initializes the handlers with database and logger (for health poller)
func Init(database *sql.DB, log logging.Logger) {
	db = database
	logger = log
}
