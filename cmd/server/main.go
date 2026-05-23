package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/G1DO/raft-kv/internal/server"
)

func main() {
	id := flag.String("id", "", "node id; setting this enables cluster (Raft) mode")
	addr := flag.String("addr", "localhost:8080", "client KV listen address")
	raftAddr := flag.String("raft-addr", "", "peer-RPC listen address (cluster mode)")
	peers := flag.String("peers", "", "other nodes as id@raftAddr@clientAddr, comma-separated")
	data := flag.String("data", "data", "data directory")
	flag.Parse()

	if *id == "" {
		runSingleNode(*addr, *data)
		return
	}
	runCluster(*id, *addr, *raftAddr, *peers, *data)
}

// runSingleNode is the original flag-less behaviour: a plain single-node KV
// store over a WAL, no Raft. Kept so `go run ./cmd/server` still works.
func runSingleNode(addr, dataDir string) {
	os.MkdirAll(dataDir, 0755)
	logPath := filepath.Join(dataDir, "raft.log")

	fmt.Println("Starting raft-kv server (single-node, no Raft)...")
	srv, err := server.NewServer(addr, logPath)
	if err != nil {
		fmt.Printf("Failed to create server: %v\n", err)
		os.Exit(1)
	}
	if err := srv.Start(); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

// runCluster wires this node into a Raft cluster and serves clients until a
// SIGINT/SIGTERM triggers a graceful step-down.
func runCluster(id, addr, raftAddr, peers, dataDir string) {
	if raftAddr == "" {
		fmt.Println("cluster mode requires --raft-addr")
		os.Exit(1)
	}

	peerAddrs, hints, err := parsePeers(peers)
	if err != nil {
		fmt.Printf("invalid --peers: %v\n", err)
		os.Exit(1)
	}
	hints[id] = addr // so a follower can hand a client our own address

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Printf("failed to create data dir: %v\n", err)
		os.Exit(1)
	}

	srv, err := server.NewRaftServer(server.RaftConfig{
		ID:         id,
		ClientAddr: addr,
		RaftAddr:   raftAddr,
		Peers:      peerAddrs,
		DataDir:    dataDir,
		LeaderHint: hints,
	})
	if err != nil {
		fmt.Printf("failed to create server: %v\n", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Printf("\n[%s] shutting down...\n", id)
		srv.Shutdown()
	}()

	if err := srv.Start(); err != nil {
		fmt.Printf("server error: %v\n", err)
		os.Exit(1)
	}
}

// parsePeers turns "id@raftAddr@clientAddr,..." into the Raft peer-address list
// (raft addresses) and an id->clientAddr hint map for client redirection.
func parsePeers(peers string) ([]string, map[string]string, error) {
	hints := make(map[string]string)
	var addrs []string
	if strings.TrimSpace(peers) == "" {
		return addrs, hints, nil
	}
	for _, p := range strings.Split(peers, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts := strings.Split(p, "@")
		if len(parts) != 3 {
			return nil, nil, fmt.Errorf("peer %q must be id@raftAddr@clientAddr", p)
		}
		peerID, peerRaft, peerClient := parts[0], parts[1], parts[2]
		addrs = append(addrs, peerRaft)
		hints[peerID] = peerClient
	}
	return addrs, hints, nil
}
