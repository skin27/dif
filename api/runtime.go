package api

import (
	"fmt"

	flowdef "dif/flows/definition"
	"dif/internal/channels"
	"dif/internal/stomp"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
)

type ChannelConfig = channels.Config
type QueueConfig = channels.QueueConfig
type RequestConfig = channels.RequestConfig
type TopicConfig = channels.TopicConfig
type IdempotencyConfig = channels.IdempotencyConfig
type ChannelStatus = channels.Status
type STOMPConfig = channels.STOMPConfig
type STOMPTLSConfig = channels.STOMPTLSConfig

// STOMPServer serves the channels marked served to STOMP 1.2 clients; see
// Runtime.NewSTOMPServer. STOMPStatus counts its connections and subscriptions.
type STOMPServer = stomp.Server
type STOMPStatus = stomp.Status

// Runtime owns an isolated set of local channels and their storage. Stop its
// loaded flows before Close. The package-level Load functions remain compatible
// and continue sharing the process's default in-memory channels.
type Runtime struct {
	channels *stepimpl.Channels
	events   *eventObserver
	policy   *Policy
}

func ValidateChannelConfig(config ChannelConfig) error { return channels.Validate(config) }

func NewRuntime(config ChannelConfig) (*Runtime, error) {
	r, err := stepimpl.NewChannels(config)
	if err != nil {
		return nil, err
	}
	return &Runtime{channels: r, events: newEventObserver(r)}, nil
}
func (r *Runtime) Close() error                  { return r.channels.Close() }
func (r *Runtime) Failed() <-chan struct{}       { return r.channels.Failed() }
func (r *Runtime) Err() error                    { return r.channels.Err() }
func (r *Runtime) ChannelStatus() ChannelStatus  { return r.channels.Snapshot() }
func (r *Runtime) DurableQueue(name string) bool { return r.channels.Queue(name).Durable() }

// ServedTopic reports whether the configuration serves the topic to STOMP clients.
func (r *Runtime) ServedTopic(name string) bool { _, ok := r.channels.ServedTopic(name); return ok }

// NewSTOMPServer returns a server for the served channels, or nil when the
// configuration has no stomp listener. Set its Passcode (and TLS when the
// configuration names an identity), then Start it once the flows run and
// Close it before stopping them.
func (r *Runtime) NewSTOMPServer() *STOMPServer { return stomp.New(r.channels.Runtime) }
func (r *Runtime) Load(path string, onResult func(*Result, error)) (*Flow, error) {
	data, err := readDocument(path)
	if err != nil {
		return nil, err
	}
	return r.LoadBytes(data, onResult)
}
func (r *Runtime) LoadBytes(data []byte, onResult func(*Result, error)) (*Flow, error) {
	if err := r.channels.Check(); err != nil {
		return nil, err
	}
	if r.policy != nil {
		info, err := InspectBytes(data)
		if err != nil {
			return nil, fmt.Errorf("runtime load: %w", err)
		}
		if ds := checkFlowPolicy(info, r.policy); len(ds) > 0 {
			return nil, policyError(ds)
		}
	}
	f, err := parseFlow(data, func(n *flowdef.Node) (stepdef.Processor, error) {
		return steps.ProcessorWithParams(n, stepdef.Params{stepimpl.ChannelRuntimeKey: r.channels, stepimpl.FlowRuntimeKey: n.Flow})
	})
	if err != nil {
		return nil, fmt.Errorf("runtime load: %w", err)
	}
	return newFlow(f, onResult, r.events), nil
}

// SetPolicy makes the runtime refuse to load a program (or flow) whose
// capability manifest p does not grant; nil (the default) enforces nothing.
// Start the loaded flows with WithPolicy to check targets known only per
// message as well.
func (r *Runtime) SetPolicy(p *Policy) { r.policy = p }
