package impl

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// sqlStep runs an SQL statement on a database, as the platform's sql and sql2
// steps do. The platform's components are not open: this follows their flows
// and expected answers.
//
// sql: the option query is a simple template, evaluated for each message, usually
// ${body}; host, port, database, username and password are templates too, so a
// flow can take them from the headers. The connectionType names the database:
// postgres, mysql8 (mysql, mysql5, mariadb), sql_server and oracle, each with
// its driver in a file of its own (sql_postgres.go and the like).
//
// sql2: the query is Camel's, with :#name for a value from the header name (or
// :#${simple expression}); the connection is the data source of the DIL
// (dil.core.connections), which the DIL parser gives the step as the option
// connection.
//
// A statement that returns rows (SELECT, WITH, SHOW, ..., or RETURNING) makes
// the body a ResultSet (see sql_result.go); another statement leaves the body.
// Either way the headers numberOfRecords (the rows, or the rows the statement
// changed) and hasErrors (false: a statement that fails fails the message) are set.
type sqlStep struct {
	dialect *sqlDialect
	query   expression // sql: the statement, per message
	segs    []sqlSegment
	named   bool // sql2: segs is the statement

	host, port, database, user, password expression
	passwordSet                          bool

	ssl            bool
	tlsMin, tlsMax uint16
	roots          *x509.CertPool
	timeout        time.Duration
	connectTimeout time.Duration
	pool           sqlPool
}

// sqlDialect is what the step needs to know of a database.
type sqlDialect struct {
	name        string
	defaultPort int
	placeholder func(i int) string // the marker of the i-th (from 1) parameter
	open        func(c sqlSettings, tlsConf *tls.Config, connectTimeout time.Duration) (*sql.DB, error)
}

// sqlSettings is a connection, once its templates are evaluated.
type sqlSettings struct {
	host, database, user, password string
	port                           int
}

// sqlDialects are the databases that can be connected to, by name; the files
// of the drivers add theirs.
var sqlDialects = map[string]*sqlDialect{}

var sqlDialectNames = map[string]string{
	"postgres": "postgres", "postgresql": "postgres",
	"mysql8": "mysql8", "mysql": "mysql8", "mysql5": "mysql8", "mariadb": "mysql8",
	"sql_server": "sql_server", "sqlserver": "sql_server", "mssql": "sql_server",
	"oracle": "oracle",
}

func sqlDialectFor(name string) (*sqlDialect, error) {
	if canonical, ok := sqlDialectNames[strings.ToLower(strings.TrimSpace(name))]; ok {
		if d := sqlDialects[canonical]; d != nil {
			return d, nil
		}
	}
	names := make([]string, 0, len(sqlDialectNames))
	for n := range sqlDialectNames {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("%q is not one of %s", name, strings.Join(names, ", "))
}

func newSQLAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := &sqlStep{}
	var err error
	if a.dialect, err = sqlDialectFor(p["connectionType"].(string)); err != nil {
		return nil, fmt.Errorf("option connectionType: %w", err)
	}
	flow := flowOf(p)
	for _, f := range []struct {
		name string
		to   *expression
	}{{"query", &a.query}, {"host", &a.host}, {"port", &a.port}, {"database", &a.database}, {"username", &a.user}} {
		if *f.to, err = compileExpressionIn(flow, "simple", p[f.name].(string)); err != nil {
			return nil, fmt.Errorf("option %s: %w", f.name, err)
		}
	}
	if strings.TrimSpace(p["query"].(string)) == "" {
		return nil, fmt.Errorf("option query: required")
	}
	if strings.TrimSpace(p["host"].(string)) == "" {
		return nil, fmt.Errorf("option host: required")
	}
	pw, set := p["password"].(string)
	if !set {
		pw, _, err = environmentSecret("DIF_SQL_PASSWORD")
		if err != nil {
			return nil, err
		}
	}
	if a.password, err = compileExpressionIn(flow, "simple", pw); err != nil {
		return nil, fmt.Errorf("option password: %w", err)
	}
	if err := a.common(p); err != nil {
		return nil, err
	}
	return a, nil
}

// common reads the options both steps have: TLS and the timeouts.
func (a *sqlStep) common(p stepdef.Params) (err error) {
	a.ssl = p["useSSL"].(bool)
	if a.tlsMin, a.tlsMax, err = sqlTLSVersions(p["tlsVersion"].(string)); err != nil {
		return fmt.Errorf("option tlsVersion: %w", err)
	}
	if a.ssl {
		if a.roots, err = outboundRoots(p); err != nil {
			return err
		}
	}
	a.timeout = time.Duration(p["socketTimeout"].(int)) * time.Millisecond
	a.connectTimeout = time.Duration(p["connectTimeout"].(int)) * time.Millisecond
	return nil
}

