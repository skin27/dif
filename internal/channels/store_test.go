package channels

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
)

func storeConfig(dir string) Config {
	return Config{Directory: dir, MaxMessageBytes: 4096, MaxDiskBytes: 64 << 10}
}

func declare(t *testing.T, r *Runtime, name string, c StoreConfig) *Store {
	t.Helper()
	s, err := r.DeclareStore(name, c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustGet(t *testing.T, s *Store, key string) (any, bool) {
	t.Helper()
	v, ok, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

// storeValues are values of every type the codec keeps.
var storeValues = map[string]any{
	"text": "hello", "int": 42, "int64": int64(-7), "float": 2.5, "bool": true,
	"bytes": []byte{0, 1, 2}, "number": json.Number("12.50"), "nil": nil,
	"list": []any{"a", 1.5, false}, "strings": []string{"x", "y"},
	"map":     map[string]any{"a": map[string]any{"b": []any{"c"}}},
	"message": message.Message{message.Body: "b", "h": 1},
}

func checkValues(t *testing.T, s *Store) {
	t.Helper()
	for k, want := range storeValues {
		got, ok := mustGet(t, s, k)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: get %q = %#v, %v; want %#v", s.Name(), k, got, ok, want)
		}
	}
}

func TestStorePutGetRemove(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprint("durable=", durable), func(t *testing.T) {
			r := openTest(t, storeConfig(t.TempDir()))
			s := declare(t, r, "cache", StoreConfig{Durable: durable})
			for k, v := range storeValues {
				if err := s.Put(k, v); err != nil {
					t.Fatal(k, err)
				}
			}
			checkValues(t, s)

			// The value is a copy: changing what was put or got changes nothing.
			m := map[string]any{"a": 1}
			s.Put("copy", m)
			m["a"] = 2
			got, _ := mustGet(t, s, "copy")
			got.(map[string]any)["a"] = 3
			if again, _ := mustGet(t, s, "copy"); again.(map[string]any)["a"] != 1 {
				t.Errorf("value shared: %v", again)
			}

			if _, ok := mustGet(t, s, "missing"); ok {
				t.Error("missing key found")
			}
			if err := s.Remove("text"); err != nil {
				t.Fatal(err)
			}
			if err := s.Remove("text"); err != nil {
				t.Fatal("removing a missing key:", err)
			}
			if _, ok := mustGet(t, s, "text"); ok {
				t.Error("removed key found")
			}
			if n := s.Len(); n != len(storeValues) {
				t.Errorf("len = %d, want %d", n, len(storeValues))
			}
			if keys := s.Keys(); len(keys) != len(storeValues) || keys[0] != "bool" {
				t.Errorf("keys = %v", keys)
			}
			for _, key := range []string{"", strings.Repeat("k", 4097)} {
				if err := s.Put(key, 1); err == nil {
					t.Errorf("key of %d bytes accepted", len(key))
				}
			}
			if err := s.Put("chan", make(chan int)); err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Errorf("unsupported value: %v", err)
			}
			if err := s.Put("big", strings.Repeat("x", 5000)); err == nil || !strings.Contains(err.Error(), "maxMessageBytes") {
				t.Errorf("value too large: %v", err)
			}
		})
	}
}

