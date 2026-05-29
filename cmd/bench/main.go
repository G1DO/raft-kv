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
		runMTTR(rest)
	case "throughput":
		runThroughput(rest)
	case "partition":
		runPartition(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", sub)
		flag.Usage()
		os.Exit(1)
	}
}

func runMTTR(args []string) {
	fs := flag.NewFlagSet("mttr", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: bench mttr [flags]")
		fmt.Fprintln(os.Stderr, "\nMeasure leader-election MTTR: kill leader, time to new stable leader.")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	// future flags: --trials, --binary, --data-dir, --timeout
	fs.Parse(args)
	fmt.Fprintln(os.Stderr, "mttr: not yet implemented")
	os.Exit(1)
}

func runThroughput(args []string) {
	fs := flag.NewFlagSet("throughput", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: bench throughput [flags]")
		fmt.Fprintln(os.Stderr, "\nMeasure steady-state write throughput and p99 commit latency.")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	// future flags: --duration, --concurrency, --binary, --data-dir
	fs.Parse(args)
	fmt.Fprintln(os.Stderr, "throughput: not yet implemented")
	os.Exit(1)
}

func runPartition(args []string) {
	fs := flag.NewFlagSet("partition", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: bench partition [flags]")
		fmt.Fprintln(os.Stderr, "\nMeasure recovery under network partition (quorum loss / ReadIndex rejection).")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	// future flags: --binary, --data-dir, --timeout
	fs.Parse(args)
	fmt.Fprintln(os.Stderr, "partition: not yet implemented")
	os.Exit(1)
}
