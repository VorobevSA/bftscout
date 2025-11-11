package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	// DatabaseSchemePostgres is the postgres database scheme identifier
	DatabaseSchemePostgres = "postgres"
)

type Config struct {
	RPCURL    string
	WSPath    string
	DBDialect string // postgres only
	DBDsn     string // DSN string passed to GORM driver
	AppAPIURL string // optional: Cosmos REST API base URL (e.g., http://node:1317)
	Debug     bool   // if true: show logs, no TUI; if false: no logs, show TUI
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

// parseDatabaseURL interprets DATABASE_URL and returns (dialect, dsn).
// Supported schemes: postgres, postgresql.
func parseDatabaseURL(databaseURL string) (string, string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", "", err
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case DatabaseSchemePostgres, "postgresql":
		// GORM postgres driver accepts URL DSN as-is
		return DatabaseSchemePostgres, databaseURL, nil
	default:
		return "", "", fmt.Errorf("unsupported DATABASE_URL scheme: %s", u.Scheme)
	}
}

func Load() Config {
	cfg := Config{
		RPCURL:    getenv("RPC_URL", "http://localhost:26657"),
		WSPath:    getenv("WS_PATH", "/websocket"),
		AppAPIURL: os.Getenv("APP_API_URL"),
		Debug:     getenvBool("DEBUG", false),
	}

	if dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL")); dbURL != "" {
		if dialect, dsn, err := parseDatabaseURL(dbURL); err == nil {
			cfg.DBDialect = dialect
			cfg.DBDsn = dsn
		} else {
			fmt.Fprintf(os.Stderr, "warning: invalid DATABASE_URL, disabling persistence: %v\n", err)
		}
	}

	return cfg
}

func (c Config) WSURL() string {
	// cometbft http client expects a separate ws endpoint path
	return c.WSPath
}

func (c Config) String() string {
	return fmt.Sprintf("rpc=%s ws_path=%s db=%s", c.RPCURL, c.WSPath, c.DBDialect)
}

// DebugString returns a human-friendly configuration string with masked secrets.
func (c Config) DebugString() string {
	return fmt.Sprintf(
		"rpc=%s ws_path=%s db=%s dsn=%s app_api_url=%s",
		c.RPCURL,
		c.WSPath,
		c.DBDialect,
		maskDSN(c.DBDialect, c.DBDsn),
		c.AppAPIURL,
	)
}

func maskDSN(dialect, dsn string) string {
	switch strings.ToLower(dialect) {
	case DatabaseSchemePostgres:
		if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
			if u.User != nil {
				username := u.User.Username()
				u.User = url.User(username)
			}
			return u.String()
		}
		// Fallback for DSN as key-value list
		parts := strings.Fields(dsn)
		for i, p := range parts {
			lower := strings.ToLower(p)
			if strings.HasPrefix(lower, "password=") {
				parts[i] = "password=***"
			}
		}
		return strings.Join(parts, " ")
	default:
		return dsn
	}
}
