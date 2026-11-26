package jobs

import (
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/17xande-dev/outbox"
	"github.com/17xande-dev/outbox/pgxstore"
	"github.com/jackc/pgx/v5"
)

type queueTx = pgx.Tx

func storage(d *db.DB) outbox.Store[queueTx] { return pgxstore.New(d.Pool()) }
