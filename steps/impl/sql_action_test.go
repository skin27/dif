package impl

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// fakeDB is a database for the sql steps to talk to: it keeps the statements it
// gets and answers them with respond.
type fakeDB struct {
	mu       sync.Mutex
	stmts    []fakeStmt
	opened   atomic.Int32 // handles opened (sql.DB)
	closed   atomic.Int32 // handles closed
	respond  func(query string, args []driver.Value) fakeAnswer
	settings []sqlSettings
	tls      []*tls.Config
	delay    time.Duration
}

type fakeStmt struct {
	query string
	args  []driver.Value
}

type fakeAnswer struct {
	cols     []string
	types    []string
	rows     [][]driver.Value
	affected int64
	err      error
}

func (f *fakeDB) last(t *testing.T) fakeStmt {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stmts) == 0 {
		t.Fatal("no statement was run")
	}
	return f.stmts[len(f.stmts)-1]
}

type fakeConnector struct{ db *fakeDB }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn{c.db}, nil }
func (c fakeConnector) Driver() driver.Driver                        { return nil }

type fakeConn struct{ db *fakeDB }

func (fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: use QueryContext")
}
func (c fakeConn) Close() error            { c.db.closed.Add(1); return nil }
func (fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fake: no transactions") }

func (c fakeConn) answer(ctx context.Context, query string, named []driver.NamedValue) (fakeAnswer, error) {
	args := make([]driver.Value, len(named))
	for i, n := range named {
		args[i] = n.Value
	}
	c.db.mu.Lock()
	c.db.stmts = append(c.db.stmts, fakeStmt{query, args})
	c.db.mu.Unlock()
	if c.db.delay > 0 {
		select {
		case <-time.After(c.db.delay):
		case <-ctx.Done():
			return fakeAnswer{}, ctx.Err()
		}
	}
	a := c.db.respond(query, args)
	return a, a.err
}

func (c fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	a, err := c.answer(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &fakeRows{a: a}, nil
}

func (c fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	a, err := c.answer(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(a.affected), nil
}

type fakeRows struct {
	a fakeAnswer
	i int
}

func (r *fakeRows) Columns() []string { return r.a.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.a.rows) {
		return io.EOF
	}
	copy(dest, r.a.rows[r.i])
	r.i++
	return nil
}
func (r *fakeRows) ColumnTypeDatabaseTypeName(i int) string {
	if i < len(r.a.types) {
		return r.a.types[i]
	}
	return ""
}

// installFakeDB adds the database "fake" to the sql steps, with the placeholders
// of style $1.
func installFakeDB(t *testing.T, respond func(query string, args []driver.Value) fakeAnswer) *fakeDB {
	t.Helper()
	db := &fakeDB{respond: respond}
	sqlDialects["fake"] = &sqlDialect{
		name:        "fake",
		defaultPort: 4242,
		placeholder: func(i int) string { return "$" + strconv.Itoa(i) },
		open: func(c sqlSettings, tlsConf *tls.Config, _ time.Duration) (*sql.DB, error) {
			db.mu.Lock()
			db.settings = append(db.settings, c)
			db.tls = append(db.tls, tlsConf)
			db.mu.Unlock()
			db.opened.Add(1)
			return sql.OpenDB(fakeConnector{db}), nil
		},
	}
	sqlDialectNames["fake"] = "fake"
	t.Cleanup(func() { delete(sqlDialects, "fake"); delete(sqlDialectNames, "fake") })
	return db
}

func sqlProcessor(t *testing.T, opts map[string]any) stepdef.ActionProcessor {
	t.Helper()
	o := map[string]any{"query": "${body}", "connectionType": "fake", "host": "db.example", "database": "main", "username": "me", "password": "pw"}
	for k, v := range opts {
		o[k] = v
	}
	return mustProcessor(t, stepdef.Action, "sql", o).(stepdef.ActionProcessor)
}

func runSQL(t *testing.T, p stepdef.ActionProcessor, body string, headers map[string]any) message.Message {
	t.Helper()
	m := message.New(body)
	for k, v := range headers {
		m[k] = v
	}
	out, err := p.Process(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSQLSelectWritesTheResultSet(t *testing.T) {
	when := time.Date(2023, 3, 21, 10, 30, 5, 120000000, time.UTC)
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer {
		return fakeAnswer{
			cols:  []string{"ID", "name", "version()", "count(*)", "1+1", "note", "born", "seen", "ok", "blob", "price", "x<y"},
			types: []string{"INT", "TEXT", "", "", "", "TEXT", "DATE", "TIMESTAMP", "BOOL", "BYTEA", "NUMERIC", ""},
			rows: [][]driver.Value{
				{int64(1), "Kees & <Co>", "8.0.44", int64(7), int64(2), nil, time.Date(2023, 3, 21, 0, 0, 0, 0, time.UTC), when, true, []byte{0xff, 0x00, 0x10}, 12.5, "a\x00b\x1fc"},
				{int64(2), []byte("bytes"), "", int64(0), int64(0), "", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), when, false, []byte("text"), float64(3), ""},
			},
		}
	})
	out := runSQL(t, sqlProcessor(t, nil), "SELECT * FROM t;", nil)
	want := "<ResultSet><ResultSize>2</ResultSize><Results>" +
		"<Result><ID>1</ID><name>Kees &amp; &lt;Co&gt;</name><version>8.0.44</version><count>7</count><_11>2</_11><note/><born>2023-03-21</born>" +
		"<seen>2023-03-21 10:30:05.12</seen><ok>true</ok><blob>/wAQ</blob><price>12.5</price><xy>abc</xy></Result>" +
		"<Result><ID>2</ID><name>bytes</name><version></version><count>0</count><_11>0</_11><note></note><born>2024-01-02</born>" +
		"<seen>2023-03-21 10:30:05.12</seen><ok>false</ok><blob>text</blob><price>3</price><xy></xy></Result>" +
		"</Results></ResultSet>"
	if out[message.Body] != want {
		t.Errorf("body =\n%v\nwant\n%v", out[message.Body], want)
	}
	if out["numberOfRecords"] != 2 || out["hasErrors"] != false || out[message.ContentType] != "application/xml" {
		t.Errorf("headers = %v", out)
	}
	if got := db.last(t).query; got != "SELECT * FROM t" {
		t.Errorf("the statement was %q", got)
	}
}

func TestSQLNoRows(t *testing.T) {
	installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{cols: []string{"id"}} })
	out := runSQL(t, sqlProcessor(t, nil), "select id from t where 1=0", nil)
	if out[message.Body] != "<ResultSet><ResultSize>0</ResultSize><Results/></ResultSet>" || out["numberOfRecords"] != 0 {
		t.Errorf("body = %v, numberOfRecords = %v", out[message.Body], out["numberOfRecords"])
	}
}

