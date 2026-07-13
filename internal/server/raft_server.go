package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/G1DO/raft-kv/internal/kv"
	"github.com/G1DO/raft-kv/internal/metrics"
	"github.com/G1DO/raft-kv/internal/raft"
)

// commitWait bounds how long a client write/read blocks for its entry to commit
// and apply. Replication is driven by the 50ms heartbeat, so this is generous.
const commitWait = 3 * time.Second

// RaftConfig describes one node of a cluster.
type RaftConfig struct {
	ID          string            // this node's id (e.g. "node1")
	ClientAddr  string            // KV text-protocol listen address
	RaftAddr    string            // peer-RPC listen address
	MetricsAddr string            // HTTP metrics/health listen address
	Peers       []string          // OTHER nodes' raft addresses
	PeerIDs     map[string]string // OTHER nodes' id -> raft address (mTLS SAN binding)
	DataDir     string            // per-node directory for log/state/snapshot
	LeaderHint  map[string]string // node id -> client address, for NOT_LEADER redirects
	Logger      *slog.Logger
	Metrics     *metrics.Registry
	// TLS holds peer-RPC certificate paths (ADR-009). Nil/empty => plaintext
	// (tests). When set, all of CertFile/KeyFile/CAFile must exist (ADR-010).
	TLS *raft.TLSConfig
	// Tracer emits request/consensus-stage spans. nil => no-op tracing; spans
	// stay out of internal/raft either way (ADR-007: the consensus core is
	// zero-dep, instrumentation lives at this layer).
	Tracer trace.Tracer
}

// RaftServer is the cluster-mode front-end: client commands go through Raft
// consensus instead of straight to the local store.
type RaftServer struct {
	cfg      RaftConfig
	raft     *raft.Raft
	store    *kv.KVStore
	listener net.Listener
	httpSrv  *http.Server
	log      *slog.Logger
	metric   *serverMetrics
	tracer   trace.Tracer

	mu      sync.Mutex
	waiters map[int]chan string // commandIndex -> result channel (buffered, cap 1)
	applied int                 // highest CommandIndex applied to the store
}

// NewRaftServer builds a node, starts its RPC server, and arms its election
// timer. The client listener is started separately by Start.
func NewRaftServer(cfg RaftConfig) (*RaftServer, error) {
	logPath := filepath.Join(cfg.DataDir, "raft.log")
	statePath := filepath.Join(cfg.DataDir, "raft.state")
	snapshotPath := filepath.Join(cfg.DataDir, "raft.snapshot")

	applyCh := make(chan raft.ApplyMsg, 256)
	store := kv.NewKVStore()
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = metrics.NewRegistry()
	}
	if cfg.Tracer == nil {
		cfg.Tracer = noop.NewTracerProvider().Tracer("raft-kv")
	}
	r := raft.NewRaft(cfg.ID, cfg.Peers, applyCh, logPath, statePath, snapshotPath, cfg.Logger, cfg.Metrics)
	if err := r.SetTLSConfig(cfg.TLS); err != nil {
		return nil, fmt.Errorf("raft TLS config: %w", err)
	}
	r.SetPeerIdentities(cfg.PeerIDs)

	s := &RaftServer{
		cfg:     cfg,
		raft:    r,
		store:   store,
		log:     cfg.Logger,
		metric:  newServerMetrics(cfg.Metrics),
		tracer:  cfg.Tracer,
		waiters: make(map[int]chan string),
	}

	go s.applyLoop(applyCh)

	if err := r.StartRPCServer(cfg.RaftAddr); err != nil {
		return nil, fmt.Errorf("failed to start RPC server: %w", err)
	}
	r.Start()

	return s, nil
}

type serverMetrics struct {
	requests        *metrics.Counter
	requestDuration *metrics.Histogram
}

func newServerMetrics(registry *metrics.Registry) *serverMetrics {
	if registry == nil {
		return nil
	}
	return &serverMetrics{
		requests:        registry.NewCounter("raftkv_requests_total", "Client API requests by operation and result."),
		requestDuration: registry.NewHistogram("raftkv_request_duration_seconds", "Client API request duration by operation.", []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}),
	}
}

// applyLoop consumes committed entries and applies them to the store, waking any
// client blocked on the matching index.
func (s *RaftServer) applyLoop(applyCh <-chan raft.ApplyMsg) {
	for msg := range applyCh {
		if msg.IsSnapshot {
			s.store.Restore(msg.Command)
		} else {
			result := s.store.Apply(msg.Command)
			s.mu.Lock()
			if ch, ok := s.waiters[msg.CommandIndex]; ok {
				ch <- result // buffered cap 1, never blocks
				delete(s.waiters, msg.CommandIndex)
			}
			s.mu.Unlock()
		}
		s.mu.Lock()
		if msg.CommandIndex > s.applied {
			s.applied = msg.CommandIndex
		}
		s.mu.Unlock()
	}
}

