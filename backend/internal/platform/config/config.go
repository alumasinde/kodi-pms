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
	Env              string
	HTTPAddr         string
	DatabaseURL      string
	RedisAddr        string
	RedisPassword    string
	RedisDB          int
	JWTSecret        string
	TokenIssuer      string
	AccessTTL        time.Duration
	RefreshTTL       time.Duration
	PIIEncryptionKey string
	CookieSecure     bool
	CookieDomain     string
	PublicBaseURL    string
	CORSOrigins      []string
	SMTPURL          string
	MailFrom         string
	TrustProxy       bool
	RateLimitIP      int
	RateLimitUser    int
	ShutdownTimeout  time.Duration
}

// devPIIKey is base64 for 32 ASCII bytes. It is only used when APP_ENV is
// development/test; production refuses to start without PII_ENCRYPTION_KEY.
const devPIIKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func Load() (*Config, error) {
	_ = godotenv.Load()

	envName := env("APP_ENV", "development")
	local := envName == "development" || envName == "test"
	publicBase, corsDefault := "", ""
	if local {
		publicBase = "http://localhost:5173"
		corsDefault = "http://localhost:5173,http://localhost:3000"
	}

	cfg := &Config{
		Env:              envName,
		HTTPAddr:         env("HTTP_ADDR", ":8080"),
		DatabaseURL:      strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RedisAddr:        strings.TrimSpace(os.Getenv("REDIS_ADDR")),
		RedisPassword:    os.Getenv("REDIS_PASSWORD"),
		RedisDB:          envInt("REDIS_DB", 0),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		TokenIssuer:      env("TOKEN_ISSUER", "kodipms"),
		AccessTTL:        envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTTL:       envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		PIIEncryptionKey: strings.TrimSpace(os.Getenv("PII_ENCRYPTION_KEY")),
		CookieSecure:     envBool("COOKIE_SECURE", !local),
		CookieDomain:     os.Getenv("COOKIE_DOMAIN"),
		PublicBaseURL:    strings.TrimRight(env("PUBLIC_BASE_URL", publicBase), "/"),
		CORSOrigins:      envList("CORS_ORIGINS", corsDefault),
		SMTPURL:          strings.TrimSpace(os.Getenv("SMTP_URL")),
		MailFrom:         strings.TrimSpace(os.Getenv("MAIL_FROM")),
		TrustProxy:       envBool("TRUST_PROXY", false),
		RateLimitIP:      envInt("RATE_LIMIT_IP_PER_MIN", 300),
		RateLimitUser:    envInt("RATE_LIMIT_USER_PER_MIN", 600),
		ShutdownTimeout:  envDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		if !cfg.IsDevelopment() {
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
	if cfg.PIIEncryptionKey == "" {
		if !cfg.IsDevelopment() {
			return nil, fmt.Errorf("PII_ENCRYPTION_KEY is required outside development")
		}
		cfg.PIIEncryptionKey = devPIIKey
	}
	if !cfg.IsDevelopment() {
		if cfg.RedisAddr == "" {
			return nil, fmt.Errorf("REDIS_ADDR is required outside development")
		}
		if !cfg.CookieSecure {
			return nil, fmt.Errorf("COOKIE_SECURE must be true outside development")
		}
		if err := validateHTTPSURL("PUBLIC_BASE_URL", cfg.PublicBaseURL); err != nil {
			return nil, err
		}
		if len(cfg.CORSOrigins) == 0 {
			return nil, fmt.Errorf("CORS_ORIGINS is required outside development")
		}
		for _, origin := range cfg.CORSOrigins {
			if err := validateHTTPSURL("CORS_ORIGINS entry", origin); err != nil {
				return nil, err
			}
		}
		if cfg.SMTPURL == "" || cfg.MailFrom == "" {
			return nil, fmt.Errorf("SMTP_URL and MAIL_FROM are required outside development")
		}
	}
	return cfg, nil
}

func (c *Config) IsDevelopment() bool { return c.Env == "development" || c.Env == "test" }

func validateHTTPSURL(name, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s is required outside development", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("%s must be an absolute https URL", name)
	}
	if isLocalHost(u.Hostname()) {
		return fmt.Errorf("%s must not point at localhost outside development", name)
	}
	return nil
}

func isLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		host == "0.0.0.0" || strings.HasSuffix(host, ".localhost")
}

// MySQLDSN accepts either a golang-migrate style URL
// (mysql://user:pass@tcp(127.0.0.1:3306)/db) or a native go-sql-driver DSN,
// and normalises it to UTC, utf8mb4 and parseTime=true.
func (c *Config) MySQLDSN() (string, error) {
	raw := strings.TrimSpace(c.DatabaseURL)
	var mc *mysql.Config
	if strings.HasPrefix(raw, "mysql://") {
		// golang-migrate style URL. mysql.ParseDSN already understands the
		// user:pass@tcp(host:port)/db part; net/url rejects tcp(...) authorities
		// because it treats the closing paren as part of the port.
		parsed, err := mysql.ParseDSN(strings.TrimPrefix(raw, "mysql://"))
		if err != nil {
			return "", fmt.Errorf("parse DATABASE_URL: %w", err)
		}
		// The URL form percent-encodes credentials; the driver DSN form does not.
		if parsed.User, err = url.PathUnescape(parsed.User); err != nil {
			return "", fmt.Errorf("parse DATABASE_URL user: %w", err)
		}
		if parsed.Passwd, err = url.PathUnescape(parsed.Passwd); err != nil {
			return "", fmt.Errorf("parse DATABASE_URL password: %w", err)
		}
		mc = parsed
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
