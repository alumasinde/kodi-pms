package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

type Config struct {
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	JWTSecret       string
	TokenIssuer     string
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	CookieSecure    bool
	CookieDomain    string
	PublicBaseURL   string
	CORSOrigins     []string
	TrustProxy      bool
	RateLimitIP     int
	RateLimitUser   int
	ShutdownTimeout time.Duration
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Env:             env("APP_ENV", "development"),
		HTTPAddr:        env("HTTP_ADDR", ":8080"),
		DatabaseURL:     strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RedisAddr:       strings.TrimSpace(os.Getenv("REDIS_ADDR")),
		RedisPassword:   os.Getenv("REDIS_PASSWORD"),
		RedisDB:         envInt("REDIS_DB", 0),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		TokenIssuer:     env("TOKEN_ISSUER", "kodipms"),
		AccessTTL:       envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTTL:      envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		CookieSecure:    envBool("COOKIE_SECURE", false),
		CookieDomain:    os.Getenv("COOKIE_DOMAIN"),
		PublicBaseURL:   strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:5173"), "/"),
		CORSOrigins:     envList("CORS_ORIGINS", "http://localhost:5173,http://localhost:3000"),
		TrustProxy:      envBool("TRUST_PROXY", false),
		RateLimitIP:     envInt("RATE_LIMIT_IP_PER_MIN", 300),
		RateLimitUser:   envInt("RATE_LIMIT_USER_PER_MIN", 600),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		if cfg.Env != "development" {
			return nil, fmt.Errorf("JWT_SECRET is required outside development")
		}
		cfg.JWTSecret = "dev-only-jwt-secret-change-me-0123456789"
	}
	if len(cfg.JWTSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	if cfg.AccessTTL <= 0 || cfg.RefreshTTL <= 0 {
		return nil, fmt.Errorf("token TTLs must be positive")
	}
	return cfg, nil
}

func (c *Config) IsDevelopment() bool { return c.Env == "development" }

// MySQLDSN accepts either a golang-migrate style URL
// (mysql://user:pass@tcp(127.0.0.1:3306)/db) or a native go-sql-driver DSN,
// and normalises it to UTC, utf8mb4 and parseTime=true.
func (c *Config) MySQLDSN() (string, error) {
	raw := strings.TrimSpace(c.DatabaseURL)
	var mc *mysql.Config
	if strings.HasPrefix(raw, "mysql://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse DATABASE_URL: %w", err)
		}
		mc = mysql.NewConfig()
		if u.User != nil {
			mc.User = u.User.Username()
			mc.Passwd, _ = u.User.Password()
		}
		host := u.Host
		switch {
		case strings.HasPrefix(host, "tcp(") && strings.HasSuffix(host, ")"):
			mc.Net, mc.Addr = "tcp", strings.TrimSuffix(strings.TrimPrefix(host, "tcp("), ")")
		case strings.HasPrefix(host, "unix(") && strings.HasSuffix(host, ")"):
			mc.Net, mc.Addr = "unix", strings.TrimSuffix(strings.TrimPrefix(host, "unix("), ")")
		default:
			mc.Net, mc.Addr = "tcp", host
		}
		mc.DBName = strings.TrimPrefix(u.Path, "/")
		if u.RawQuery != "" {
			mc.Params = map[string]string{}
			for k, v := range u.Query() {
				if len(v) > 0 {
					mc.Params[k] = v[len(v)-1]
				}
			}
		}
	} else {
		parsed, err := mysql.ParseDSN(raw)
		if err != nil {
			return "", fmt.Errorf("parse DATABASE_URL: %w", err)
		}
		mc = parsed
	}
	if mc.DBName == "" {
		return "", fmt.Errorf("DATABASE_URL must include a database name")
	}
	mc.ParseTime = true
	mc.Loc = time.UTC
	if mc.Params == nil {
		mc.Params = map[string]string{}
	}
	if _, ok := mc.Params["charset"]; !ok {
		mc.Params["charset"] = "utf8mb4"
	}
	if _, ok := mc.Params["collation"]; !ok {
		mc.Params["collation"] = "utf8mb4_unicode_ci"
	}
	if _, ok := mc.Params["time_zone"]; !ok {
		mc.Params["time_zone"] = "'+00:00'"
	}
	return mc.FormatDSN(), nil
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(k string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func envList(k, def string) []string {
	raw := env(k, def)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
