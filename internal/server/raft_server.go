package server

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/G1DO/raft-kv/internal/kv"
	"github.com/G1DO/raft-kv/internal/raft"
)

// commitWait bounds how long a client write/read blocks for its entry to commit
// and apply. Replication is driven by the 50ms heartbeat, so this is generous.
const commitWait = 3 * time.Second

// RaftConfig describes one node of a cluster.
type RaftConfig struct {
	ID         string            // this node's id (e.g. "node1")
	ClientAddr string            // KV text-protocol listen address
	RaftAddr   string            // peer-RPC listen address
	Peers      []string          // OTHER nodes' raft addresses
	DataDir    string            // per-node directory for log/state/snapshot
	LeaderHint map[string]string // node id -> client address, for NOT_LEADER redirects
}

// RaftServer is the cluster-mode front-end: client commands go through Raft
// consensus instead of straight to the local store.
type RaftServer struct {
	cfg      RaftConfig
	raft     *raft.Raft
	store    *kv.KVStore
	listener net.Listener

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
	r := raft.NewRaft(cfg.ID, cfg.Peers, applyCh, logPath, statePath, snapshotPath)

	s := &RaftServer{
		cfg:     cfg,
		raft:    r,
		store:   store,
		waiters: make(map[int]chan string),
	}

	go s.applyLoop(applyCh)

	if err := r.StartRPCServer(cfg.RaftAddr); err != nil {
		return nil, fmt.Errorf("failed to start RPC server: %w", err)
	}
	r.Start()

	return s, nil
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
	ln, err := net.Listen("tcp", s.cfg.ClientAddr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	fmt.Printf("[%s] client API on %s, raft RPC on %s\n", s.cfg.ID, ln.Addr(), s.cfg.RaftAddr)

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
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
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
		conn.Write([]byte(s.processCommand(command) + "\n"))
	}
}

func (s *RaftServer) processCommand(command string) string {
	verb := command
	if i := strings.IndexByte(command, ' '); i >= 0 {
		verb = command[:i]
	}

	switch verb {
	case "GET":
		return s.read(command)
	case "PUT", "DELETE":
		return s.write(command)
	case "ADD_SERVER", "REMOVE_SERVER":
		return s.membership(verb, command)
	default:
		return "ERROR: unknown command"
	}
}

// write replicates a mutating command and blocks until it is committed+applied.
func (s *RaftServer) write(command string) string {
	idx, _, isLeader := s.raft.AppendCommand([]byte(command))
	if !isLeader {
		return s.notLeader()
	}

	ch := make(chan string, 1)
	s.mu.Lock()
	s.waiters[idx] = ch
	s.mu.Unlock()

	select {
	case res := <-ch:
		return res
	case <-time.After(commitWait):
		s.mu.Lock()
		delete(s.waiters, idx)
		s.mu.Unlock()
		return "ERROR: timeout waiting for commit"
	}
}

// read serves a linearizable GET via the ReadIndex protocol.
func (s *RaftServer) read(command string) string {
	readIdx, isLeader, _ := s.raft.ReadIndex()
	if !isLeader {
		return s.notLeader()
	}
	// Wait until our store has applied through readIdx. When ReadIndex returns a
	// non-nil error with isLeader=true it means "not caught up yet" — readIdx is
	// still valid, so we wait on it here.
	deadline := time.Now().Add(commitWait)
	for {
		s.mu.Lock()
		applied := s.applied
		s.mu.Unlock()
		if applied >= readIdx {
			break
		}
		if time.Now().After(deadline) {
			return "ERROR: timeout waiting for read index"
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.store.Apply([]byte(command))
}

// membership routes ADD_SERVER/REMOVE_SERVER to the Raft membership API.
// Format: ADD_SERVER <id> <raftAddr>
func (s *RaftServer) membership(verb, command string) string {
	parts := strings.Fields(command)
	if len(parts) != 3 {
		return "ERROR: " + verb + " requires <id> <raftAddr>"
	}
	id, addr := parts[1], parts[2]

	var isLeader bool
	if verb == "ADD_SERVER" {
		_, _, isLeader = s.raft.AddServer(id, addr)
	} else {
		_, _, isLeader = s.raft.RemoveServer(id, addr)
	}
	if !isLeader {
		return s.notLeader()
	}
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