// sqlTLSVersions turns a name such as TLSv1.2 into the version the connection is
// limited to, as Java's enabled protocols limit it; no name means TLS 1.2 or
// later.
func sqlTLSVersions(name string) (min, max uint16, err error) {
	v := strings.ToLower(strings.TrimSpace(name))
	if v == "" {
		return tls.VersionTLS12, 0, nil
	}
	v = strings.TrimPrefix(strings.TrimPrefix(v, "tls"), "v")
	switch v {
	case "1", "1.0":
		return tls.VersionTLS10, tls.VersionTLS10, nil
	case "1.1":
		return tls.VersionTLS11, tls.VersionTLS11, nil
	case "1.2":
		return tls.VersionTLS12, tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, tls.VersionTLS13, nil
	}
	return 0, 0, fmt.Errorf("%q is not TLSv1, TLSv1.1, TLSv1.2 or TLSv1.3", name)
}

// sql2 keys of a connection of the DIL.
type sqlConnectionKeys struct {
	DBType   string `json:"dbtype"`
	DBName   string `json:"dbname"`
	Host     string `json:"host"`
	Port     any    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func newSQL2Action(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := &sqlStep{named: true}
	raw := p["connection"].(string)
	if raw == "" {
		return nil, fmt.Errorf("the connection is missing: set the option dataSource to the id of a connection in dil.core.connections")
	}
	var k sqlConnectionKeys
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		return nil, fmt.Errorf("option connection: want a JSON object with dbtype, dbname, host, port, username and password: %w", err)
	}
	var err error
	if a.dialect, err = sqlDialectFor(k.DBType); err != nil {
		return nil, fmt.Errorf("connection: dbtype %w", err)
	}
	if k.Host == "" {
		return nil, fmt.Errorf("connection: host is missing")
	}
	port := ""
	switch x := k.Port.(type) {
	case string:
		port = x
	case float64:
		port = strconv.Itoa(int(x))
	}
	for _, f := range []struct {
		to   *expression
		text string
	}{{&a.host, k.Host}, {&a.port, port}, {&a.database, k.DBName}, {&a.user, k.Username}, {&a.password, k.Password}} {
		*f.to = expression{text: f.text, isText: true}
	}
	if a.segs, err = parseSQLParams(flowOf(p), p["query"].(string)); err != nil {
		return nil, fmt.Errorf("option query: %w", err)
	}
	if err := a.common(p); err != nil {
		return nil, err
	}
	return a, nil
}

// ---- named parameters of sql2

// sqlSegment is a piece of an sql2 statement: text, or a parameter.
type sqlSegment struct {
	text string
	name string      // :#name, the header
	expr *expression // :#${expression}
}

// parseSQLParams cuts a statement at its parameters, :#name and :#${simple
// expression}. Quoted text ('...') has none.
func parseSQLParams(flow *flowProperties, q string) ([]sqlSegment, error) {
	var segs []sqlSegment
	var text strings.Builder
	inQuote := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			inQuote = !inQuote // '' inside a text is two quotes, which toggles twice
			text.WriteByte(c)
		case !inQuote && c == ':' && i+1 < len(q) && q[i+1] == '#':
			rest := q[i+2:]
			var seg sqlSegment
			n := 0
			if strings.HasPrefix(rest, "${") {
				end := strings.IndexByte(rest, '}')
				if end < 0 {
					return nil, fmt.Errorf("%q: missing }", clip(q[i:]))
				}
				x, err := compileExpressionIn(flow, "simple", rest[:end+1])
				if err != nil {
					return nil, err
				}
				seg.expr, n = &x, end+1
			} else {
				for n < len(rest) && (rest[n] == '_' || rest[n] == '.' || rest[n] == '-' || isVAlpha(rest[n]) || rest[n] >= '0' && rest[n] <= '9') {
					n++
				}
				if n == 0 {
					text.WriteByte(c)
					continue
				}
				seg.name = rest[:n]
			}
			if text.Len() > 0 {
				segs = append(segs, sqlSegment{text: text.String()})
				text.Reset()
			}
			segs = append(segs, seg)
			i += 1 + n
		default:
			text.WriteByte(c)
		}
	}
	if text.Len() > 0 {
		segs = append(segs, sqlSegment{text: text.String()})
	}
	return segs, nil
}

// statement writes the statement of an sql2 step for m, with a marker for each
// parameter, and the values of them.
func (a *sqlStep) statement(m message.Message) (string, []any, error) {
	var b strings.Builder
	var args []any
	for _, s := range a.segs {
		switch {
		case s.expr != nil:
			v, err := s.expr.value(m)
			if err != nil {
				return "", nil, err
			}
			args = append(args, sqlArg(v))
		case s.name != "":
			v := headerValue(m, s.name)
			if v == nil {
				return "", nil, fmt.Errorf("sql2: there is no header %q for the parameter :#%s", s.name, s.name)
			}
			args = append(args, sqlArg(v))
		default:
			b.WriteString(s.text)
			continue
		}
		b.WriteString(a.dialect.placeholder(len(args)))
	}
	return b.String(), args, nil
}

