package impl

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// NewSource creates the message producer for a source node. A "timer" URI
// gives a Timer. Other URIs give no producer (nil, nil): until real sources
// (https, file, ...) exist, their messages are sent to the flow explicitly.
func NewSource(n *flowdef.Node) (stepdef.Source, error) {
	scheme, _, _ := strings.Cut(n.URI, ":")
	if scheme != "timer" {
		return nil, nil
	}

	period, err := intOption(n.Options, "period", 1000)
	if err != nil {
		return nil, err
	}
	count, err := intOption(n.Options, "numbers", 0)
	if err != nil {
		return nil, err
	}
	if period <= 0 || count < 0 {
		return nil, fmt.Errorf("timer needs period > 0 and numbers >= 0")
	}
	return Timer{Period: time.Duration(period) * time.Millisecond, Count: count}, nil
}

// Timer emits a message every Period with the counter (1, 2, 3, ...) as body.
// Count limits the number of messages; 0 means unlimited.
type Timer struct {
	Period time.Duration
	Count  int
}

func (t Timer) Run(ctx context.Context, emit func(*message.Message) error) error {
	ticker := time.NewTicker(t.Period)
	defer ticker.Stop()

	for i := 1; t.Count == 0 || i <= t.Count; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if emit(message.New(i)) != nil {
			return nil // flow is stopping
		}
	}
	return nil
}

// intOption reads an integer option given as a JSON number or a numeric string,
// since DIL converted from XML often stores numbers as strings.
func intOption(opts map[string]any, key string, def int) (int, error) {
	switch v := opts[key].(type) {
	case nil:
		return def, nil
	case float64:
		return int(v), nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("option %s: %q is not a number", key, v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("option %s: %v is not a number", key, v)
	}
}
