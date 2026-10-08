package impl

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// sqlPool keeps the database handles of a sql step, one for each set of
// connection settings. Settings can come from the headers of the message, so
// the number of handles is limited, and a handle that nobody has used for
// sqlIdle is closed (the engine has no hook to close a step's resources when its
// flow stops, so a handle must not outlive its use).
type sqlPool struct {
	mu  sync.Mutex
	dbs map[string]*pooledDB
}

type pooledDB struct {
	db   *sql.DB
	refs int
	idle *time.Timer
}

// sqlIdle is how long a database handle that is not in use is kept; a variable
// so that tests can shorten it.
var sqlIdle = time.Minute

// maxSQLHandles is the number of different connections a step keeps.
const maxSQLHandles = 32

// get returns the handle for key, opened by open the first time, and a function
// to call when the statement is done.
func (p *sqlPool) get(key string, open func() (*sql.DB, error)) (*sql.DB, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.dbs[key]
	if e == nil {
		if len(p.dbs) >= maxSQLHandles {
			return nil, nil, fmt.Errorf("sql: more than %d different connections in use; the connection options of the step change too much", maxSQLHandles)
		}
		db, err := open()
		if err != nil {
			return nil, nil, err
		}
		e = &pooledDB{db: db}
		if p.dbs == nil {
			p.dbs = map[string]*pooledDB{}
		}
		p.dbs[key] = e
	}
	if e.idle != nil {
		e.idle.Stop()
		e.idle = nil
	}
	e.refs++
	return e.db, func() { p.release(key, e) }, nil
}

func (p *sqlPool) release(key string, e *pooledDB) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.refs--; e.refs > 0 {
		return
	}
	e.idle = time.AfterFunc(sqlIdle, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if e.refs == 0 && p.dbs[key] == e {
			delete(p.dbs, key)
			e.db.Close()
		}
	})
}

// size is the number of handles open.
func (p *sqlPool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.dbs)
}
