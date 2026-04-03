package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "POSTGRES_DSN is required")
		os.Exit(1)
	}

	migrator, err := migrate.New("file://migrations/files", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate.New: %v\n", err)
		os.Exit(1)
	}
	defer migrator.Close()

	if err := migrator.Up(); err != nil {
		if strings.Contains(err.Error(), "no change") {
			fmt.Println("[migrations] no changes")
			return
		}
		fmt.Fprintf(os.Stderr, "migrate up: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("[migrations] applied successfully")
}
