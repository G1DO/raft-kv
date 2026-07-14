package raft

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer guards concurrent slog writes vs test reads (race detector).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b.Bytes()...)
}

func TestEmitPeerTLSAudit_Schema(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	r := NewRaft("node-a", nil, nil, t.TempDir()+"/log", t.TempDir()+"/state", t.TempDir()+"/snap", logger, nil)
	defer r.Stop()

	r.emitPeerTLSAudit("127.0.0.1:9090", "identity", "deny")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("audit line not JSON: %v", err)
	}
	if line["msg"] != "security_audit" {
		t.Fatalf("msg=%v", line["msg"])
	}
	if line["audit"] != true {
		t.Fatalf("audit=%v", line["audit"])
	}
	if line["event"] != auditEventPeerTLSFail {
		t.Fatalf("event=%v", line["event"])
	}
	if line["outcome"] != "deny" {
		t.Fatalf("outcome=%v", line["outcome"])
	}
	if line["action"] != "identity" {
		t.Fatalf("action=%v", line["action"])
	}
	if _, ok := line["actor"]; ok {
		t.Fatal("peer TLS audit must not include actor")
	}
	if strings.Contains(buf.String(), "BEGIN CERTIFICATE") {
		t.Fatal("must not log cert material")
	}
}

func TestMTLS_HandshakeFailureEmitsAudit(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := dir + "/ca.crt"
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)
	cert, key := mustGenLeaf(t, dir, "node-a", caDER, caKey)

	var buf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	data := t.TempDir()
	srv := NewRaft("node-a", nil, nil, data+"/log", data+"/state", data+"/snap", logger, nil)
	defer srv.Stop()
	if err := srv.SetTLSConfig(&TLSConfig{CertFile: cert, KeyFile: key, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := srv.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialTimeout("tcp", srv.rpcAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = conn.Write([]byte(`{"Type":"RequestVote","Data":{}}` + "\n"))
	time.Sleep(100 * time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, `"event":"`+auditEventPeerTLSFail+`"`) {
		t.Fatalf("expected peer TLS audit, got: %s", out)
	}
	if !strings.Contains(out, `"action":"handshake"`) {
		t.Fatalf("expected handshake action, got: %s", out)
	}
}

func TestMTLS_IdentityRejectionEmitsAudit(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := dir + "/ca.crt"
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)
	aCert, aKey := mustGenLeaf(t, dir, "node-a", caDER, caKey)
	bCert, bKey := mustGenLeaf(t, dir, "node-b", caDER, caKey)

	var buf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	dirA := t.TempDir()
	dirB := t.TempDir()
	nodeA := NewRaft("node-a", nil, nil, dirA+"/log", dirA+"/state", dirA+"/snap", logger, nil)
	nodeB := NewRaft("node-b", nil, nil, dirB+"/log", dirB+"/state", dirB+"/snap", nil, nil)
	defer nodeA.Stop()
	defer nodeB.Stop()

	if err := nodeA.SetTLSConfig(&TLSConfig{CertFile: aCert, KeyFile: aKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.SetTLSConfig(&TLSConfig{CertFile: bCert, KeyFile: bKey, CAFile: caPath}); err != nil {
		t.Fatal(err)
	}
	nodeA.SetPeerIdentities(map[string]string{"node-b": "127.0.0.1:0"})

	if err := nodeA.StartRPCServer("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}

	args := &RequestVoteArgs{Term: 1, CandidateID: "node-a", LastLogIndex: 0, LastLogTerm: 0}
	var reply RequestVoteReply
	_ = nodeB.callRPC(nodeA.rpcAddr, "RequestVote", args, &reply)

	out := buf.String()
	if !strings.Contains(out, `"event":"`+auditEventPeerTLSFail+`"`) {
		t.Fatalf("expected peer TLS audit, got: %s", out)
	}
	if !strings.Contains(out, `"action":"identity"`) {
		t.Fatalf("expected identity action, got: %s", out)
	}
}
