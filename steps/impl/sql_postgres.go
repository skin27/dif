package impl

import (
	"crypto/tls"
	"database/sql"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// The postgres driver is github.com/jackc/pgx/v5 (its database/sql adapter).
func init() {
	sqlDialects["postgres"] = &sqlDialect{
		name:        "postgres",
		defaultPort: 5432,
		placeholder: func(i int) string { return "$" + strconv.Itoa(i) },
		open: func(c sqlSettings, tlsConf *tls.Config, connectTimeout time.Duration) (*sql.DB, error) {
			u := url.URL{
				Scheme:   "postgres",
				User:     url.UserPassword(c.user, c.password),
				Host:     net.JoinHostPort(c.host, strconv.Itoa(c.port)),
				Path:     "/" + c.database,
				RawQuery: "sslmode=disable",
			}
			cfg, err := pgx.ParseConfig(u.String())
			if err != nil {
				return nil, err
			}
			cfg.ConnectTimeout = connectTimeout
			cfg.TLSConfig, cfg.Fallbacks = tlsConf, nil
			return stdlib.OpenDB(*cfg), nil
		},
	}
}