func TestSQLInsertLeavesTheBody(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{affected: 5} })
	out := runSQL(t, sqlProcessor(t, nil), "INSERT INTO t VALUES (1),\n(2)", map[string]any{"keep": "me"})
	if out[message.Body] != "INSERT INTO t VALUES (1),\n(2)" || out["numberOfRecords"] != 5 || out["hasErrors"] != false || out["keep"] != "me" {
		t.Errorf("message = %v", out)
	}
	if _, set := out[message.ContentType]; set {
		t.Errorf("content type = %v", out[message.ContentType])
	}
	if got := db.last(t).query; got != "INSERT INTO t VALUES (1),\n(2)" {
		t.Errorf("the statement was %q", got)
	}
}

func TestSQLWhichStatementsReturnRows(t *testing.T) {
	for q, want := range map[string]bool{
		"SELECT 1": true, "select 1": true, "  \n with x as (select 1) select * from x": true, "SHOW TABLES": true,
		"-- note\nSELECT 1": true, "/* note */ select 1": true, "describe t": true, "EXPLAIN select 1": true, "values (1)": true,
		"INSERT INTO t VALUES (1) RETURNING id": true,
		"INSERT INTO t VALUES (1)":              false, "update t set a = 1": false, "DELETE FROM t": false,
		"CREATE TABLE t (id int)": false, "selection": false, "-- only a comment": false, "": false,
	} {
		if got := sqlReturnsRows(q); got != want {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
}

func TestSQLQueryAndConnectionAreTemplates(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{affected: 1} })
	p := sqlProcessor(t, map[string]any{
		"query": "SELECT * FROM t WHERE id = ${header.id}", "host": "${header.DB_Host}", "port": "${header.DB_Port}",
		"database": "${header.DB_Database}", "username": "${header.DB_Username}", "password": "${header.DB_Password}",
	})
	h := map[string]any{"id": 7, "DB_Host": "one.example", "DB_Port": "5433", "DB_Database": "d1", "DB_Username": "u1", "DB_Password": "p1"}
	runSQL(t, p, "", h)
	if got := db.last(t).query; got != "SELECT * FROM t WHERE id = 7" {
		t.Errorf("the statement was %q", got)
	}
	if got := db.settings[0]; got != (sqlSettings{"one.example", "d1", "u1", "p1", 5433}) {
		t.Errorf("settings = %+v", got)
	}

	// The same settings share a handle; other settings get their own.
	runSQL(t, p, "", h)
	if n := db.opened.Load(); n != 1 {
		t.Errorf("%d handles opened for one connection", n)
	}
	h["DB_Host"] = "two.example"
	runSQL(t, p, "", h)
	if n := db.opened.Load(); n != 2 || db.settings[1].host != "two.example" {
		t.Errorf("%d handles opened for two connections: %+v", n, db.settings)
	}

	// An empty port is the port of the database.
	h["DB_Port"] = ""
	runSQL(t, p, "", h)
	if got := db.settings[2].port; got != 4242 {
		t.Errorf("port = %d, want the default", got)
	}
}

