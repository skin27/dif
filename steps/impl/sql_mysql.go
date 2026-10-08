package impl

import (
	"crypto/tls"
	"database/sql"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

// The mysql8 driver (it talks to MySQL 5.7 and later and to MariaDB) is
// github.com/go-sql-driver/mysql.
func init() {
	sqlDialects["mysql8"] = &sqlDialect{
		name:        "mysql8",
		defaultPort: 3306,
		placeholder: func(int) string { return "?" },
		open: func(c sqlSettings, tlsConf *tls.Config, connectTimeout time.Duration) (*sql.DB, error) {
			cfg := mysql.NewConfig()
			cfg.User, cfg.Passwd = c.user, c.password
			cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(c.host, strconv.Itoa(c.port))
			cfg.DBName = c.database
			cfg.Timeout = connectTimeout
			cfg.TLS = tlsConf
			connector, err := mysql.NewConnector(cfg)
			if err != nil {
				return nil, err
			}
			return sql.OpenDB(connector), nil
		},
	}
}