// sqlArg is a value a driver can take as a parameter.
func sqlArg(v any) any {
	switch x := v.(type) {
	case nil, string, []byte, bool, int64, float64, time.Time:
		return v
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case float32:
		return float64(x)
	}
	return render(v)
}

// ---- running a statement

func (a *sqlStep) Process(ctx context.Context, m message.Message) (message.Message, error) {
	var query string
	var args []any
	var err error
	if a.named {
		if query, args, err = a.statement(m); err != nil {
			return nil, err
		}
	} else if query, err = a.query.eval(m); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	for strings.HasSuffix(query, ";") { // Oracle takes no ; and the rest not two
		query = strings.TrimSpace(strings.TrimSuffix(query, ";"))
	}
	if query == "" {
		return nil, fmt.Errorf("sql: the query is empty")
	}

	c, err := a.settings(m)
	if err != nil {
		return nil, err
	}
	db, release, err := a.pool.get(a.key(c), func() (*sql.DB, error) { return a.open(c) })
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	if sqlReturnsRows(query) {
		rs, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("sql: %w", err)
		}
		defer rs.Close()
		xml, n, err := sqlResultXML(rs)
		if err != nil {
			return nil, fmt.Errorf("sql: %w", err)
		}
		m[message.Body] = xml
		m[message.ContentType] = "application/xml"
		m["numberOfRecords"] = n
	} else {
		res, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("sql: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			n = -1 // the driver does not know
		}
		m["numberOfRecords"] = int(n)
	}
	m["hasErrors"] = false
	return m, nil
}

// settings evaluates the connection options for m.
func (a *sqlStep) settings(m message.Message) (sqlSettings, error) {
	var c sqlSettings
	var err error
	for _, f := range []struct {
		to   *string
		from expression
		what string
	}{{&c.host, a.host, "host"}, {&c.database, a.database, "database"}, {&c.user, a.user, "username"}, {&c.password, a.password, "password"}} {
		if *f.to, err = f.from.eval(m); err != nil {
			return c, fmt.Errorf("sql: option %s: %w", f.what, err)
		}
	}
	c.host = strings.TrimSpace(c.host)
	if c.host == "" {
		return c, fmt.Errorf("sql: the host is empty")
	}
	port, err := a.port.eval(m)
	if err != nil {
		return c, fmt.Errorf("sql: option port: %w", err)
	}
	if port = strings.TrimSpace(port); port == "" {
		c.port = a.dialect.defaultPort
	} else if c.port, err = strconv.Atoi(port); err != nil || c.port < 1 || c.port > 65535 {
		return c, fmt.Errorf("sql: the port %q is not a port number", port)
	}
	return c, nil
}

// key identifies a connection; it holds the password, hashed.
func (a *sqlStep) key(c sqlSettings) string {
	h := sha256.Sum256([]byte(strings.Join([]string{a.dialect.name, c.host, strconv.Itoa(c.port), c.database, c.user, c.password, strconv.FormatBool(a.ssl)}, "\x00")))
	return hex.EncodeToString(h[:])
}

func (a *sqlStep) open(c sqlSettings) (*sql.DB, error) {
	var tlsConf *tls.Config
	if a.ssl {
		tlsConf = &tls.Config{ServerName: c.host, RootCAs: a.roots, MinVersion: a.tlsMin, MaxVersion: a.tlsMax}
	}
	db, err := a.dialect.open(c, tlsConf, a.connectTimeout)
	if err != nil {
		return nil, fmt.Errorf("sql: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(30 * time.Second)
	db.SetConnMaxLifetime(10 * time.Minute)
	return db, nil
}

// sqlReturnsRows tells if a statement returns rows: it starts with SELECT, WITH,
// SHOW, DESCRIBE, EXPLAIN, VALUES or TABLE, or it has a RETURNING clause.
func sqlReturnsRows(q string) bool {
	s := strings.TrimSpace(q)
	for { // leading comments
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
				continue
			}
			return false
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = strings.TrimSpace(s[i+2:])
				continue
			}
			return false
		}
		break
	}
	word := s
	if i := strings.IndexFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') }); i >= 0 {
		word = s[:i]
	}
	switch strings.ToLower(word) {
	case "select", "with", "show", "describe", "desc", "explain", "values", "table", "pragma":
		return true
	}
	return strings.Contains(strings.ToLower(s), " returning ")
}