func TestSQLErrors(t *testing.T) {
	db := installFakeDB(t, func(q string, _ []driver.Value) fakeAnswer {
		if strings.Contains(q, "boom") {
			return fakeAnswer{err: errors.New("relation boom does not exist")}
		}
		return fakeAnswer{}
	})
	p := sqlProcessor(t, map[string]any{"host": "${header.h}", "port": "${header.p}"})
	for _, c := range []struct {
		body string
		h    map[string]any
		want string
	}{
		{"select boom", map[string]any{"h": "x"}, "sql: relation boom does not exist"},
		{"insert into boom values (1)", map[string]any{"h": "x"}, "sql: relation boom does not exist"},
		{" ; ", map[string]any{"h": "x"}, "the query is empty"},
		{"select 1", map[string]any{}, "the host is empty"},
		{"select 1", map[string]any{"h": "x", "p": "http"}, `the port "http" is not a port number`},
		{"select 1", map[string]any{"h": "x", "p": "70000"}, "is not a port number"},
	} {
		m := message.New(c.body)
		for k, v := range c.h {
			m[k] = v
		}
		if _, err := p.Process(context.Background(), m); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q %v: err = %v, want containing %q", c.body, c.h, err, c.want)
		}
	}
	_ = db
}

func TestSQLTimeout(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{} })
	db.delay = 2 * time.Second
	p := sqlProcessor(t, map[string]any{"socketTimeout": 50})
	start := time.Now()
	if _, err := p.Process(context.Background(), message.New("select 1")); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Errorf("err = %v, want the deadline", err)
	}
	if time.Since(start) > time.Second {
		t.Error("the statement was not stopped by the timeout")
	}
}

func TestSQLPasswordFromTheEnvironment(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{} })
	t.Setenv("DIF_SQL_PASSWORD", "from-env")
	o := map[string]any{"query": "${body}", "connectionType": "fake", "host": "h"}
	runSQL(t, mustProcessor(t, stepdef.Action, "sql", o).(stepdef.ActionProcessor), "select 1", nil)
	if got := db.settings[0].password; got != "from-env" {
		t.Errorf("password = %q", got)
	}
	o["password"] = "from-option"
	runSQL(t, mustProcessor(t, stepdef.Action, "sql", o).(stepdef.ActionProcessor), "select 1", nil)
	if got := db.settings[1].password; got != "from-option" {
		t.Errorf("password = %q", got)
	}
}

func TestSQLTLS(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{} })
	runSQL(t, sqlProcessor(t, nil), "select 1", nil)
	if db.tls[0] != nil {
		t.Error("TLS without useSSL")
	}
	runSQL(t, sqlProcessor(t, map[string]any{"useSSL": true, "tlsVersion": "TLSv1.3", "host": "tls.example"}), "select 1", nil)
	c := db.tls[1]
	if c == nil || c.MinVersion != tls.VersionTLS13 || c.MaxVersion != tls.VersionTLS13 || c.ServerName != "tls.example" || c.InsecureSkipVerify {
		t.Errorf("tls config = %+v", c)
	}
	runSQL(t, sqlProcessor(t, map[string]any{"useSSL": true, "host": "tls2.example"}), "select 1", nil)
	if c := db.tls[2]; c.MinVersion != tls.VersionTLS12 || c.MaxVersion != 0 {
		t.Errorf("tls config = %+v", c)
	}
	for in, want := range map[string]uint16{"TLSv1": tls.VersionTLS10, "tlsv1.0": tls.VersionTLS10, "TLSv1.1": tls.VersionTLS11, "TLS1.2": tls.VersionTLS12, "1.3": tls.VersionTLS13} {
		if min, max, err := sqlTLSVersions(in); err != nil || min != want || max != want {
			t.Errorf("%q: %x %x %v, want %x", in, min, max, err, want)
		}
	}
}

