package server

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/G1DO/raft-kv/internal/kv"
	"github.com/G1DO/raft-kv/internal/log"
)

type Server struct {
	log    *log.Log
	store  *kv.KVStore
	addr   string
	logger *slog.Logger
}

func NewServer(addr string, logPath string, logger *slog.Logger) (*Server, error) {
	// 1. Create the log
	l, err := log.NewLog(logPath)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	// 2. Create the KV store
	store := kv.NewKVStore()

	// 3. Replay log to rebuild KV store (if recovering from crash)
	entries, err := l.Replay()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		store.Apply(entry)
	}

	// 4. Return the server
	return &Server{
		log:    l,
		store:  store,
		addr:   addr,
		logger: logger,
	}, nil
}

func (s *Server) Start() error {
	// 1. Listen on TCP
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	defer listener.Close()

	// Update addr with actual address (useful when using port 0)
	s.addr = listener.Addr().String()
	s.logger.Info("server_started", "mode", "single-node", "client_addr", s.addr)

	// 2. Loop: accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.logger.Warn("accept_failed", "error", err)
			continue
		}

		// 3. Handle each connection in a goroutine (allows multiple clients)
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)

	for {
		// Read one line (command ends with \n)
		line, err := reader.ReadString('\n')
		if err != nil {
			return // client disconnected
		}

		// Remove trailing newline
		command := strings.TrimSpace(line)
		if command == "" {
			continue
		}

		verb := opOf(command)
		result := s.processCommand(command)
		if isSecurityRelevantVerb(verb) {
			emitClientAudit(s.logger, clientAudit{
				Event:   auditEventForVerb(verb),
				Outcome: outcomeFromClientResult(result),
				Node:    "single-node",
				Remote:  conn.RemoteAddr().String(),
				Action:  verb,
			})
		}

		conn.Write([]byte(result + "\n"))
	}
}

func (s *Server) processCommand(command string) string {
	// For GET, no need to log - just read from store
	if strings.HasPrefix(command, "GET ") {
		return s.store.Apply([]byte(command))
	}

	// For PUT and DELETE, write to log first (durability)
	_, err := s.log.Append([]byte(command))
	if err != nil {
		return "ERROR: " + err.Error()
	}

	// Then apply to store
	return s.store.Apply([]byte(command))
}
