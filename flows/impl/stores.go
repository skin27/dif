package impl

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"

	stepdef "dif/steps/definition"
)

// A store in dil.core.stores is a named key-value store of the channel
// runtime: name, durable (false), ttl (milliseconds an entry lives after it
// was last put; 0 for ever) and maxKeys (0 for the runtime's default). Steps
// refer to it by its name or, through a direct import, as <namespace>:<name>;
// both refer to the one store the runtime has by that name.

// storeName is the form of a store's name: no colon, which separates a
// namespace from a name and a name from a key.
var storeName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,199}$`)

// dilStore is a store as written; check reads it.
type dilStore struct {
	Name   string
	Fields map[string]any // the other fields, as written
}

func (s *dilStore) UnmarshalJSON(data []byte) error {
	var all map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	name, _ := all["name"].(string)
	delete(all, "name")
	*s = dilStore{Name: name, Fields: all}
	return nil
}

func (s dilStore) MarshalJSON() ([]byte, error) {
	out := map[string]any{"name": s.Name}
	maps.Copy(out, s.Fields)
	return json.Marshal(out)
}

// store checks the store's fields and returns it as steps get it. The
// errors name the store and the field, as DIL converted from XML may write a
// number or a boolean as text.
func (s dilStore) store() (stepdef.Store, error) {
	out := stepdef.Store{Name: s.Name}
	if !storeName.MatchString(s.Name) {
		return out, fmt.Errorf("store %q: the name must be letters, digits, '_', '.' or '-' (no ':')", s.Name)
	}
	for _, k := range slices.Sorted(maps.Keys(s.Fields)) {
		v := s.Fields[k]
		switch k {
		case "durable":
			switch v {
			case true, "true":
				out.Durable = true
			case false, "false":
			default:
				return out, fmt.Errorf("store %q: durable must be true or false", s.Name)
			}
		case "ttl", "maxKeys":
			n, err := nonNegativeInt(v)
			if err != nil {
				return out, fmt.Errorf("store %q: %s: %w", s.Name, k, err)
			}
			if k == "ttl" {
				if int64(n) > int64((1<<63-1)/time.Millisecond) {
					return out, fmt.Errorf("store %q: ttl is too large", s.Name)
				}
				out.TTL = time.Duration(n) * time.Millisecond
			} else {
				out.MaxKeys = n
			}
		default:
			return out, fmt.Errorf("store %q: unknown field %s; a store has name, durable, ttl and maxKeys", s.Name, k)
		}
	}
	return out, nil
}

// StoreInfo describes a store in dil.core.stores. TTL is in milliseconds.
type StoreInfo struct {
	Name    string `json:"name"`
	Durable bool   `json:"durable"`
	TTL     int64  `json:"ttl,omitempty"`
	MaxKeys int    `json:"maxKeys,omitempty"`
}

// StoreInfo describes the document's stores.
func (d *Document) StoreInfo() []StoreInfo {
	out := []StoreInfo{}
	for _, s := range d.doc.DIL.Core.Stores.Store {
		st, _ := s.store() // checked by DecodeDocument
		out = append(out, StoreInfo{Name: st.Name, Durable: st.Durable, TTL: st.TTL.Milliseconds(), MaxKeys: st.MaxKeys})
	}
	return out
}

// storeBindings returns the stores the document's steps may refer to: its
// own by name, and those of its direct imports as <namespace>:<name>.
func (d *Document) storeBindings() map[string]stepdef.Store {
	out := map[string]stepdef.Store{}
	for _, s := range d.doc.DIL.Core.Stores.Store {
		out[s.Name], _ = s.store()
	}
	for _, i := range d.Imports() {
		other := d.imports[i.Namespace]
		if other == nil {
			continue
		}
		for _, s := range other.doc.DIL.Core.Stores.Store {
			if _, ok := out[i.Namespace+":"+s.Name]; !ok {
				out[i.Namespace+":"+s.Name], _ = s.store()
			}
		}
	}
	return out
}
