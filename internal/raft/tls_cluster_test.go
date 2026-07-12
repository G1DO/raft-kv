//go:build !race

package raft

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMTLS_ThreeNodeCluster verifies Phase A #5: election, replication, follower
// rejoin, leader failover, and a negative plaintext join — all under peer mTLS.
//
// Built only without -race: connection-per-RPC mTLS handshakes under the race
// detector routinely exceed the 150–300ms election timeout and thrash the
// cluster. The race-safe smoke coverage lives in tls_cluster_race_test.go.
func TestMTLS_ThreeNodeCluster(t *testing.T) {
	dir := t.TempDir()
	caDER, caKey := mustGenCA(t)
	caPath := filepath.Join(dir, "ca.crt")
	mustWritePEM(t, caPath, "CERTIFICATE", caDER)

	type nodeFiles struct {
		cert, key string
		data      string
	}
	files := map[string]nodeFiles{}
	for _, id := range []string{"node1", "node2", "node3"} {
		cert, key := mustGenLeaf(t, dir, id, caDER, caKey)
		data := filepath.Join(dir, id)
		if err := os.MkdirAll(data, 0755); err != nil {
			t.Fatal(err)
		}
		files[id] = nodeFiles{cert: cert, key: key, data: data}
	}

	startNode := func(id string) *Raft {
		f := files[id]
		r := NewRaft(id, nil, nil,
			filepath.Join(f.data, "log"),
			filepath.Join(f.data, "state"),
			filepath.Join(f.data, "snap"),
			nil, nil)
		if err := r.SetTLSConfig(&TLSConfig{CertFile: f.cert, KeyFile: f.key, CAFile: caPath}); err != nil {
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

	wirePeers := func(nodes ...*Raft) {
		idToAddr := make(map[string]string, len(nodes))
		for _, n := range nodes {
			idToAddr[n.id] = n.rpcAddr
		}
		for _, n := range nodes {
			peers := make([]string, 0, len(nodes)-1)
			for _, o := range nodes {
				if o.id != n.id {
					peers = append(peers, o.rpcAddr)
				}
			}
			n.mu.Lock()
			n.peers = peers
			n.mu.Unlock()
			n.SetPeerIdentities(idToAddr)
		}
	}
	wirePeers(n1, n2, n3)

	for _, n := range []*Raft{n1, n2, n3} {
		n.Start()
	}

	const wait = 8 * time.Second

	leader := waitStableLeader(t, []*Raft{n1, n2, n3}, wait, 300*time.Millisecond)
	t.Logf("elected leader %s term %d", leader.id, leaderTerm(leader))
	waitCommitAtLeast(t, leader, 1, wait)

	leader = requireLeader(t, leader, []*Raft{n1, n2, n3}, wait)
	idx, _, ok := leader.AppendCommand([]byte("PUT k v1"))
	if !ok {
		t.Fatal("AppendCommand failed on leader")
	}
	waitCommitAtLeast(t, leader, idx, wait)
	waitAppliedAtLeast(t, []*Raft{n1, n2, n3}, idx, wait)

	var follower *Raft
	for _, n := range []*Raft{n1, n2, n3} {
		if n.id != leader.id {
			follower = n
			break
		}
	}
	followerID := follower.id
	follower.Stop()

	alive := []*Raft{}
	for _, n := range []*Raft{n1, n2, n3} {
		if n.id != followerID {
			alive = append(alive, n)
		}
	}
	wirePeers(alive...)

	leader = requireLeader(t, leader, alive, wait)
	idx2, _, ok := leader.AppendCommand([]byte("PUT k v2"))
	if !ok {
		t.Fatal("AppendCommand while follower down failed")
	}
	waitCommitAtLeast(t, leader, idx2, wait)

	restarted := startNode(followerID)
	defer restarted.Stop()
	alive = append(alive, restarted)
	wirePeers(alive...)
	restarted.Start()

	waitAppliedAtLeast(t, []*Raft{restarted}, idx2, wait)

	oldLeader := requireLeader(t, leader, alive, wait)
	oldID := oldLeader.id
	oldLeader.Stop()

	remaining := []*Raft{}
	for _, n := range alive {
		if n.id != oldID {
			remaining = append(remaining, n)
		}
	}
	wirePeers(remaining...)

	newLeader := waitStableLeader(t, remaining, wait, 300*time.Millisecond)
	if newLeader.id == oldID {
		t.Fatalf("expected a different leader after kill, still %s", oldID)
	}
	idx3, _, ok := newLeader.AppendCommand([]byte("PUT k v3"))
	if !ok {
		t.Fatal("AppendCommand after leader kill failed")
	}
	waitCommitAtLeast(t, newLeader, idx3, wait)
	waitAppliedAtLeast(t, remaining, idx3, wait)

	attackerDir := filepath.Join(dir, "attacker")
	if err := os.MkdirAll(attackerDir, 0755); err != nil {
		t.Fatal(err)
	}
	plain := NewRaft("attacker", nil, nil,
		filepath.Join(attackerDir, "log"),
		filepath.Join(attackerDir, "state"),
		filepath.Join(attackerDir, "snap"),
		nil, nil)
	defer plain.Stop()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	plainAddr := ln.Addr().String()
	_ = ln.Close()
	if err := plain.StartRPCServer(plainAddr); err != nil {
		t.Fatal(err)
	}
	plain.mu.Lock()
	plain.peers = []string{remaining[0].rpcAddr}
	plain.mu.Unlock()
	plain.SetPeerIdentities(map[string]string{remaining[0].id: remaining[0].rpcAddr})
	plain.Start()
	time.Sleep(800 * time.Millisecond)
	if plain.IsLeader() {
		t.Fatal("plaintext node must not become leader against mTLS cluster")
	}
}

func waitStableLeader(t *testing.T, nodes []*Raft, timeout, stableFor time.Duration) *Raft {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var current string
	stableSince := time.Time{}
	for time.Now().Before(deadline) {
		var leaders []*Raft
		for _, n := range nodes {
			if n.IsLeader() {
				leaders = append(leaders, n)
			}
		}
		if len(leaders) == 1 {
			if leaders[0].id != current {
				current = leaders[0].id
				stableSince = time.Now()
			} else if time.Since(stableSince) >= stableFor {
				return leaders[0]
			}
		} else {
			current = ""
			stableSince = time.Time{}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no stable single leader among %d nodes within %s", len(nodes), timeout)
	return nil
}

func requireLeader(t *testing.T, prefer *Raft, nodes []*Raft, timeout time.Duration) *Raft {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if prefer != nil && prefer.IsLeader() {
			return prefer
		}
		for _, n := range nodes {
			if n.IsLeader() {
				return n
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("no leader available")
	return nil
}

func leaderTerm(r *Raft) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentTerm
}

func waitCommitAtLeast(t *testing.T, r *Raft, index int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		ci := r.commitIndex
		r.mu.Unlock()
		if ci >= index {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	ci := r.commitIndex
	r.mu.Unlock()
	t.Fatalf("commitIndex never reached %d (got %d) on %s", index, ci, r.id)
}

func waitAppliedAtLeast(t *testing.T, nodes []*Raft, index int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := true
		for _, n := range nodes {
			n.mu.Lock()
			ai := n.lastApplied
			n.mu.Unlock()
			if ai < index {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("lastApplied never reached %d on all nodes", index)
}
