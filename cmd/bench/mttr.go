package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

// runMTTR measures leader-election MTTR: repeatedly kill the current leader and
// time how long until a new leader accepts a write, restarting the killed node
// between trials so every trial starts from full quorum.
func runMTTR(args []string) int {
	fs := flag.NewFlagSet("mttr", flag.ExitOnError)
	trials := fs.Int("trials", 100, "number of kill-leader trials")
	timeout := fs.Duration("timeout", 10*time.Second, "max wait for a new leader per trial")
	settle := fs.Duration("settle", 1*time.Second, "grace after a restarted node rejoins")
	dataDir := fs.String("data", "data/bench-mttr", "data directory root")
	src := fs.String("src", ".", "repo root to build ./cmd/server from")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: bench mttr [flags]")
		fmt.Fprintln(os.Stderr, "\nMeasure leader-election MTTR: kill leader, time to new stable leader.")
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

	fmt.Fprintf(os.Stderr, "measuring election MTTR over %d trials...\n", *trials)
	var samples []float64 // milliseconds
	failures := 0

	for i := 0; i < *trials; i++ {
		leaderID, ok := c.idForClientAddr(leaderAddr)
		if !ok {
			fmt.Fprintf(os.Stderr, "trial %d: cannot map leader addr %s\n", i, leaderAddr)
			failures++
			break
		}

		start := time.Now()
		if err := c.Kill(leaderID); err != nil {
			fmt.Fprintf(os.Stderr, "trial %d: kill %s: %v\n", i, leaderID, err)
			failures++
			break
		}

		newAddr, err := c.WaitLeader(*timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "trial %d: no new leader within %v\n", i, *timeout)
			failures++
			c.Start(leaderID) // restore quorum and try to continue
			c.WaitNodeUp(leaderAddr, *timeout)
			if a, e := c.WaitLeader(*timeout); e == nil {
				leaderAddr = a
			}
			continue
		}
		samples = append(samples, float64(time.Since(start).Microseconds())/1000.0)

		// Restart the killed node so the next trial begins at full quorum.
		if err := c.Start(leaderID); err != nil {
			fmt.Fprintf(os.Stderr, "trial %d: restart %s: %v\n", i, leaderID, err)
			failures++
			break
		}
		if err := c.WaitNodeUp(leaderAddr, *timeout); err != nil {
			fmt.Fprintf(os.Stderr, "trial %d: %s did not rejoin: %v\n", i, leaderID, err)
		}
		time.Sleep(*settle)
		leaderAddr = newAddr

		if (i+1)%10 == 0 {
			fmt.Fprintf(os.Stderr, "  %d/%d trials\n", i+1, *trials)
		}
	}

	if len(samples) == 0 {
		fmt.Fprintln(os.Stderr, "no successful trials")
		return 1
	}

	s := summarize(samples)
	fmt.Printf("Election MTTR — kill leader (SIGKILL) to first write accepted by a new leader\n")
	fmt.Printf("trials:        %d ok, %d failed\n", s.N, failures)
	fmt.Printf("p50:           %.1f ms\n", s.P50)
	fmt.Printf("p99:           %.1f ms\n", s.P99)
	fmt.Printf("min/mean/max:  %.1f / %.1f / %.1f ms\n", s.Min, s.Mean, s.Max)
	fmt.Printf("\nhistogram:\n%s", histogram(samples, 10, "ms"))
	return 0
}
