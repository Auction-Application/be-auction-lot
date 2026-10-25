package database

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func ConnectToDB() *pgx.Conn {
	conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to connect to database:%v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	pingErr := conn.Ping(ctx)

	if pingErr != nil {
		fmt.Println("Cannot ping to database")
		os.Exit(1)
	}

	return conn
}

type (
	ForeignKeyName             string
	foreignKeyViolationMessage map[ForeignKeyName][]StorageField
)

type StorageField struct {
	Subject     string
	Description string
}

var foreignKeyConstraint = foreignKeyViolationMessage{
	"image_blob_upload_attempts_lot_id_fkey": []StorageField{
		{
			Subject:     "Storage",
			Description: "lotId does not match",
		},
	},
}

func (fkConstraint foreignKeyViolationMessage) getErrorMessage(fkName ForeignKeyName) ([]StorageField, bool) {
	errMessage, ok := fkConstraint[fkName]
	if !ok {
		return nil, false
	}
	return errMessage, true
}

func isStorageError(err error) (*pgconn.PgError, bool) {
	pgError, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return nil, false
	}

	return pgError, true
}

func DeriveIfStorageErrorMessage(err error) ([]StorageField, bool) {
	pgError, ok := isStorageError(err)
	if !ok {
		return nil, false
	}

	return foreignKeyConstraint.getErrorMessage(ForeignKeyName(pgError.ConstraintName))
}
