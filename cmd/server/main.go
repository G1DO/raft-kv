package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"go.opentelemetry.io/otel/trace"

	"github.com/G1DO/raft-kv/internal/server"
)

func main() {
	id := flag.String("id", "", "node id; setting this enables cluster (Raft) mode")
	addr := flag.String("addr", "localhost:8080", "client KV listen address")
	raftAddr := flag.String("raft-addr", "", "peer-RPC listen address (cluster mode)")
	metricsAddr := flag.String("metrics-addr", ":2112", "HTTP metrics/health listen address (cluster mode)")
	otlpEndpoint := flag.String("otlp-endpoint", "", "OTLP/HTTP trace endpoint as host:port (cluster mode); empty disables tracing")
	peers := flag.String("peers", "", "other nodes as id@raftAddr@clientAddr, comma-separated")
	data := flag.String("data", "data", "data directory")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *id == "" {
		runSingleNode(*addr, *data, logger)
		return
	}
	runCluster(*id, *addr, *raftAddr, *metricsAddr, *otlpEndpoint, *peers, *data, logger)
}

// runSingleNode is the original flag-less behaviour: a plain single-node KV
// store over a WAL, no Raft. Kept so `go run ./cmd/server` still works.
func runSingleNode(addr, dataDir string, logger *slog.Logger) {
	os.MkdirAll(dataDir, 0755)
	logPath := filepath.Join(dataDir, "raft.log")

	logger.Info("starting_server", "mode", "single-node")
	srv, err := server.NewServer(addr, logPath, logger)
	if err != nil {
		logger.Error("server_create_failed", "error", err)
		os.Exit(1)
	}
	if err := srv.Start(); err != nil {
		logger.Error("server_failed", "error", err)
		os.Exit(1)
	}
}

// runCluster wires this node into a Raft cluster and serves clients until a
// SIGINT/SIGTERM triggers a graceful step-down.
func runCluster(id, addr, raftAddr, metricsAddr, otlpEndpoint, peers, dataDir string, logger *slog.Logger) {
	if raftAddr == "" {
		logger.Error("missing_required_flag", "flag", "raft-addr")
		os.Exit(1)
	}

	peerAddrs, hints, err := parsePeers(peers, id)
	if err != nil {
		logger.Error("invalid_peers", "error", err)
		os.Exit(1)
	}
	hints[id] = addr // so a follower can hand a client our own address

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Error("data_dir_create_failed", "dir", dataDir, "error", err)
		os.Exit(1)
	}

	var tracer trace.Tracer
	if otlpEndpoint != "" {
		t, stopTracing, err := setupTracing(otlpEndpoint, id, logger)
		if err != nil {
			logger.Error("tracing_setup_failed", "endpoint", otlpEndpoint, "error", err)
			os.Exit(1)
		}
		defer stopTracing() // flush buffered spans on graceful shutdown
		tracer = t
		logger.Info("tracing_enabled", "node", id, "otlp_endpoint", otlpEndpoint)
	}

	srv, err := server.NewRaftServer(server.RaftConfig{
		ID:          id,
		ClientAddr:  addr,
		RaftAddr:    raftAddr,
		MetricsAddr: metricsAddr,
		Peers:       peerAddrs,
		DataDir:     dataDir,
		LeaderHint:  hints,
		Logger:      logger,
		Tracer:      tracer,
	})
	if err != nil {
		logger.Error("server_create_failed", "node", id, "error", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting_down", "node", id)
		srv.Shutdown()
	}()

	if err := srv.Start(); err != nil {
		logger.Error("server_failed", "node", id, "error", err)
		os.Exit(1)
	}
}

// parsePeers turns "id@raftAddr@clientAddr,..." into the Raft peer-address list
// (raft addresses) and an id->clientAddr hint map for client redirection. The
// entry matching selfID (if present) is dropped: a node must never list itself
// among its peers, because Raft sizes the cluster as len(peers)+1. Tolerating
// self in the list lets every node be handed one identical --peers spanning the
// whole cluster — which is what a Kubernetes StatefulSet (shared pod template)
// needs, since it cannot give each pod a different, self-excluding list.
func parsePeers(peers, selfID string) ([]string, map[string]string, error) {
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
		if peerID == selfID {
			continue // never list self; quorum is sized as len(peers)+1
		}
		addrs = append(addrs, peerRaft)
		hints[peerID] = peerClient
	}
	return addrs, hints, nil
}