// Start listens for clients and serves them until the listener is closed.
func (s *RaftServer) Start() error {
	if err := s.startHTTPServer(); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", s.cfg.ClientAddr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	s.log.Info("server_started", "node", s.cfg.ID, "client_addr", ln.Addr().String(), "raft_addr", s.cfg.RaftAddr, "metrics_addr", s.cfg.MetricsAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil // listener closed (shutdown)
		}
		go s.handleConnection(conn)
	}
}

// Shutdown steps the node down and stops accepting clients.
func (s *RaftServer) Shutdown() {
	s.raft.Stop()
	s.mu.Lock()
	ln := s.listener
	httpSrv := s.httpSrv
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	if httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			s.log.Warn("http_shutdown_failed", "node", s.cfg.ID, "error", err)
		}
	}
}

func (s *RaftServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		if command == "" {
			continue
		}
		ctx, span := s.tracer.Start(context.Background(), "raftkv.request",
			trace.WithAttributes(attribute.String("raftkv.op", opOf(command))))
		// With tracing enabled the OTel trace ID doubles as the log trace_id, so
		// Loki lines and Tempo traces share one key; without it, a random ID
		// still correlates the log lines of one request.
		traceID := newTraceID()
		if sc := span.SpanContext(); sc.HasTraceID() {
			traceID = sc.TraceID().String()
		}
		reqLog := s.log.With("trace_id", traceID, "node", s.cfg.ID, "remote_addr", conn.RemoteAddr().String())
		ctx = context.WithValue(ctx, traceIDKey{}, traceID)
		ctx = context.WithValue(ctx, loggerKey{}, reqLog)
		verb := opOf(command)
		reqLog.Info("request_started", "op", verb)
		result := s.processCommand(ctx, command)
		class := resultClass(result)
		if isSecurityRelevantVerb(verb) {
			emitClientAudit(s.log, clientAudit{
				Event:     auditEventForVerb(verb),
				Outcome:   outcomeFromClientResult(result),
				Node:      s.cfg.ID,
				Term:      s.raft.CurrentTerm(),
				Remote:    conn.RemoteAddr().String(),
				Action:    verb,
				RequestID: requestIDFromCommand(command),
				TraceID:   traceID,
			})
		}
		span.SetAttributes(attribute.String("raftkv.result", class))
		if class == "error" || class == "timeout" {
			span.SetStatus(codes.Error, result)
		}
		span.End()
		reqLog.Info("request_finished", "op", verb, "result", class)
		conn.Write([]byte(result + "\n"))
	}
}

func (s *RaftServer) processCommand(ctx context.Context, command string) string {
	start := time.Now()
	verb := opOf(command)
	result := "error"
	defer func() {
		if s.metric != nil {
			op := verb
			if !knownOp(op) {
				op = "UNKNOWN"
			}
			s.metric.requests.Inc(metrics.Label{Name: "op", Value: op}, metrics.Label{Name: "result", Value: result})
			s.metric.requestDuration.Observe(time.Since(start).Seconds(), metrics.Label{Name: "op", Value: op})
		}
	}()

	switch verb {
	case "GET":
		res := s.read(ctx, command)
		result = resultClass(res)
		return res
	case "PUT", "DELETE":
		res := s.write(ctx, command)
		result = resultClass(res)
		return res
	case "ADD_SERVER", "REMOVE_SERVER":
		res := s.membership(ctx, verb, command)
		result = resultClass(res)
		return res
	default:
		result = "error"
		return "ERROR: unknown command"
	}
}

// write replicates a mutating command and blocks until it is committed+applied.
func (s *RaftServer) write(ctx context.Context, command string) string {
	_, appendSpan := s.tracer.Start(ctx, "raft.append")
	idx, _, isLeader := s.raft.AppendCommand([]byte(command))
	appendSpan.SetAttributes(attribute.Int("raft.index", idx))
	appendSpan.End()
	if !isLeader {
		loggerFrom(ctx).Info("request_not_leader", "op", opOf(command), "leader", s.raft.LeaderID())
		return s.notLeader()
	}
	loggerFrom(ctx).Info("request_appended", "op", opOf(command), "index", idx)

	ch := make(chan string, 1)
	s.mu.Lock()
	s.waiters[idx] = ch
	s.mu.Unlock()

	// One span for replicate→commit→apply: it opens when the entry is handed to
	// Raft and closes when applyLoop wakes us, so its width is exactly the
	// raft_commit_latency_seconds cost (fan-out + quorum + fsync + apply).
	_, commitSpan := s.tracer.Start(ctx, "raft.replicate_commit_apply",
		trace.WithAttributes(attribute.Int("raft.index", idx)))
	select {
	case res := <-ch:
		commitSpan.End()
		loggerFrom(ctx).Info("request_committed", "op", opOf(command), "index", idx)
		return res
	case <-time.After(commitWait):
		s.mu.Lock()
		delete(s.waiters, idx)
		s.mu.Unlock()
		commitSpan.SetStatus(codes.Error, "timeout waiting for commit")
		commitSpan.End()
		loggerFrom(ctx).Warn("request_timeout", "op", opOf(command), "index", idx)
		return "ERROR: timeout waiting for commit"
	}
}

