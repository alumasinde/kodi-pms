package config

import (
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestMySQLDSNFromURL(t *testing.T) {
	cfg := &Config{DatabaseURL: "mysql://rental:p%40ss@tcp(127.0.0.1:3306)/kodi_demo?multiStatements=true"}
	dsn, err := cfg.MySQLDSN()
	if err != nil {
		t.Fatal(err)
	}
	mc, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if mc.User != "rental" || mc.Passwd != "p@ss" {
		t.Fatalf("credentials = %q/%q", mc.User, mc.Passwd)
	}
	if mc.Net != "tcp" || mc.Addr != "127.0.0.1:3306" || mc.DBName != "kodi_demo" {
		t.Fatalf("target = %s/%s/%s", mc.Net, mc.Addr, mc.DBName)
	}
	if !mc.ParseTime || mc.Loc != time.UTC {
		t.Fatalf("time settings = parseTime:%v loc:%v", mc.ParseTime, mc.Loc)
	}
	if mc.Params["time_zone"] != "'+00:00'" {
		t.Fatalf("time_zone = %q", mc.Params["time_zone"])
	}
}

func TestMySQLDSNRequiresDatabase(t *testing.T) {
	cfg := &Config{DatabaseURL: "root@tcp(127.0.0.1:3306)/"}
	if _, err := cfg.MySQLDSN(); err == nil {
		t.Fatal("expected an error for a missing database name")
	}
}
