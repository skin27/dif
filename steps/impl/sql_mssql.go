package impl

import (
	"crypto/tls"
	"database/sql"
	"net"
	"net/url"
	"strconv"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
)

// The sql_server driver is github.com/microsoft/go-mssqldb.
func init() {
	sqlDialects["sql_server"] = &sqlDialect{
		name:        "sql_server",
		defaultPort: 1433,
		placeholder: func(i int) string { return "@p" + strconv.Itoa(i) },
		open: func(c sqlSettings, tlsConf *tls.Config, connectTimeout time.Duration) (*sql.DB, error) {
			encrypt := "disable"
			if tlsConf != nil {
				encrypt = "true"
			}
			q := url.Values{
				"database":           {c.database},
				"encrypt":            {encrypt},
				"dial timeout":       {strconv.Itoa(max(1, int(connectTimeout/time.Second)))},
				"connection timeout": {strconv.Itoa(max(1, int(connectTimeout/time.Second)))},
			}
			u := url.URL{
				Scheme:   "sqlserver",
				User:     url.UserPassword(c.user, c.password),
				Host:     net.JoinHostPort(c.host, strconv.Itoa(c.port)),
				RawQuery: q.Encode(),
			}
			cfg, err := msdsn.Parse(u.String())
			if err != nil {
				return nil, err
			}
			if tlsConf != nil {
				cfg.TLSConfig = tlsConf
			}
			return sql.OpenDB(mssql.NewConnectorConfig(cfg)), nil
		},
	}
}
