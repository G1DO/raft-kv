package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/G1DO/raft-kv/internal/metrics"
)

func TestRaftServer_EmitsMutateAudit(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dataDir := t.TempDir()
	clientAddr := "127.0.0.1:19110"
	raftAddr := "127.0.0.1:19111"
	metricsAddr := "127.0.0.1:19112"

	srv, err := NewRaftServer(RaftConfig{
		ID:          "node1",
		ClientAddr:  clientAddr,
		RaftAddr:    raftAddr,
		MetricsAddr: metricsAddr,
		Peers:       nil,
		DataDir:     dataDir,
		Logger:      logger,
		Metrics:     metrics.NewRegistry(),
	})
	if err != nil {
		t.Fatalf("NewRaftServer: %v", err)
	}
	defer srv.Shutdown()

	go func() {
		if err := srv.Start(); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("tcp", clientAddr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	resp, err := sendCommand(conn, "PUT auditkey secretvalue")
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	if !strings.HasPrefix(resp, "NOT_LEADER") {
		t.Fatalf("expected NOT_LEADER on solo follower, got %q", resp)
	}

	out := buf.String()
	if !strings.Contains(out, `"event":"`+auditEventClientMutate+`"`) {
		t.Fatalf("missing mutate audit: %s", out)
	}
	if !strings.Contains(out, `"outcome":"deny"`) {
		t.Fatalf("missing deny outcome: %s", out)
	}
	if strings.Contains(out, "secretvalue") || strings.Contains(out, "auditkey") {
		t.Fatalf("audit or ops log leaked key/value: %s", out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var auditLine map[string]any
	for _, line := range lines {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec["audit"] == true {
			auditLine = rec
			break
		}
	}
	if auditLine == nil {
		t.Fatal("no audit=true line found")
	}
	if auditLine["actor"] != actorUnauthenticated {
		t.Fatalf("actor=%v", auditLine["actor"])
	}
	if auditLine["action"] != "PUT" {
		t.Fatalf("action=%v", auditLine["action"])
	}
}

func TestServer_EmitsMutateAudit(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	logPath := filepath.Join(t.TempDir(), "audit.log")
	addr := "127.0.0.1:19120"
	defer os.Remove(logPath)

	srv, err := NewServer(addr, logPath, logger)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	go srv.Start()
	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	resp, err := sendCommand(conn, "PUT foo bar")
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	if resp != "OK" {
		t.Fatalf("got %q", resp)
	}

	out := buf.String()
	if !strings.Contains(out, auditEventClientMutate) {
		t.Fatalf("missing audit event: %s", out)
	}
	if strings.Contains(out, "bar") {
		t.Fatalf("leaked value: %s", out)
	}
}

func TestRaftServer_EmitsMembershipAudit(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dataDir := t.TempDir()
	clientAddr := fmt.Sprintf("127.0.0.1:%d", 19200+os.Getpid()%1000)

	srv, err := NewRaftServer(RaftConfig{
		ID:         "node1",
		ClientAddr: clientAddr,
		RaftAddr:   "127.0.0.1:0",
		DataDir:    dataDir,
		Logger:     logger,
		Metrics:    metrics.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()

	go func() { _ = srv.Start() }()
	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("tcp", clientAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	resp, err := sendCommand(conn, "ADD_SERVER node2 127.0.0.1:9099")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp, "NOT_LEADER") {
		t.Fatalf("expected NOT_LEADER, got %q", resp)
	}

	if !strings.Contains(buf.String(), auditEventClientMembership) {
		t.Fatalf("missing membership audit: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"outcome":"deny"`) {
		t.Fatalf("missing deny outcome: %s", buf.String())
	}
}
