package raft

import (
	"testing"
	"time"
)

func TestNewRaft(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})

	if r.id != "node1" {
		t.Errorf("expected id node1, got %s", r.id)
	}
	if r.state != Follower {
		t.Errorf("expected Follower state, got %d", r.state)
	}
	if r.currentTerm != 0 {
		t.Errorf("expected term 0, got %d", r.currentTerm)
	}
	if r.votedFor != "" {
		t.Errorf("expected empty votedFor, got %s", r.votedFor)
	}
	if len(r.peers) != 2 {
		t.Errorf("expected 2 peers, got %d", len(r.peers))
	}
}

func TestStartElection(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})

	// Manually call startElection
	r.startElection()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Should be candidate
	if r.state != Candidate {
		t.Errorf("expected Candidate, got %d", r.state)
	}
	// Term should increment
	if r.currentTerm != 1 {
		t.Errorf("expected term 1, got %d", r.currentTerm)
	}
	// Should vote for itself
	if r.votedFor != "node1" {
		t.Errorf("expected votedFor=node1, got %s", r.votedFor)
	}
	// Should have 1 vote (itself)
	if r.votesReceived != 1 {
		t.Errorf("expected 1 vote, got %d", r.votesReceived)
	}
}

func TestRequestVote_GrantsVote(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.currentTerm = 1

	args := &RequestVoteArgs{
		Term:        2, // higher term
		CandidateID: "node2",
	}
	reply := &RequestVoteReply{}

	r.RequestVote(args, reply)

	if !reply.VoteGranted {
		t.Error("expected vote to be granted")
	}
	if r.votedFor != "node2" {
		t.Errorf("expected votedFor=node2, got %s", r.votedFor)
	}
	if r.currentTerm != 2 {
		t.Errorf("expected term updated to 2, got %d", r.currentTerm)
	}
}

func TestRequestVote_RejectsLowerTerm(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.currentTerm = 5

	args := &RequestVoteArgs{
		Term:        3, // lower term
		CandidateID: "node2",
	}
	reply := &RequestVoteReply{}

	r.RequestVote(args, reply)

	if reply.VoteGranted {
		t.Error("expected vote to be rejected (lower term)")
	}
	if r.currentTerm != 5 {
		t.Errorf("term should not change, got %d", r.currentTerm)
	}
}

func TestRequestVote_RejectsIfAlreadyVoted(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.currentTerm = 2
	r.votedFor = "node3" // already voted for node3

	args := &RequestVoteArgs{
		Term:        2, // same term
		CandidateID: "node2",
	}
	reply := &RequestVoteReply{}

	r.RequestVote(args, reply)

	if reply.VoteGranted {
		t.Error("expected vote to be rejected (already voted for someone else)")
	}
	if r.votedFor != "node3" {
		t.Errorf("votedFor should not change, got %s", r.votedFor)
	}
}

func TestRequestVote_GrantsIfAlreadyVotedForSame(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.currentTerm = 2
	r.votedFor = "node2" // already voted for node2

	args := &RequestVoteArgs{
		Term:        2, // same term
		CandidateID: "node2", // same candidate
	}
	reply := &RequestVoteReply{}

	r.RequestVote(args, reply)

	if !reply.VoteGranted {
		t.Error("expected vote to be granted (already voted for same candidate)")
	}
}

func TestHandleVoteResponse_CountsVotes(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.state = Candidate
	r.currentTerm = 1
	r.votesReceived = 1 // voted for self

	reply := &RequestVoteReply{
		Term:        1,
		VoteGranted: true,
	}

	r.handleVoteResponse(reply)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.votesReceived != 2 {
		t.Errorf("expected 2 votes, got %d", r.votesReceived)
	}
}

func TestHandleVoteResponse_BecomesLeaderWithMajority(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"}) // 3 nodes total
	r.state = Candidate
	r.currentTerm = 1
	r.votesReceived = 1 // voted for self

	reply := &RequestVoteReply{
		Term:        1,
		VoteGranted: true,
	}

	r.handleVoteResponse(reply) // now has 2 votes = majority of 3

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Leader {
		t.Errorf("expected Leader state, got %d", r.state)
	}
}

