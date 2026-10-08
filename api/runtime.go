package api

import (
	"fmt"
	"os"

	"dif/engine"
	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	"dif/internal/channels"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
)

type ChannelConfig = channels.Config
type QueueConfig = channels.QueueConfig
type RequestConfig = channels.RequestConfig
type TopicConfig = channels.TopicConfig
type IdempotencyConfig = channels.IdempotencyConfig
type ChannelStatus = channels.Status

// Runtime owns an isolated set of local channels and their storage. Stop its
// loaded flows before Close. The package-level Load functions remain compatible
// and continue sharing the process's default in-memory channels.
type Runtime struct{ channels *stepimpl.Channels }

func ValidateChannelConfig(config ChannelConfig) error { return channels.Validate(config) }

func NewRuntime(config ChannelConfig) (*Runtime, error) {
	r, err := stepimpl.NewChannels(config)
	if err != nil {
		return nil, err
	}
	return &Runtime{channels: r}, nil
}
func (r *Runtime) Close() error                  { return r.channels.Close() }
func (r *Runtime) Failed() <-chan struct{}       { return r.channels.Failed() }
func (r *Runtime) Err() error                    { return r.channels.Err() }
func (r *Runtime) ChannelStatus() ChannelStatus  { return r.channels.Snapshot() }
func (r *Runtime) DurableQueue(name string) bool { return r.channels.Queue(name).Durable() }
func (r *Runtime) Load(path string, onResult func(*Result, error)) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return r.LoadBytes(data, onResult)
}
func (r *Runtime) LoadBytes(data []byte, onResult func(*Result, error)) (*Flow, error) {
	if err := r.channels.Check(); err != nil {
		return nil, err
	}
	f, err := flowimpl.Parse(data, func(n *flowdef.Node) (stepdef.Processor, error) {
		return steps.ProcessorWithParams(n, stepdef.Params{stepimpl.ChannelRuntimeKey: r.channels})
	})
	if err != nil {
		return nil, fmt.Errorf("runtime load: %w", err)
	}
	return &Flow{Runner: engine.NewRunner(f, onResult)}, nil
}