// read serves a linearizable GET via the ReadIndex protocol.
func (s *RaftServer) read(ctx context.Context, command string) string {
	// ReadIndex round-trips a heartbeat to confirm leadership before reading.
	_, riSpan := s.tracer.Start(ctx, "raft.read_index")
	readIdx, isLeader, _ := s.raft.ReadIndex()
	riSpan.SetAttributes(attribute.Int("raft.read_index", readIdx))
	riSpan.End()
	if !isLeader {
		loggerFrom(ctx).Info("request_not_leader", "op", "GET", "leader", s.raft.LeaderID())
		return s.notLeader()
	}
	// Wait until our store has applied through readIdx. When ReadIndex returns a
	// non-nil error with isLeader=true it means "not caught up yet" — readIdx is
	// still valid, so we wait on it here.
	_, applySpan := s.tracer.Start(ctx, "raft.apply_wait",
		trace.WithAttributes(attribute.Int("raft.read_index", readIdx)))
	deadline := time.Now().Add(commitWait)
	for {
		s.mu.Lock()
		applied := s.applied
		s.mu.Unlock()
		if applied >= readIdx {
			break
		}
		if time.Now().After(deadline) {
			applySpan.SetStatus(codes.Error, "timeout waiting for read index")
			applySpan.End()
			loggerFrom(ctx).Warn("request_timeout", "op", "GET", "read_index", readIdx)
			return "ERROR: timeout waiting for read index"
		}
		time.Sleep(5 * time.Millisecond)
	}
	applySpan.End()
	loggerFrom(ctx).Info("read_index_confirmed", "read_index", readIdx)
	return s.store.Apply([]byte(command))
}

// membership routes ADD_SERVER/REMOVE_SERVER to the Raft membership API.
// Format: ADD_SERVER <id> <raftAddr>
func (s *RaftServer) membership(ctx context.Context, verb, command string) string {
	parts := strings.Fields(command)
	if len(parts) != 3 {
		return "ERROR: " + verb + " requires <id> <raftAddr>"
	}
	id, addr := parts[1], parts[2]

	_, memberSpan := s.tracer.Start(ctx, "raft.membership_append",
		trace.WithAttributes(attribute.String("raft.server_id", id)))
	var isLeader bool
	if verb == "ADD_SERVER" {
		_, _, isLeader = s.raft.AddServer(id, addr)
	} else {
		_, _, isLeader = s.raft.RemoveServer(id, addr)
	}
	memberSpan.End()
	if !isLeader {
		loggerFrom(ctx).Info("request_not_leader", "op", verb, "leader", s.raft.LeaderID())
		return s.notLeader()
	}
	loggerFrom(ctx).Info("membership_request_logged", "op", verb, "server_id", id, "server_addr", addr)
	return "OK"
}

// notLeader builds a NOT_LEADER reply, appending the leader's client address
// when known so the client can retry against it.
func (s *RaftServer) notLeader() string {
	leader := s.raft.LeaderID()
	if leader != "" {
		if addr, ok := s.cfg.LeaderHint[leader]; ok {
			return "NOT_LEADER " + addr
		}
	}
	return "NOT_LEADER"
}

func (s *RaftServer) startHTTPServer() error {
	if strings.TrimSpace(s.cfg.MetricsAddr) == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if _, err := s.cfg.Metrics.WriteTo(w); err != nil {
			s.log.Warn("metrics_write_failed", "node", s.cfg.ID, "error", err)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.raft.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready\n"))
	})

	httpSrv := &http.Server{
		Addr:              s.cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	s.mu.Lock()
	s.httpSrv = httpSrv
	s.mu.Unlock()
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error("http_server_failed", "node", s.cfg.ID, "addr", s.cfg.MetricsAddr, "error", err)
		}
	}()
	return nil
}

type traceIDKey struct{}
type loggerKey struct{}

func newTraceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func loggerFrom(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

func opOf(command string) string {
	if i := strings.IndexByte(command, ' '); i >= 0 {
		return command[:i]
	}
	return command
}

func knownOp(op string) bool {
	switch op {
	case "GET", "PUT", "DELETE", "ADD_SERVER", "REMOVE_SERVER":
		return true
	default:
		return false
	}
}

func resultClass(result string) string {
	switch {
	case result == "OK" || result == "NOT_FOUND" || (!strings.HasPrefix(result, "ERROR:") && !strings.HasPrefix(result, "NOT_LEADER")):
		return "ok"
	case strings.HasPrefix(result, "NOT_LEADER"):
		return "not_leader"
	case strings.Contains(result, "timeout"):
		return "timeout"
	default:
		return "error"
	}
}
