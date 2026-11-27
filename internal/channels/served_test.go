package channels

import (
	"strings"
	"testing"
)

func TestServedChannelConfiguration(t *testing.T) {
	listener := func(address string, tls *STOMPTLSConfig) *STOMPConfig {
		return &STOMPConfig{Address: address, Login: "app", TLS: tls}
	}
	servedQueue := map[string]QueueConfig{"orders": {Served: true}}
	for _, tc := range []struct {
		config Config
		err    string
	}{
		{Config{Queues: servedQueue}, "require a stomp listener"},
		{Config{Topics: map[string]TopicConfig{"audit": {Served: true}}}, "require a stomp listener"},
		{Config{Queues: map[string]QueueConfig{"orders": {}}, STOMP: listener("127.0.0.1:61613", nil)}, "no queue or topic is served"},
		{Config{Queues: servedQueue, STOMP: listener("0.0.0.0:61613", nil)}, "requires tls"},
		{Config{Queues: servedQueue, STOMP: listener(":61613", nil)}, "requires tls"},
		{Config{Queues: servedQueue, STOMP: listener("example.com:61613", nil)}, "requires tls"},
		{Config{Queues: servedQueue, STOMP: listener("127.0.0.1", nil)}, "host:port"},
		{Config{Queues: servedQueue, STOMP: listener("0.0.0.0:61613", &STOMPTLSConfig{})}, "tls.identity"},
		{Config{Queues: servedQueue, STOMP: &STOMPConfig{Address: "127.0.0.1:61613"}}, "login"},
		{Config{Queues: servedQueue, STOMP: &STOMPConfig{Address: "127.0.0.1:61613", Login: "app", MaxConnections: -1}}, "maxConnections"},
		{Config{Queues: servedQueue, STOMP: listener("localhost:61613", nil)}, ""},
		{Config{Queues: servedQueue, STOMP: listener("[::1]:61613", nil)}, ""},
		{Config{Queues: servedQueue, STOMP: listener("0.0.0.0:61613", &STOMPTLSConfig{Identity: "server.p12"})}, ""},
	} {
		err := Validate(tc.config)
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%+v: %v, want %q", tc.config.STOMP, err, tc.err)
		}
	}
}

func TestServedLookupsDoNotCreateChannels(t *testing.T) {
	r := openTest(t, Config{
		Queues: map[string]QueueConfig{"orders": {Served: true}, "private": {}},
		Topics: map[string]TopicConfig{"audit": {Served: true}, "hidden": {}},
		STOMP:  &STOMPConfig{Address: "127.0.0.1:0", Login: "app"},
	})
	if _, ok := r.ServedQueue("orders"); !ok {
		t.Fatal("orders not served")
	}
	if _, ok := r.ServedTopic("audit"); !ok {
		t.Fatal("audit not served")
	}
	for _, name := range []string{"private", "missing"} {
		if q, ok := r.ServedQueue(name); ok || q != nil {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"hidden", "missing"} {
		if topic, ok := r.ServedTopic(name); ok || topic != nil {
			t.Fatal(name)
		}
	}
	status := r.Snapshot()
	if len(status.Queues) != 2 || len(status.Topics) != 2 {
		t.Fatal(status)
	}
	if c := r.STOMP(); c == nil || c.MaxConnections != DefaultMaxConnections {
		t.Fatal(c)
	}
}
