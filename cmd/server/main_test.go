package main

import "testing"

// TestParsePeers_ExcludesSelf is the behaviour that lets a StatefulSet hand every
// pod one identical --peers spanning the whole cluster: the entry matching the
// node's own id is dropped from both the peer-address list and the hint map.
func TestParsePeers_ExcludesSelf(t *testing.T) {
	all := "n0@n0:9090@n0:8080,n1@n1:9090@n1:8080,n2@n2:9090@n2:8080"
	addrs, raftByID, hints, err := parsePeers(all, "n1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"n0:9090", "n2:9090"} // self (n1) removed, order preserved
	if len(addrs) != len(want) {
		t.Fatalf("addrs = %v, want %v", addrs, want)
	}
	for i := range want {
		if addrs[i] != want[i] {
			t.Fatalf("addrs[%d] = %q, want %q", i, addrs[i], want[i])
		}
	}

	if _, ok := hints["n1"]; ok {
		t.Errorf("hints must not contain self n1: %v", hints)
	}
	if hints["n0"] != "n0:8080" || hints["n2"] != "n2:8080" {
		t.Errorf("hints = %v, want n0->n0:8080, n2->n2:8080", hints)
	}
	if raftByID["n0"] != "n0:9090" || raftByID["n2"] != "n2:9090" {
		t.Errorf("raftByID = %v, want n0/n2 raft addrs", raftByID)
	}
	if _, ok := raftByID["n1"]; ok {
		t.Errorf("raftByID must not contain self: %v", raftByID)
	}
}

// TestParsePeers_LegacyOtherNodesOnly is the hand-run form (cluster-up.sh): the
// caller already passes only the OTHER nodes, so nothing is filtered.
func TestParsePeers_LegacyOtherNodesOnly(t *testing.T) {
	addrs, _, _, err := parsePeers("n2@n2:9090@n2:8080,n3@n3:9090@n3:8080", "n1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("addrs = %v, want 2 entries", addrs)
	}
}

// TestParsePeers_SoleMember: a one-node cluster listing only itself yields an
// empty peer set (quorum of 1).
func TestParsePeers_SoleMember(t *testing.T) {
	addrs, raftByID, _, err := parsePeers("n1@n1:9090@n1:8080", "n1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 0 || len(raftByID) != 0 {
		t.Fatalf("addrs = %v raftByID = %v, want empty", addrs, raftByID)
	}
}

func TestParsePeers_Empty(t *testing.T) {
	addrs, raftByID, hints, err := parsePeers("", "n1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 0 || len(hints) != 0 || len(raftByID) != 0 {
		t.Fatalf("expected empty, got addrs=%v hints=%v raftByID=%v", addrs, hints, raftByID)
	}
}

func TestParsePeers_Malformed(t *testing.T) {
	if _, _, _, err := parsePeers("not-a-valid-entry", "n1"); err == nil {
		t.Fatal("expected error for malformed peer spec")
	}
}
