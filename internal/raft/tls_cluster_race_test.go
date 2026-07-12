//go:build race

package raft

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMTLS_ThreeNodeForcedLeader is the race-detector smoke for Phase A #5.
// Full election / rejoin / failover lives in tls_cluster_test.go (!race): under
// -race, per-RPC mTLS handshakes routinely outrun the 150–300ms election window.
func TestMTLS_ThreeNodeForcedLeader(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := filepath.Join(dir, "ca.crt")
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)

	startNode := func(id string) *Raft {
		data := filepath.Join(dir, id)
		if err := os.MkdirAll(data, 0755); err != nil {
			t.Fatal(err)
		}
		cert, key := mustGenLeaf(t, dir, id, caDER, caKey)
		r := NewRaft(id, nil, nil,
			filepath.Join(data, "log"),
			filepath.Join(data, "state"),
			filepath.Join(data, "snap"),
			nil, nil)
		if err := r.SetTLSConfig(&TLSConfig{CertFile: cert, KeyFile: key, CAFile: caPath}); err != nil {
			t.Fatalf("%s TLS: %v", id, err)
		}
		if err := r.StartRPCServer("127.0.0.1:0"); err != nil {
			t.Fatalf("%s listen: %v", id, err)
		}
		return r
	}

	n1 := startNode("node1")
	n2 := startNode("node2")
	n3 := startNode("node3")
	defer n1.Stop()
	defer n2.Stop()
	defer n3.Stop()

	idToAddr := map[string]string{
		n1.id: n1.rpcAddr,
		n2.id: n2.rpcAddr,
		n3.id: n3.rpcAddr,
	}
	for _, n := range []*Raft{n1, n2, n3} {
		peers := make([]string, 0, 2)
		for _, o := range []*Raft{n1, n2, n3} {
			if o.id != n.id {
				peers = append(peers, o.rpcAddr)
			}
		}
		n.mu.Lock()
		n.peers = peers
		n.mu.Unlock()
		n.SetPeerIdentities(idToAddr)
	}

	// No Start(): avoid election-timer storms. Force leadership + heartbeat loop.
	n1.mu.Lock()
	n1.becomeLeader()
	n1.mu.Unlock()

	const wait = 5 * time.Second
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		n1.mu.Lock()
		ci := n1.commitIndex
		n1.mu.Unlock()
		if ci >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	idx, _, ok := n1.AppendCommand([]byte("PUT k race"))
	if !ok {
		t.Fatal("AppendCommand failed on forced leader")
	}
	deadline = time.Now().Add(wait)
	for time.Now().Before(deadline) {
		okAll := true
		for _, n := range []*Raft{n1, n2, n3} {
			n.mu.Lock()
			ai := n.lastApplied
			n.mu.Unlock()
			if ai < idx {
				okAll = false
				break
			}
		}
		if okAll {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("mTLS replication did not apply index %d on all nodes under -race", idx)
}