func TestHandleVoteResponse_StepsDownOnHigherTerm(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.state = Candidate
	r.currentTerm = 1

	reply := &RequestVoteReply{
		Term:        5, // higher term!
		VoteGranted: false,
	}

	r.handleVoteResponse(reply)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Follower {
		t.Errorf("expected Follower state, got %d", r.state)
	}
	if r.currentTerm != 5 {
		t.Errorf("expected term 5, got %d", r.currentTerm)
	}
}

func TestElectionTimeout(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})

	// Start election timer
	r.resetElectionTimer()

	// Wait for timeout (max 300ms + buffer)
	time.Sleep(400 * time.Millisecond)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Should have started election (may have restarted, so term could be > 1)
	if r.state != Candidate {
		t.Errorf("expected Candidate after timeout, got %d", r.state)
	}
	if r.currentTerm < 1 {
		t.Errorf("expected term >= 1, got %d", r.currentTerm)
	}
}

func TestAppendEntries_AcceptsValidHeartbeat(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.currentTerm = 1

	args := &AppendEntriesArgs{
		Term:     1,
		LeaderID: "node2",
	}
	reply := &AppendEntriesReply{}

	r.AppendEntries(args, reply)

	if !reply.Success {
		t.Error("expected heartbeat to be accepted")
	}
	if r.state != Follower {
		t.Errorf("expected Follower state, got %d", r.state)
	}
}

func TestAppendEntries_RejectsOldTerm(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.currentTerm = 5

	args := &AppendEntriesArgs{
		Term:     3, // old term
		LeaderID: "node2",
	}
	reply := &AppendEntriesReply{}

	r.AppendEntries(args, reply)

	if reply.Success {
		t.Error("expected heartbeat to be rejected (old term)")
	}
	if reply.Term != 5 {
		t.Errorf("expected reply term 5, got %d", reply.Term)
	}
}

func TestAppendEntries_UpdatesTermAndStepsDown(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.currentTerm = 1
	r.state = Candidate // was running for election

	args := &AppendEntriesArgs{
		Term:     3, // higher term
		LeaderID: "node2",
	}
	reply := &AppendEntriesReply{}

	r.AppendEntries(args, reply)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.currentTerm != 3 {
		t.Errorf("expected term 3, got %d", r.currentTerm)
	}
	if r.state != Follower {
		t.Errorf("expected Follower state, got %d", r.state)
	}
	if !reply.Success {
		t.Error("expected heartbeat to be accepted")
	}
}

func TestBecomeLeader_StartsHeartbeats(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})
	r.state = Candidate
	r.currentTerm = 1

	r.becomeLeader()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Leader {
		t.Errorf("expected Leader state, got %d", r.state)
	}
	if r.heartbeatTimer == nil {
		t.Error("expected heartbeatTimer to be set")
	}
}

func TestRPC_RequestVoteOverNetwork(t *testing.T) {
	// Create two nodes
	node1 := NewRaft("node1", []string{})
	node2 := NewRaft("node2", []string{})

	// Start RPC servers
	err := node1.StartRPCServer("localhost:0") // port 0 = random available port
	if err != nil {
		t.Fatalf("node1 failed to start: %v", err)
	}
	defer node1.listener.Close()

	err = node2.StartRPCServer("localhost:0")
	if err != nil {
		t.Fatalf("node2 failed to start: %v", err)
	}
	defer node2.listener.Close()

	// node1 sends RequestVote to node2
	args := &RequestVoteArgs{
		Term:        1,
		CandidateID: "node1",
	}
	var reply RequestVoteReply

	ok := node1.callRPC(node2.rpcAddr, "RequestVote", args, &reply)

	if !ok {
		t.Fatal("RPC call failed")
	}
	if !reply.VoteGranted {
		t.Error("expected vote to be granted")
	}
}

func TestRPC_AppendEntriesOverNetwork(t *testing.T) {
	// Create two nodes
	leader := NewRaft("leader", []string{})
	follower := NewRaft("follower", []string{})
	follower.currentTerm = 1

	// Start RPC servers
	leader.StartRPCServer("localhost:0")
	defer leader.listener.Close()

	follower.StartRPCServer("localhost:0")
	defer follower.listener.Close()

	// Start follower's election timer (so AppendEntries can reset it)
	follower.resetElectionTimer()

	// Leader sends heartbeat to follower
	args := &AppendEntriesArgs{
		Term:     1,
		LeaderID: "leader",
	}
	var reply AppendEntriesReply

	ok := leader.callRPC(follower.rpcAddr, "AppendEntries", args, &reply)

	if !ok {
		t.Fatal("RPC call failed")
	}
	if !reply.Success {
		t.Error("expected heartbeat to succeed")
	}
}

