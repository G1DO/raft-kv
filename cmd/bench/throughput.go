package main

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"time"
)

// runThroughput measures steady-state write throughput and client-observed write
// latency: N concurrent clients PUT to the leader for a fixed window. Because the
// server blocks each write until it commits and applies, the round-trip time is a
// proxy for commit latency (replication + WAL fsync + apply).
func runThroughput(args []string) int {
	fs := flag.NewFlagSet("throughput", flag.ExitOnError)
	duration := fs.Duration("duration", 10*time.Second, "measurement window")
	concurrency := fs.Int("concurrency", 8, "number of concurrent client connections")
	timeout := fs.Duration("timeout", 10*time.Second, "max wait for the initial leader")
	dataDir := fs.String("data", "data/bench-throughput", "data directory root")
	src := fs.String("src", ".", "repo root to build ./cmd/server from")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: bench throughput [flags]")
		fmt.Fprintln(os.Stderr, "\nMeasure steady-state write throughput and p99 commit latency.")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	c, err := setup(*dataDir, *src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "setup: %v\n", err)
		return 1
	}
	defer c.KillAll()

	leaderAddr, err := c.WaitLeader(*timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initial leader: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "running throughput: %d clients for %v...\n", *concurrency, *duration)
	deadline := time.Now().Add(*duration)

	// Each worker writes only to its own slice index, read after Wait — no shared
	// mutable state, so no locking needed.
	latsByWorker := make([][]float64, *concurrency)
	errsByWorker := make([]int, *concurrency)
	var wg sync.WaitGroup

	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			cl, err := Dial(leaderAddr)
			if err != nil {
				errsByWorker[w]++
				return
			}
			defer cl.Close()

			var lats []float64
			for n := 0; time.Now().Before(deadline); n++ {
				t0 := time.Now()
				resp, err := cl.Do(fmt.Sprintf("PUT w%d-k%d v%d", w, n, n))
				if err != nil || resp != "OK" {
					errsByWorker[w]++
					continue
				}
				lats = append(lats, float64(time.Since(t0).Microseconds())/1000.0)
			}
			latsByWorker[w] = lats
		}(w)
	}
	wg.Wait()

	var all []float64
	totalErr := 0
	for w := 0; w < *concurrency; w++ {
		all = append(all, latsByWorker[w]...)
		totalErr += errsByWorker[w]
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "no successful writes")
		return 1
	}

	s := summarize(all)
	opsPerSec := float64(len(all)) / duration.Seconds()
	fmt.Printf("Steady-state write throughput — PUT through Raft commit+apply\n")
	fmt.Printf("concurrency:   %d clients\n", *concurrency)
	fmt.Printf("window:        %v\n", *duration)
	fmt.Printf("writes ok:     %d  (errors: %d)\n", len(all), totalErr)
	fmt.Printf("throughput:    %.0f writes/sec\n", opsPerSec)
	fmt.Printf("latency p50:   %.2f ms\n", s.P50)
	fmt.Printf("latency p99:   %.2f ms\n", s.P99)
	fmt.Printf("latency min/mean/max: %.2f / %.2f / %.2f ms\n", s.Min, s.Mean, s.Max)
	fmt.Printf("\nnote: latency is client-observed round-trip = replication + WAL fsync + apply.\n")
	return 0
}
