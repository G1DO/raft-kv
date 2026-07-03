package metrics

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestRegistry_WriteTo(t *testing.T) {
	reg := NewRegistry()
	reg.NewGauge("raft_term", "Current Raft term.").Set(2)
	reg.NewCounter("raftkv_requests_total", "Client API requests.").Add(3, Label{Name: "result", Value: "ok"}, Label{Name: "op", Value: "PUT"})
	reg.NewHistogram("raft_commit_latency_seconds", "Append to commit latency.", []float64{0.1, 0.5}).Observe(0.2)

	var buf bytes.Buffer
	if _, err := reg.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	want := strings.Join([]string{
		"# HELP raft_term Current Raft term.",
		"# TYPE raft_term gauge",
		"raft_term 2",
		"# HELP raftkv_requests_total Client API requests.",
		"# TYPE raftkv_requests_total counter",
		`raftkv_requests_total{op="PUT",result="ok"} 3`,
		"# HELP raft_commit_latency_seconds Append to commit latency.",
		"# TYPE raft_commit_latency_seconds histogram",
		`raft_commit_latency_seconds_bucket{le="0.1"} 0`,
		`raft_commit_latency_seconds_bucket{le="0.5"} 1`,
		`raft_commit_latency_seconds_bucket{le="+Inf"} 1`,
		"raft_commit_latency_seconds_sum 0.2",
		"raft_commit_latency_seconds_count 1",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Fatalf("unexpected exposition:\n%s\nwant:\n%s", got, want)
	}
}

func TestRegistry_ConcurrentUpdates(t *testing.T) {
	reg := NewRegistry()
	counter := reg.NewCounter("requests_total", "Requests.")
	gauge := reg.NewGauge("inflight", "Inflight.")
	histogram := reg.NewHistogram("duration_seconds", "Duration.", []float64{0.1, 1})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter.Inc(Label{Name: "result", Value: "ok"})
			gauge.Set(1)
			histogram.Observe(0.05)
		}()
	}
	wg.Wait()

	var buf bytes.Buffer
	if _, err := reg.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`requests_total{result="ok"} 100`,
		"duration_seconds_count 100",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output:\n%s", want, out)
		}
	}
}
