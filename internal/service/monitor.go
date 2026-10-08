package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"dif/api"
	"dif/engine"
)

func (s *Service) snapshot() (ready, started, stopping bool, flows []engine.FlowStatus) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ready, started, stopping = s.ready, s.started, s.stopping
	flows = make([]engine.FlowStatus, 0, len(s.runners))
	for _, r := range s.runners {
		status := r.Status()
		flows = append(flows, status)
		if status.State != engine.Started || status.Source == "starting" || status.Source == "failed" || status.Source == "cancelled" {
			ready = false
		}
	}
	return
}

// Handler exposes read-only operational data, never DIL, option values or
// message bodies. Deploy it on a private monitoring network.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"/livez", "/readyz", "/startupz"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			ready, started, _, _ := s.snapshot()
			ok := path == "/livez" || path == "/readyz" && ready || path == "/startupz" && started
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if !ok {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintln(w, "not ready")
				return
			}
			fmt.Fprintln(w, "ok")
		})
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		ready, started, stopping, flows := s.snapshot()
		active, queued := s.work.Counts()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(struct {
			Channels api.ChannelStatus   `json:"channels"`
			Ready    bool                `json:"ready"`
			Started  bool                `json:"started"`
			Stopping bool                `json:"stopping"`
			Active   int64               `json:"active"`
			Queued   int64               `json:"queued"`
			Flows    []engine.FlowStatus `json:"flows"`
		}{s.channelStatus(), ready, started, stopping, active, queued, flows})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		ready, _, _, flows := s.snapshot()
		active, queued := s.work.Counts()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		value := 0
		if ready {
			value = 1
		}
		fmt.Fprintf(w, "# TYPE dif_ready gauge\ndif_ready %d\n# TYPE dif_active_work gauge\ndif_active_work %d\n# TYPE dif_queued_messages gauge\ndif_queued_messages %d\n", value, active, queued)
		channels := s.channelStatus()
		fmt.Fprintf(w, "# TYPE dif_channel_journal_bytes gauge\ndif_channel_journal_bytes %d\n# TYPE dif_channel_storage_failures_total counter\ndif_channel_storage_failures_total %d\n", channels.JournalBytes, channels.StorageFailures)
		for _, q := range channels.Queues {
			label := strconv.Quote(q.Name)
			fmt.Fprintf(w, "dif_queue_waiting{queue=%s} %d\ndif_queue_inflight{queue=%s} %d\ndif_queue_parked{queue=%s} %d\ndif_queue_blocked_producers{queue=%s} %d\ndif_queue_redeliveries_total{queue=%s} %d\n", label, q.Waiting, label, q.InFlight, label, q.Parked, label, q.Blocked, label, q.Redeliveries)
		}
		for _, t := range channels.Topics {
			fmt.Fprintf(w, "dif_topic_waiting{topic=%s} %d\ndif_topic_blocked_producers{topic=%s} %d\n", strconv.Quote(t.Name), t.Waiting, strconv.Quote(t.Name), t.Blocked)
		}
		metrics := []struct {
			name, kind string
			value      func(engine.FlowStatus) string
		}{
			{"dif_messages_completed_total", "counter", func(f engine.FlowStatus) string { return strconv.FormatInt(f.Completed, 10) }},
			{"dif_messages_failed_total", "counter", func(f engine.FlowStatus) string { return strconv.FormatInt(f.Failed, 10) }},
			{"dif_processing_seconds_total", "counter", func(f engine.FlowStatus) string {
				return strconv.FormatFloat(float64(f.DurationNanos)/1e9, 'g', -1, 64)
			}},
			{"dif_flow_inflight", "gauge", func(f engine.FlowStatus) string { return strconv.FormatInt(f.InFlight, 10) }},
		}
		for _, metric := range metrics {
			fmt.Fprintf(w, "# TYPE %s %s\n", metric.name, metric.kind)
			for _, f := range flows {
				label := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(f.ID) + `"`
				fmt.Fprintf(w, "%s{flow=%s} %s\n", metric.name, label, metric.value(f))
			}
		}
	})
	return mux
}

func (s *Service) channelStatus() api.ChannelStatus {
	s.mu.RLock()
	r := s.runtime
	s.mu.RUnlock()
	if r == nil {
		return api.ChannelStatus{}
	}
	return r.ChannelStatus()
}
