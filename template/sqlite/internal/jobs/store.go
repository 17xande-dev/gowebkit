package jobs

import (
	"database/sql"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/outbox"
	"github.com/17xande-dev/outbox/sqlitestore"
)

type queueTx = *sql.Tx

func storage(d *db.DB) outbox.Store[queueTx] { return sqlitestore.New(d.DB) }
