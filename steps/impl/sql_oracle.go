package impl

import (
	"crypto/tls"
	"database/sql"
	"errors"
	"strconv"
	"time"

	goora "github.com/sijms/go-ora/v2"
)

// The oracle driver is github.com/sijms/go-ora/v2, which is pure Go. The database
// option is the service name, such as xe.
func init() {
	sqlDialects["oracle"] = &sqlDialect{
		name:        "oracle",
		defaultPort: 1521,
		placeholder: func(i int) string { return ":" + strconv.Itoa(i) },
		open: func(c sqlSettings, tlsConf *tls.Config, connectTimeout time.Duration) (*sql.DB, error) {
			opts := map[string]string{"TIMEOUT": strconv.Itoa(max(1, int(connectTimeout/time.Second)))}
			if tlsConf != nil {
				opts["SSL"] = "true"
			}
			connector, ok := goora.NewConnector(goora.BuildUrl(c.host, c.port, c.database, c.user, c.password, opts)).(*goora.OracleConnector)
			if !ok {
				return nil, errors.New("oracle: the driver returned an unexpected connector")
			}
			if tlsConf != nil {
				connector.WithTLSConfig(tlsConf)
			}
			return sql.OpenDB(connector), nil
		},
	}
}
