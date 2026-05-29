package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// runPartition drives the leader into a minority (by killing both followers in a
// 3-node cluster) and verifies that a linearizable read is refused while the
// leader cannot reach a quorum — then restores the followers and times recovery.
//
// This is minority-isolation-via-crash, not a literal bidirectional netsplit, but
// it exercises the exact property: ReadIndex's confirmLeadership cannot get a
// majority of heartbeat ACKs, so the leader declines to serve the read.
func runPartition(args []string) int {
	fs := flag.NewFlagSet("partition", flag.ExitOnError)
	timeout := fs.Duration("timeout", 10*time.Second, "max wait for leader / recovery")
	settle := fs.Duration("settle", 1*time.Second, "grace for followers to rejoin")
	dataDir := fs.String("data", "data/bench-partition", "data directory root")
	src := fs.String("src", ".", "repo root to build ./cmd/server from")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: bench partition [flags]")
		fmt.Fprintln(os.Stderr, "\nMeasure recovery under quorum loss (ReadIndex rejection + recovery).")
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
	leaderID, _ := c.idForClientAddr(leaderAddr)

	// Seed a committed value through the healthy cluster.
	if resp, err := do(leaderAddr, "PUT foo bar"); err != nil || resp != "OK" {
		fmt.Fprintf(os.Stderr, "seed write failed: resp=%q err=%v\n", resp, err)
		return 1
	}

	pass := true
	check := func(name string, ok bool, detail string) {
		status := "PASS"
		if !ok {
			status = "FAIL"
			pass = false
		}
		fmt.Printf("[%s] %s — %s\n", status, name, detail)
	}

	// Drive the leader into a minority: kill both followers.
	var killed []string
	for _, n := range defaultNodes {
		if n.ID == leaderID {
			continue
		}
		if err := c.Kill(n.ID); err == nil {
			killed = append(killed, n.ID)
		}
	}
	fmt.Fprintf(os.Stderr, "killed followers %v; leader %s now in a minority\n", killed, leaderID)
	time.Sleep(300 * time.Millisecond) // let in-flight heartbeat leases lapse

	// A leader that cannot reach a majority must refuse a linearizable read.
	resp, _ := do(leaderAddr, "GET foo")
	check("ReadIndex refuses read under quorum loss",
		strings.HasPrefix(resp, "NOT_LEADER"),
		fmt.Sprintf("GET foo -> %q", resp))

	// Recover: restart the followers; time until the cluster serves writes again.
	// WaitLeader's probe is itself a write, so its return means a leader has
	// committed an entry in its CURRENT term — which advances the commit index to
	// cover the seeded entry, so the subsequent linearizable read observes it.
	// (A bare GET here would be racy: if recovery triggered a re-election, the new
	// leader's ReadIndex can't see a prior-term committed key until it commits in
	// its own term — this implementation appends no no-op on elect. See benchmarks.md.)
	start := time.Now()
	for _, id := range killed {
		c.Start(id)
	}
	newLeaderAddr, werr := c.WaitLeader(*timeout)
	recovery := time.Since(start).Round(time.Millisecond)
	if werr != nil {
		check("cluster recovers after quorum restored", false,
			fmt.Sprintf("no leader within %v", *timeout))
	} else {
		check("cluster recovers after quorum restored", true,
			fmt.Sprintf("leader serves writes again after %v", recovery))
		recovered, _ := do(newLeaderAddr, "GET foo")
		check("committed value survives quorum loss", strings.Contains(recovered, "bar"),
			fmt.Sprintf("GET foo -> %q", recovered))
	}

	time.Sleep(*settle)
	fmt.Printf("\nrecovery time (followers restarted -> leader serves writes again): %v\n", recovery)
	fmt.Printf("note: a PUT to the minority leader instead blocks until the 3s commit timeout.\n")

	if !pass {
		return 1
	}
	return 0
}
