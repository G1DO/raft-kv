package main

import (
	"flag"
	"fmt"
	"os"
)

const usageText = `bench — raft-kv measurement harness

Usage:
  bench <subcommand> [flags]

Subcommands:
  mttr        measure election MTTR (kill leader, time to new stable leader)
  throughput  measure steady-state write throughput and p99 commit latency
  partition   measure recovery under network partition (quorum loss)

Run 'bench <subcommand> -help' for subcommand flags.
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	sub, rest := flag.Arg(0), flag.Args()[1:]
	switch sub {
	case "mttr":
		os.Exit(runMTTR(rest))
	case "throughput":
		os.Exit(runThroughput(rest))
	case "partition":
		os.Exit(runPartition(rest))
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", sub)
		flag.Usage()
		os.Exit(1)
	}
}
