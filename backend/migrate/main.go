// Command migrate applies and rolls back SQL migrations in db/migrations
// using golang-migrate.
//
//	DATABASE_URL="mysql://user:pass@tcp(127.0.0.1:3306)/rental"
//
//	go run ./migrate up              apply all pending migrations
//	go run ./migrate up 2            apply the next 2
//	go run ./migrate down            roll back ONE migration (safe default)
//	go run ./migrate down 3          roll back 3
//	go run ./migrate down -all -yes  roll back everything (destroys data)
//	go run ./migrate goto 5          move to exactly version 5 (up or down)
//	go run ./migrate version         show current version / dirty state
//	go run ./migrate force 4         mark version 4 clean after a failed run
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	path := fs.String("path", envOr("MIGRATIONS_PATH", "db/migrations"), "migrations directory")
	all := fs.Bool("all", false, "down: roll back every migration")
	yes := fs.Bool("yes", false, "confirm destructive action")
	_ = fs.Parse(os.Args[2:])
	args := fs.Args()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set, e.g. mysql://user:pass@tcp(127.0.0.1:3306)/rental")
	}
	dsn, err := withMultiStatements(dsn)
	if err != nil {
		log.Fatalf("invalid DATABASE_URL: %v", err)
	}

	m, err := migrate.New("file://"+*path, dsn)
	if err != nil {
		log.Fatalf("init migrate: %v", err)
	}
	defer m.Close()

	switch cmd {
	case "up":
		if n, ok := intArg(args); ok {
			report(m.Steps(n))
		} else {
			report(m.Up())
		}
	case "down":
		if *all {
			requireConfirm(*yes, "rolling back ALL migrations drops every table")
			report(m.Down())
			break
		}
		n, ok := intArg(args)
		if !ok {
			n = 1
		}
		if os.Getenv("APP_ENV") == "production" {
			requireConfirm(*yes, "APP_ENV=production")
		}
		report(m.Steps(-n))
	case "goto":
		v, ok := intArg(args)
		if !ok || v < 0 {
			log.Fatal("usage: migrate goto <version>")
		}
		if os.Getenv("APP_ENV") == "production" {
			requireConfirm(*yes, "APP_ENV=production")
		}
		report(m.Migrate(uint(v)))
	case "force":
		v, ok := intArg(args)
		if !ok {
			log.Fatal("usage: migrate force <version>")
		}
		if os.Getenv("APP_ENV") == "production" {
			requireConfirm(*yes, "APP_ENV=production")
		}
		report(m.Force(v))
	case "version":
		v, dirty, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			fmt.Println("version: none (no migrations applied)")
			return
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("version: %d dirty: %v\n", v, dirty)
	default:
		usage()
	}
}

func report(err error) {
	switch {
	case err == nil:
		fmt.Println("done")
	case errors.Is(err, migrate.ErrNoChange):
		fmt.Println("no change")
	default:
		var dirty migrate.ErrDirty
		if errors.As(err, &dirty) {
			log.Fatalf("database is dirty at version %d: a previous migration failed midway. "+
				"MySQL DDL is not transactional, so inspect the schema, fix it, then run "+
				"`migrate force <last good version>` and retry", dirty.Version)
		}
		log.Fatal(err)
	}
}

func requireConfirm(yes bool, why string) {
	if !yes {
		log.Fatalf("refusing to continue (%s): re-run with -yes", why)
	}
}

func intArg(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 0 && args[0] != "-1" {
		log.Fatalf("invalid number %q", args[0])
	}
	return n, true
}

// Migration files contain several statements, so the driver needs multiStatements=true.
func withMultiStatements(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if !strings.EqualFold(q.Get("multiStatements"), "true") {
		q.Set("multiStatements", "true")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: migrate <up [n] | down [n] [-all -yes] | goto v | force v | version> [-path dir]")
	os.Exit(2)
}
