package database

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
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