func TestStoreTTLAndMaxKeys(t *testing.T) {
	r := openTest(t, storeConfig(t.TempDir()))
	s := declare(t, r, "short", StoreConfig{TTLMS: 200, MaxKeys: 2})
	s.Put("a", 1)
	s.Put("b", 2)
	if err := s.Put("c", 3); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("full store: %v", err)
	}
	if err := s.Put("a", 10); err != nil {
		t.Fatal("replacing a key in a full store:", err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, ok := mustGet(t, s, "a"); ok {
		t.Error("expired key found")
	}
	if s.Len() != 0 || len(s.Keys()) != 0 {
		t.Errorf("expired keys counted: %d %v", s.Len(), s.Keys())
	}
	if err := s.Put("c", 3); err != nil {
		t.Fatal("expired keys keep the store full:", err)
	}
	if v, ok := mustGet(t, s, "c"); !ok || v != 3 {
		t.Errorf("c = %v, %v", v, ok)
	}

	// A put renews the ttl.
	s.Put("d", 4)
	time.Sleep(120 * time.Millisecond)
	s.Put("d", 5)
	time.Sleep(120 * time.Millisecond)
	if v, ok := mustGet(t, s, "d"); !ok || v != 5 {
		t.Errorf("renewed key = %v, %v", v, ok)
	}

	if d := declare(t, r, "default", StoreConfig{}); d.Config().MaxKeys != DefaultMaxKeys {
		t.Errorf("default maxKeys = %d", d.Config().MaxKeys)
	}
}

func TestStoreDeclaration(t *testing.T) {
	r := openTest(t, storeConfig(t.TempDir()))
	a := declare(t, r, "rates", StoreConfig{Durable: true, TTLMS: 1000})
	if b := declare(t, r, "rates", StoreConfig{Durable: true, TTLMS: 1000}); b != a {
		t.Error("the same declaration opened another store")
	}
	if r.Store("rates") != a || r.Store("other") != nil {
		t.Error("lookup")
	}
	_, err := r.DeclareStore("rates", StoreConfig{Durable: true, TTLMS: 2000})
	if err == nil || !strings.Contains(err.Error(), `store "rates" is already declared`) {
		t.Errorf("different settings: %v", err)
	}
	for _, c := range []StoreConfig{{TTLMS: -1}, {MaxKeys: -1}} {
		if _, err := r.DeclareStore("bad", c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := r.DeclareStore("", StoreConfig{}); err == nil {
		t.Error("empty name accepted")
	}

	mem := openTest(t, Config{})
	if _, err := mem.DeclareStore("rates", StoreConfig{Durable: true}); err == nil || !strings.Contains(err.Error(), "channels.directory") {
		t.Errorf("durable store without a directory: %v", err)
	}
	if _, err := mem.DeclareStore("rates", StoreConfig{}); err != nil {
		t.Error(err)
	}
}

func TestStoreRecoveryAndCompaction(t *testing.T) {
	c := storeConfig(t.TempDir())
	r := openTest(t, c)
	s := declare(t, r, "cache", StoreConfig{Durable: true})
	exp := declare(t, r, "expiring", StoreConfig{Durable: true, TTLMS: 50})
	mem := declare(t, r, "memory", StoreConfig{})
	for k, v := range storeValues {
		if err := s.Put(k, v); err != nil {
			t.Fatal(err)
		}
	}
	s.Put("removed", 1)
	s.Remove("removed")
	exp.Put("gone", 1)
	mem.Put("lost", 1)
	r.Close()

	r = openTest(t, c)
	if r.Store("cache") != nil {
		t.Fatal("a store is open before it is declared")
	}
	s = declare(t, r, "cache", StoreConfig{Durable: true})
	checkValues(t, s)
	if _, ok := mustGet(t, s, "removed"); ok {
		t.Error("removed key recovered")
	}
	if v, ok := mustGet(t, declare(t, r, "memory", StoreConfig{}), "lost"); ok {
		t.Errorf("memory store recovered %v", v)
	}
	time.Sleep(60 * time.Millisecond)

	// Fill the journal until it compacts: the live entries stay, removed and
	// expired ones go, also those of "expiring", which this run does not declare.
	big := strings.Repeat("x", 1000)
	for i := 0; i < 40; i++ {
		if err := s.Put("churn", big); err != nil {
			t.Fatal(err)
		}
		if err := s.Remove("churn"); err != nil {
			t.Fatal(err)
		}
	}
	if r.Snapshot().JournalBytes > c.MaxDiskBytes/2 {
		t.Fatal("journal exceeded its bound")
	}
	r.Close()

	data, err := os.ReadFile(filepath.Join(c.Directory, "channels.journal"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"removed"`) || strings.Contains(string(data), `"gone"`) {
		t.Error("compaction kept a removed or expired entry")
	}

	r = openTest(t, c)
	checkValues(t, declare(t, r, "cache", StoreConfig{Durable: true}))
	if _, ok := mustGet(t, declare(t, r, "expiring", StoreConfig{Durable: true, TTLMS: 50}), "gone"); ok {
		t.Error("expired entry recovered")
	}
}

func TestStoreUndeclaredEntriesSurvive(t *testing.T) {
	c := storeConfig(t.TempDir())
	r := openTest(t, c)
	declare(t, r, "later", StoreConfig{Durable: true}).Put("k", "kept")
	r.Close()

	// A run that does not declare the store, or declares it in memory, keeps
	// its entries through compaction.
	r = openTest(t, c)
	if v, ok := mustGet(t, declare(t, r, "later", StoreConfig{}), "k"); ok {
		t.Errorf("memory store got journal entry %v", v)
	}
	other := declare(t, r, "other", StoreConfig{Durable: true})
	for i := 0; i < 40; i++ {
		other.Put("churn", strings.Repeat("y", 1000))
	}
	r.Close()

	r = openTest(t, c)
	if v, ok := mustGet(t, declare(t, r, "later", StoreConfig{Durable: true}), "k"); !ok || v != "kept" {
		t.Errorf("undeclared entry lost: %v, %v", v, ok)
	}
}

func TestStoreDiskBudget(t *testing.T) {
	c := storeConfig(t.TempDir())
	r := openTest(t, c)
	s := declare(t, r, "full", StoreConfig{Durable: true})
	var err error
	for i := 0; i < 100 && err == nil; i++ {
		err = s.Put(fmt.Sprint("k", i), strings.Repeat("z", 3000))
	}
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("disk budget: %v", err)
	}
	if r.Err() != nil {
		t.Fatal("a full budget failed the runtime:", r.Err())
	}
	// Removing still works: it may use the half of the budget kept for settlement.
	if err := s.Remove("k0"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreConcurrentUse(t *testing.T) {
	r := openTest(t, storeConfig(t.TempDir()))
	stores := []*Store{declare(t, r, "durable", StoreConfig{Durable: true}), declare(t, r, "memory", StoreConfig{})}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				for _, s := range stores {
					key := fmt.Sprint(g, "-", i%3)
					if err := s.Put(key, i); err != nil {
						t.Error(err)
						return
					}
					if _, _, err := s.Get(key); err != nil {
						t.Error(err)
					}
					if i%2 == 0 {
						s.Remove(key)
					}
					s.Len()
					r.Snapshot()
				}
			}
		}()
	}
	wg.Wait()
	snap := r.Snapshot()
	if len(snap.Stores) != 2 || snap.Stores[0] != (StoreStatus{Name: "durable", Durable: true, Keys: stores[0].Len()}) {
		t.Errorf("status = %+v", snap.Stores)
	}
}

func TestStoreAfterStorageFailure(t *testing.T) {
	r := openTest(t, storeConfig(t.TempDir()))
	s := declare(t, r, "cache", StoreConfig{Durable: true})
	r.file.Close() // a failed write
	if err := s.Put("k", 1); err == nil {
		t.Fatal("failed write reported success")
	}
	if _, ok := mustGet(t, s, "k"); ok {
		t.Error("failed put is visible")
	}
	if err := s.Put("k", 1); err == nil || !strings.Contains(err.Error(), "channel storage") {
		t.Errorf("put after failure: %v", err)
	}
}

func TestStoreTornRecord(t *testing.T) {
	c := storeConfig(t.TempDir())
	r := openTest(t, c)
	s := declare(t, r, "cache", StoreConfig{Durable: true})
	s.Put("kept", 1)
	s.Put("torn", 2)
	r.Close()
	p := filepath.Join(c.Directory, "channels.journal")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data[:len(data)-3], 0600); err != nil {
		t.Fatal(err)
	}
	r = openTest(t, c)
	s = declare(t, r, "cache", StoreConfig{Durable: true})
	if v, ok := mustGet(t, s, "kept"); !ok || v != 1 {
		t.Errorf("kept = %v, %v", v, ok)
	}
	if _, ok := mustGet(t, s, "torn"); ok {
		t.Error("a torn record was replayed")
	}
	if err := s.Put("after", 3); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r = openTest(t, c)
	if v, ok := mustGet(t, declare(t, r, "cache", StoreConfig{Durable: true}), "after"); !ok || v != 3 {
		t.Errorf("after the truncation: %v, %v", v, ok)
	}
}
