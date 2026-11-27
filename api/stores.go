package api

import (
	"fmt"

	flowimpl "dif/flows/impl"
	"dif/internal/channels"
	stepimpl "dif/steps/impl"
)

// StoreInfo describes a store a document declares in dil.core.stores; TTL is
// in milliseconds. StoreStatus is a store of a runtime as monitoring shows it.
type StoreInfo = flowimpl.StoreInfo
type StoreStatus = channels.StoreStatus

// declareStores opens every store the program declares in the runtime (the
// default channels when it is nil) before any flow is built, so a store has
// the settings its declaration gives, whichever flow uses it first.
func (p *PreparedProgram) declareStores(runtime *Runtime) error {
	ch := stepimpl.DefaultChannels()
	if runtime != nil {
		ch = runtime.channels
	}
	for _, doc := range p.resolved.Documents {
		for _, s := range doc.Stores {
			if _, err := ch.DeclareStore(s.Name, channels.StoreConfig{Durable: s.Durable, TTLMS: s.TTL, MaxKeys: s.MaxKeys}); err != nil {
				return fmt.Errorf("%s: %w", doc.Name, err)
			}
		}
	}
	return nil
}
