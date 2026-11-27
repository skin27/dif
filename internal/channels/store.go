package channels

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultMaxKeys bounds a store whose declaration does not.
const DefaultMaxKeys = 100000

// StoreConfig is how a program declares a store. TTLMS is the milliseconds an
// entry lives after it was last put, 0 for ever; MaxKeys 0 is DefaultMaxKeys.
type StoreConfig struct {
	Durable bool  `json:"durable,omitempty"`
	TTLMS   int64 `json:"ttl,omitempty"`
	MaxKeys int   `json:"maxKeys,omitempty"`
}

func (c StoreConfig) String() string {
	kind := "memory"
	if c.Durable {
		kind = "durable"
	}
	return fmt.Sprintf("%s, ttl %d ms, maxKeys %d", kind, c.TTLMS, c.MaxKeys)
}

// Store is a named key-value store of a runtime. A durable store keeps its
// entries in the journal; a put or remove returns once the record is synced.
// Values keep their type, as messages in a durable queue do. Safe for
// concurrent use.
//
// Locks: mutations of a durable store hold r.mu and then s.mu, so compaction,
// which runs under r.mu, may read the entries; Get holds only s.mu.
type Store struct {
	r       *Runtime
	name    string
	config  StoreConfig
	mu      sync.RWMutex
	entries map[string]storeEntry
}

type storeEntry struct {
	v       value // the encoded value; decoding it gives a fresh copy
	data    []byte
	expires int64 // unix milliseconds; 0: never
}

func (e storeEntry) live(now int64) bool { return e.expires == 0 || now < e.expires }