func TestSQLInvalidOptions(t *testing.T) {
	ok := map[string]any{"query": "${body}", "connectionType": "postgres", "host": "h"}
	with := func(k string, v any) map[string]any {
		o := map[string]any{}
		for kk, vv := range ok {
			o[kk] = vv
		}
		o[k] = v
		return o
	}
	wantInvalid(t, stepdef.Action, "sql", with("connectionType", "db2"), `option connectionType: "db2" is not one of`)
	wantInvalid(t, stepdef.Action, "sql", with("host", ""), "option host: required")
	wantInvalid(t, stepdef.Action, "sql", with("query", " "), "option query: required")
	wantInvalid(t, stepdef.Action, "sql", with("tlsVersion", "SSLv3"), "option tlsVersion")
	wantInvalid(t, stepdef.Action, "sql", with("query", "${nofunction()}"), "option query")
	wantInvalid(t, stepdef.Action, "sql", map[string]any{"query": "x"}, "missing required option connectionType")
	wantInvalid(t, stepdef.Action, "sql", with("unknown", 1), "unknown option")
	for _, name := range []string{"postgres", "PostgreSQL", "mysql8", "mysql", "mariadb", "sql_server", "sqlserver", "oracle"} {
		mustProcessor(t, stepdef.Action, "sql", with("connectionType", name))
	}
}

func TestSQLPoolClosesIdleHandles(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{} })
	old := sqlIdle
	sqlIdle = 30 * time.Millisecond
	defer func() { sqlIdle = old }()
	p := sqlProcessor(t, nil)
	runSQL(t, p, "select 1", nil)
	step := p.(*sqlStep)
	if step.pool.size() != 1 {
		t.Fatalf("pool size = %d", step.pool.size())
	}
	deadline := time.Now().Add(2 * time.Second)
	for step.pool.size() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if step.pool.size() != 0 || db.closed.Load() == 0 {
		t.Errorf("pool size = %d, connections closed = %d: the idle handle was kept", step.pool.size(), db.closed.Load())
	}
	// And it opens again when it is needed.
	runSQL(t, p, "select 1", nil)
	if db.opened.Load() != 2 {
		t.Errorf("opened = %d", db.opened.Load())
	}
}

func TestSQLPoolLimit(t *testing.T) {
	installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{} })
	p := sqlProcessor(t, map[string]any{"host": "${header.h}"})
	var err error
	for i := 0; i <= maxSQLHandles && err == nil; i++ {
		m := message.New("select 1")
		m["h"] = "host" + strconv.Itoa(i)
		_, err = p.Process(context.Background(), m)
	}
	if err == nil || !strings.Contains(err.Error(), "different connections") {
		t.Errorf("err = %v, want the limit", err)
	}
}