func TestCluster_ElectsLeader(t *testing.T) {
	// Create 3 nodes - we'll set peers after getting their addresses
	node1 := NewRaft("node1", nil)
	node2 := NewRaft("node2", nil)
	node3 := NewRaft("node3", nil)

	// Start RPC servers
	node1.StartRPCServer("localhost:0")
	node2.StartRPCServer("localhost:0")
	node3.StartRPCServer("localhost:0")

	defer node1.listener.Close()
	defer node2.listener.Close()
	defer node3.listener.Close()

	// Now set peers with actual addresses
	node1.peers = []string{node2.rpcAddr, node3.rpcAddr}
	node2.peers = []string{node1.rpcAddr, node3.rpcAddr}
	node3.peers = []string{node1.rpcAddr, node2.rpcAddr}

	// Start election timers on all nodes
	node1.resetElectionTimer()
	node2.resetElectionTimer()
	node3.resetElectionTimer()

	// Wait for election to happen (max 500ms for timeout + some buffer)
	time.Sleep(800 * time.Millisecond)

	// Count leaders
	leaderCount := 0
	nodes := []*Raft{node1, node2, node3}

	for _, n := range nodes {
		n.mu.Lock()
		if n.state == Leader {
			leaderCount++
			t.Logf("%s is the leader (term %d)", n.id, n.currentTerm)
		}
		n.mu.Unlock()
	}

	if leaderCount != 1 {
		t.Errorf("expected exactly 1 leader, got %d", leaderCount)
	}
}

// Test: When becoming leader, nextIndex and matchIndex are initialized
func TestBecomeLeader_InitializesLogIndices(t *testing.T) {
	r := NewRaft("node1", []string{"node2", "node3"})

	// Add some entries to the log (simulate previous terms)
	r.log = []LogEntry{
		{Term: 1, Command: []byte("cmd1")},
		{Term: 1, Command: []byte("cmd2")},
	}

	// Become leader
	r.mu.Lock()
	r.state = Candidate
	r.currentTerm = 2
	r.becomeLeader()
	r.mu.Unlock()

	// Check nextIndex: should be len(log) + 1 = 3 for each peer
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, peer := range r.peers {
		if r.nextIndex[peer] != 3 {
			t.Errorf("nextIndex[%s] = %d, want 3", peer, r.nextIndex[peer])
		}
		if r.matchIndex[peer] != 0 {
			t.Errorf("matchIndex[%s] = %d, want 0", peer, r.matchIndex[peer])
		}
	}
}

// Test: Leader sends correct PrevLogIndex, PrevLogTerm, Entries in AppendEntries
func TestSendHeartbeat_IncludesLogEntries(t *testing.T) {
	// Create leader with 2 log entries
	leader := NewRaft("leader", []string{})
	leader.state = Leader
	leader.currentTerm = 2
	leader.log = []LogEntry{
		{Term: 1, Command: []byte("cmd1")},
		{Term: 2, Command: []byte("cmd2")},
	}

	// Create follower (empty log)
	follower := NewRaft("follower", []string{})
	follower.currentTerm = 2

	// Start RPC servers
	leader.StartRPCServer("localhost:0")
	defer leader.listener.Close()
	follower.StartRPCServer("localhost:0")
	defer follower.listener.Close()

	// Initialize leader's tracking for follower
	// nextIndex = 1 means "send from entry 1" (follower has nothing)
	leader.nextIndex = map[string]int{follower.rpcAddr: 1}
	leader.matchIndex = map[string]int{follower.rpcAddr: 0}

	// Leader sends heartbeat (which should include entries)
	leader.peers = []string{follower.rpcAddr}
	follower.resetElectionTimer() // so AppendEntries can reset it
	leader.sendHeartbeat(follower.rpcAddr)

	// Give it a moment
	time.Sleep(50 * time.Millisecond)

	// Check: follower should have the entries now
	follower.mu.Lock()
	defer follower.mu.Unlock()

	if len(follower.log) != 2 {
		t.Errorf("follower log length = %d, want 2", len(follower.log))
	}
}