// DeclareStore opens the store name, or returns it when it is already open
// with the same settings. A durable store takes over the entries the journal
// holds for it.
func (r *Runtime) DeclareStore(name string, c StoreConfig) (*Store, error) {
	if name == "" || c.TTLMS < 0 || c.TTLMS > int64((1<<63-1)/time.Millisecond) || c.MaxKeys < 0 {
		return nil, fmt.Errorf("invalid store declaration %q", name)
	}
	if c.MaxKeys == 0 {
		c.MaxKeys = DefaultMaxKeys
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storesMu.Lock()
	defer r.storesMu.Unlock()
	if s := r.stores[name]; s != nil {
		if s.config != c {
			return nil, fmt.Errorf("store %q is already declared as %s; declared again as %s", name, s.config, c)
		}
		return s, nil
	}
	if c.Durable && r.file == nil {
		return nil, fmt.Errorf("store %q is durable: the channel runtime has no storage directory; configure channels.directory", name)
	}
	s := &Store{r: r, name: name, config: c, entries: map[string]storeEntry{}}
	if c.Durable && r.pending[name] != nil {
		s.entries = r.pending[name]
		delete(r.pending, name)
	}
	if r.stores == nil {
		r.stores = map[string]*Store{}
	}
	r.stores[name] = s
	return s, nil
}

// Store returns the store declared as name, nil if none is.
func (r *Runtime) Store(name string) *Store {
	r.storesMu.RLock()
	defer r.storesMu.RUnlock()
	return r.stores[name]
}

func (s *Store) Name() string        { return s.name }
func (s *Store) Config() StoreConfig { return s.config }

// Get returns the value of key; false when it is not set or has expired.
func (s *Store) Get(key string) (any, bool, error) {
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok || !e.live(time.Now().UnixMilli()) {
		return nil, false, nil
	}
	v, err := decodeValue(e.v)
	if err != nil {
		return nil, false, fmt.Errorf("store %s: %w", s.name, err)
	}
	return v, true, nil
}

// Put sets key to v. A new key in a full store fails; nothing is evicted.
func (s *Store) Put(key string, v any) error {
	if key == "" || len(key) > 4096 {
		return fmt.Errorf("store %s: a key must have 1..4096 bytes", s.name)
	}
	w, err := encodeValue(v, 0)
	if err != nil {
		return fmt.Errorf("store %s: %w", s.name, err)
	}
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	if len(data) > s.r.config.MaxMessageBytes {
		return fmt.Errorf("store %s: value exceeds maxMessageBytes", s.name)
	}
	now := time.Now().UnixMilli()
	e := storeEntry{v: w}
	if s.config.Durable {
		e.data = data
	}
	if s.config.TTLMS > 0 {
		e.expires = now + s.config.TTLMS
	}
	if s.config.Durable {
		s.r.mu.Lock()
		defer s.r.mu.Unlock()
		if err := s.r.status(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[key]; !ok || !old.live(now) {
		if len(s.entries) >= s.config.MaxKeys {
			s.expire(now)
		}
		if len(s.entries) >= s.config.MaxKeys {
			return fmt.Errorf("store %s is full (%d keys)", s.name, s.config.MaxKeys)
		}
	}
	if s.config.Durable {
		// Compaction may run in append; it reads the entries, which holding
		// r.mu keeps unchanged, so s.mu is not taken again there.
		if err := s.r.append(record{Op: "store.put", Queue: s.name, Key: key, Data: data, Expires: e.expires}); err != nil {
			return err
		}
	}
	s.entries[key] = e
	return nil
}

// Remove removes key; a key that is not set is no error.
func (s *Store) Remove(key string) error {
	if s.config.Durable {
		s.r.mu.Lock()
		defer s.r.mu.Unlock()
		if err := s.r.status(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[key]; !ok {
		return nil
	}
	if s.config.Durable {
		if err := s.r.append(record{Op: "store.remove", Queue: s.name, Key: key}); err != nil {
			return err
		}
	}
	delete(s.entries, key)
	return nil
}

// Keys returns the keys that are set, sorted.
func (s *Store) Keys() []string {
	now := time.Now().UnixMilli()
	s.mu.RLock()
	keys := make([]string, 0, len(s.entries))
	for k, e := range s.entries {
		if e.live(now) {
			keys = append(keys, k)
		}
	}
	s.mu.RUnlock()
	sort.Strings(keys)
	return keys
}

// Len returns the number of keys that are set.
func (s *Store) Len() int {
	now := time.Now().UnixMilli()
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, e := range s.entries {
		if e.live(now) {
			n++
		}
	}
	return n
}

// expire drops the expired entries; the caller holds s.mu. A durable store
// writes no record for them: they are not replayed live, and compaction drops them.
func (s *Store) expire(now int64) {
	for k, e := range s.entries {
		if !e.live(now) {
			delete(s.entries, k)
		}
	}
}

// replayStore applies a store record. Stores are declared after the journal
// is open, so the entries wait in pending until their store is declared.
func (r *Runtime) replayStore(rec record) error {
	if rec.Queue == "" {
		return fmt.Errorf("journal store record without a store")
	}
	if r.pending == nil {
		r.pending = map[string]map[string]storeEntry{}
	}
	entries := r.pending[rec.Queue]
	if entries == nil {
		entries = map[string]storeEntry{}
		r.pending[rec.Queue] = entries
	}
	if rec.Op == "store.remove" {
		delete(entries, rec.Key)
		return nil
	}
	var w value
	if err := json.Unmarshal(rec.Data, &w); err != nil {
		return err
	}
	if _, err := decodeValue(w); err != nil {
		return err
	}
	e := storeEntry{v: w, data: rec.Data, expires: rec.Expires}
	if !e.live(time.Now().UnixMilli()) {
		delete(entries, rec.Key)
		return nil
	}
	entries[rec.Key] = e
	return nil
}

// compactStores writes the live entries of the durable stores, and of those
// the journal holds that are not declared (yet). The caller holds r.mu.
func (r *Runtime) compactStores(write func(record) error) error {
	now := time.Now().UnixMilli()
	put := func(name string, entries map[string]storeEntry) error {
		for k, e := range entries {
			if e.live(now) {
				if err := write(record{Op: "store.put", Queue: name, Key: k, Data: e.data, Expires: e.expires}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	r.storesMu.RLock()
	defer r.storesMu.RUnlock()
	for name, s := range r.stores {
		if s.config.Durable {
			if err := put(name, s.entries); err != nil {
				return err
			}
		}
	}
	for name, entries := range r.pending {
		if err := put(name, entries); err != nil {
			return err
		}
	}
	return nil
}

// StoreStatus is a store as monitoring shows it.
type StoreStatus struct {
	Name    string `json:"name"`
	Durable bool   `json:"durable"`
	Keys    int    `json:"keys"`
}

// storeStatus lists the declared stores; the caller holds r.mu.
func (r *Runtime) storeStatus() []StoreStatus {
	r.storesMu.RLock()
	stores := make([]*Store, 0, len(r.stores))
	for _, s := range r.stores {
		stores = append(stores, s)
	}
	r.storesMu.RUnlock()
	out := []StoreStatus{}
	for _, s := range stores {
		out = append(out, StoreStatus{Name: s.name, Durable: s.config.Durable, Keys: s.Len()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
