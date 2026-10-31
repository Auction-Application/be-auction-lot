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

// todo remove image_blobs table
// todo lot_images is a separate verified true table.
// todo tabel lots need a revamp
// todo make error msg take parameter
var databaseConstraint = foreignKeyViolationMessage{
	"image_blob_upload_attempts_lot_id_fkey": []StorageField{
		{
			Subject:     "Storage",
			Description: "lotId does not match",
		},
	},
	// todo make uniq index verfied column, not upload_state column
	// todo implement ALREADY_EXISTS grpc code
	"idx_unq_img_attmpts_sha256_lot_id_upload_state": []StorageField{
		{
			Subject:     "Storage",
			Description: "file with same sha256 hash and lotId whose upload has not been completed already exists",
		},
	},
	// todo remove this table, "image_blobs"
	"lot_images_image_blob_id_fkey": []StorageField{
		{
			Subject:     "Storage",
			Description: "image blob with given id does not exist",
		},
	},
	// todo remove this table, "image_blobs"
	"lot_images_lot_id_fkey": []StorageField{
		{
			Subject:     "Storage",
			Description: "lot with given id does not exist",
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

	return databaseConstraint.getErrorMessage(ForeignKeyName(pgError.ConstraintName))
}

func IsRowNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