func TestSQLConcurrent(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer {
		return fakeAnswer{cols: []string{"a"}, rows: [][]driver.Value{{"x"}}}
	})
	p := sqlProcessor(t, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				out, err := p.Process(context.Background(), message.New("select a"))
				if err != nil || out["numberOfRecords"] != 1 {
					t.Errorf("out = %v, err = %v", out, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := db.opened.Load(); n != 1 {
		t.Errorf("%d handles opened", n)
	}
}

// ---- sql2

func sql2Processor(t *testing.T, query string) stepdef.ActionProcessor {
	t.Helper()
	conn := `{"dbtype":"fake","dbname":"tempdb","host":"api.example","port":"1433","username":"SA","password":"pw"}`
	return mustProcessor(t, stepdef.Action, "sql2", map[string]any{"query": query, "connection": conn}).(stepdef.ActionProcessor)
}

func TestSQL2NamedParameters(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer {
		return fakeAnswer{cols: []string{"EmployeeID"}, rows: [][]driver.Value{{int64(7)}}}
	})
	p := sql2Processor(t, "SELECT * FROM Employees WHERE EmployeeID = :#EmployeeID AND name = ':#not' AND n::int > :#${header.min} AND x = :#EmployeeID")
	out := runSQL(t, p, "", map[string]any{"EmployeeID": 7, "min": "3"})
	st := db.last(t)
	if st.query != "SELECT * FROM Employees WHERE EmployeeID = $1 AND name = ':#not' AND n::int > $2 AND x = $3" {
		t.Errorf("statement = %q", st.query)
	}
	if len(st.args) != 3 || st.args[0] != int64(7) || st.args[1] != "3" || st.args[2] != int64(7) {
		t.Errorf("args = %#v", st.args)
	}
	if got := db.settings[0]; got != (sqlSettings{"api.example", "tempdb", "SA", "pw", 1433}) {
		t.Errorf("settings = %+v", got)
	}
	if !strings.Contains(out[message.Body].(string), "<EmployeeID>7</EmployeeID>") {
		t.Errorf("body = %v", out[message.Body])
	}

	// Header names are not told apart by case, as in Camel.
	runSQL(t, p, "", map[string]any{"employeeid": 8, "min": 1})
	if st := db.last(t); st.args[0] != int64(8) || st.args[1] != int64(1) {
		t.Errorf("args = %#v", st.args)
	}

	m := message.New("")
	if _, err := p.Process(context.Background(), m); err == nil || !strings.Contains(err.Error(), `no header "EmployeeID"`) {
		t.Errorf("err = %v, want the missing header", err)
	}
}

func TestSQL2InsertAndBodyParameter(t *testing.T) {
	db := installFakeDB(t, func(string, []driver.Value) fakeAnswer { return fakeAnswer{affected: 1} })
	p := sql2Processor(t, "INSERT into Employees (EmployeeID, Email) VALUES (:#EmployeeID,'john.doe@galaxy@milkyway.stars'); ")
	out := runSQL(t, p, "kept", map[string]any{"EmployeeID": 3})
	if out[message.Body] != "kept" || out["numberOfRecords"] != 1 {
		t.Errorf("message = %v", out)
	}
	if got := db.last(t).query; got != "INSERT into Employees (EmployeeID, Email) VALUES ($1,'john.doe@galaxy@milkyway.stars')" {
		t.Errorf("statement = %q", got)
	}
	runSQL(t, sql2Processor(t, "select :#${body}"), "bodyvalue", nil)
	if st := db.last(t); st.query != "select $1" || st.args[0] != "bodyvalue" {
		t.Errorf("statement = %q %#v", st.query, st.args)
	}
}

func TestSQL2Dialects(t *testing.T) {
	for name, want := range map[string]string{"postgres": "$1,$2", "mysql8": "?,?", "sql_server": "@p1,@p2", "oracle": ":1,:2"} {
		segs, err := parseSQLParams(nil, ":#a,:#b")
		if err != nil {
			t.Fatal(err)
		}
		a := &sqlStep{dialect: sqlDialects[name], segs: segs, named: true}
		got, _, err := a.statement(message.Message{"a": "1", "b": "2"})
		if err != nil || got != want {
			t.Errorf("%s: %q, %v, want %q", name, got, err, want)
		}
	}
}

func TestSQL2InvalidOptions(t *testing.T) {
	conn := func(s string) map[string]any { return map[string]any{"query": "select 1", "connection": s} }
	wantInvalid(t, stepdef.Action, "sql2", map[string]any{"query": "select 1"}, "the connection is missing")
	wantInvalid(t, stepdef.Action, "sql2", conn("nope"), "option connection")
	wantInvalid(t, stepdef.Action, "sql2", conn(`{"dbtype":"db2","host":"h"}`), `dbtype "db2" is not one of`)
	wantInvalid(t, stepdef.Action, "sql2", conn(`{"dbtype":"postgres"}`), "host is missing")
	wantInvalid(t, stepdef.Action, "sql2", map[string]any{"query": "select :#${", "connection": `{"dbtype":"postgres","host":"h"}`}, "missing }")
	mustProcessor(t, stepdef.Action, "sql2", conn(`{"dbtype":"sqlserver","dbname":"d","host":"h","port":1433,"username":"u","password":"p"}`))
}

// ---- the real drivers

// Each driver builds its connector from the settings and the TLS configuration
// without reaching the server, and a connection to nobody fails with an error.
func TestSQLDriversRefuseToConnectToNobody(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close() // nothing listens there now

	for _, name := range []string{"postgres", "mysql8", "sql_server", "oracle"} {
		for _, tlsConf := range []*tls.Config{nil, {ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}} {
			d := sqlDialects[name]
			if d == nil {
				t.Fatalf("no dialect %s", name)
			}
			db, err := d.open(sqlSettings{"127.0.0.1", "db", "us er", "p@ss:w/rd", port}, tlsConf, time.Second)
			if err != nil {
				t.Errorf("%s: open: %v", name, err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err = db.PingContext(ctx)
			cancel()
			db.Close()
			if err == nil {
				t.Errorf("%s: connected to nobody", name)
			} else if strings.Contains(err.Error(), "p@ss") {
				t.Errorf("%s: the error shows the password: %v", name, err)
			}
		}
	}
}
