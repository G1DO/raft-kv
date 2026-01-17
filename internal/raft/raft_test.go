package raft

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Helper to create a Raft node with temp persistence files
func newTestRaft(t *testing.T, id string, peers []string, applyCh chan ApplyMsg) *Raft {
	// Create temp directory for this test
	tempDir, err := os.MkdirTemp("", "raft-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	logPath := fmt.Sprintf("%s/%s.log", tempDir, id)
	statePath := fmt.Sprintf("%s/%s.state", tempDir, id)

	r := NewRaft(id, peers, applyCh, logPath, statePath)

	// Register cleanup
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	return r
}

func TestNewRaft(t *testing.T) {
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)

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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)

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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil) // 3 nodes total
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)

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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)
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
	node1 := newTestRaft(t, "node1", []string{}, nil)
	node2 := newTestRaft(t, "node2", []string{}, nil)

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
	leader := newTestRaft(t, "leader", []string{}, nil)
	follower := newTestRaft(t, "follower", []string{}, nil)
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
	node1 := newTestRaft(t, "node1", nil, nil)
	node2 := newTestRaft(t, "node2", nil, nil)
	node3 := newTestRaft(t, "node3", nil, nil)

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
	r := newTestRaft(t, "node1", []string{"node2", "node3"}, nil)

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
	leader := newTestRaft(t, "leader", []string{}, nil)
	leader.state = Leader
	leader.currentTerm = 2
	leader.log = []LogEntry{
		{Term: 1, Command: []byte("cmd1")},
		{Term: 2, Command: []byte("cmd2")},
	}

	// Create follower (empty log)
	follower := newTestRaft(t, "follower", []string{}, nil)
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

// Test: Leader updates nextIndex and matchIndex after successful replication
func TestLeader_UpdatesIndicesOnSuccess(t *testing.T) {
	// Create leader with 2 log entries
	leader := newTestRaft(t, "leader", []string{}, nil)
	leader.state = Leader
	leader.currentTerm = 2
	leader.log = []LogEntry{
		{Term: 1, Command: []byte("cmd1")},
		{Term: 2, Command: []byte("cmd2")},
	}

	// Create follower
	follower := newTestRaft(t, "follower", []string{}, nil)
	follower.currentTerm = 2

	// Start RPC servers
	leader.StartRPCServer("localhost:0")
	defer leader.listener.Close()
	follower.StartRPCServer("localhost:0")
	defer follower.listener.Close()

	// Initialize leader's tracking - follower has nothing
	leader.nextIndex = map[string]int{follower.rpcAddr: 1}
	leader.matchIndex = map[string]int{follower.rpcAddr: 0}
	leader.peers = []string{follower.rpcAddr}

	follower.resetElectionTimer()
	leader.sendHeartbeat(follower.rpcAddr)

	// Give it a moment
	time.Sleep(50 * time.Millisecond)

	// Check: leader should have updated indices
	leader.mu.Lock()
	defer leader.mu.Unlock()

	// After sending 2 entries successfully:
	// matchIndex should be 2 (follower confirmed entries 1 and 2)
	// nextIndex should be 3 (next entry to send)
	if leader.matchIndex[follower.rpcAddr] != 2 {
		t.Errorf("matchIndex = %d, want 2", leader.matchIndex[follower.rpcAddr])
	}
	if leader.nextIndex[follower.rpcAddr] != 3 {
		t.Errorf("nextIndex = %d, want 3", leader.nextIndex[follower.rpcAddr])
	}
}

// Test: Leader commits entry when majority have it
func TestLeader_CommitsOnMajority(t *testing.T) {
	// Create leader with 2 log entries (term 2)
	leader := newTestRaft(t, "leader", []string{}, nil)
	leader.state = Leader
	leader.currentTerm = 2
	leader.log = []LogEntry{
		{Term: 2, Command: []byte("cmd1")},
		{Term: 2, Command: []byte("cmd2")},
	}
	leader.commitIndex = 0 // nothing committed yet

	// Create 2 followers (3-node cluster)
	follower1 := newTestRaft(t, "follower1", []string{}, nil)
	follower1.currentTerm = 2
	follower2 := newTestRaft(t, "follower2", []string{}, nil)
	follower2.currentTerm = 2

	// Start RPC servers
	leader.StartRPCServer("localhost:0")
	defer leader.listener.Close()
	follower1.StartRPCServer("localhost:0")
	defer follower1.listener.Close()
	follower2.StartRPCServer("localhost:0")
	defer follower2.listener.Close()

	// Initialize leader's tracking
	leader.peers = []string{follower1.rpcAddr, follower2.rpcAddr}
	leader.nextIndex = map[string]int{
		follower1.rpcAddr: 1,
		follower2.rpcAddr: 1,
	}
	leader.matchIndex = map[string]int{
		follower1.rpcAddr: 0,
		follower2.rpcAddr: 0,
	}

	// Start election timers on followers
	follower1.resetElectionTimer()
	follower2.resetElectionTimer()

	// Leader sends heartbeats to both
	leader.sendHeartbeat(follower1.rpcAddr)
	leader.sendHeartbeat(follower2.rpcAddr)

	// Wait for replication
	time.Sleep(100 * time.Millisecond)

	// Check: commitIndex should be 2 (majority have both entries)
	leader.mu.Lock()
	defer leader.mu.Unlock()

	if leader.commitIndex != 2 {
		t.Errorf("commitIndex = %d, want 2", leader.commitIndex)
	}
}

// Test: Committed entries are sent to applyCh
func TestApplyChannel_ReceivesCommittedEntries(t *testing.T) {
	// Create apply channel with buffer
	applyCh := make(chan ApplyMsg, 10)

	// Create leader with apply channel
	leader := newTestRaft(t, "leader", []string{}, applyCh)
	leader.state = Leader
	leader.currentTerm = 2
	leader.log = []LogEntry{
		{Term: 2, Command: []byte("PUT foo bar")},
		{Term: 2, Command: []byte("PUT baz qux")},
	}
	leader.commitIndex = 0
	leader.lastApplied = 0

	// Create follower
	follower := newTestRaft(t, "follower", []string{}, nil)
	follower.currentTerm = 2

	// Start RPC servers
	leader.StartRPCServer("localhost:0")
	defer leader.listener.Close()
	follower.StartRPCServer("localhost:0")
	defer follower.listener.Close()

	// Initialize leader's tracking
	leader.peers = []string{follower.rpcAddr}
	leader.nextIndex = map[string]int{follower.rpcAddr: 1}
	leader.matchIndex = map[string]int{follower.rpcAddr: 0}

	follower.resetElectionTimer()
	leader.sendHeartbeat(follower.rpcAddr)

	// Wait for replication
	time.Sleep(100 * time.Millisecond)

	// Check: should receive 2 ApplyMsg
	if len(applyCh) != 2 {
		t.Errorf("applyCh has %d messages, want 2", len(applyCh))
	}

	// Verify first message
	msg1 := <-applyCh
	if msg1.CommandIndex != 1 {
		t.Errorf("msg1.CommandIndex = %d, want 1", msg1.CommandIndex)
	}
	if string(msg1.Command) != "PUT foo bar" {
		t.Errorf("msg1.Command = %s, want 'PUT foo bar'", msg1.Command)
	}

	// Verify second message
	msg2 := <-applyCh
	if msg2.CommandIndex != 2 {
		t.Errorf("msg2.CommandIndex = %d, want 2", msg2.CommandIndex)
	}
	if string(msg2.Command) != "PUT baz qux" {
		t.Errorf("msg2.Command = %s, want 'PUT baz qux'", msg2.Command)
	}
}

// ============ NEW PERSISTENCE TESTS ============

// Test: State is persisted when term changes
func TestPersistence_TermIsSaved(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "raft-persist-*")
	defer os.RemoveAll(tempDir)

	logPath := tempDir + "/node.log"
	statePath := tempDir + "/node.state"

	// Create node, start election (increments term)
	r1 := NewRaft("node1", []string{"node2"}, nil, logPath, statePath)
	r1.startElection() // term becomes 1

	// Create new node with same paths - should restore state
	r2 := NewRaft("node1", []string{"node2"}, nil, logPath, statePath)

	if r2.currentTerm != 1 {
		t.Errorf("expected restored term=1, got %d", r2.currentTerm)
	}
}

// Test: VotedFor is persisted
func TestPersistence_VotedForIsSaved(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "raft-persist-*")
	defer os.RemoveAll(tempDir)

	logPath := tempDir + "/node.log"
	statePath := tempDir + "/node.state"

	// Create node, start election (votes for self)
	r1 := NewRaft("node1", []string{"node2"}, nil, logPath, statePath)
	r1.startElection() // votes for node1

	// Create new node with same paths - should restore state
	r2 := NewRaft("node1", []string{"node2"}, nil, logPath, statePath)

	if r2.votedFor != "node1" {
		t.Errorf("expected restored votedFor=node1, got %s", r2.votedFor)
	}
}

// Test: Log entries are persisted
func TestPersistence_LogEntriesAreSaved(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "raft-persist-*")
	defer os.RemoveAll(tempDir)

	logPath := tempDir + "/node.log"
	statePath := tempDir + "/node.state"

	// Create leader and append entries
	r1 := NewRaft("leader", []string{}, nil, logPath, statePath)
	r1.state = Leader
	r1.currentTerm = 1
	r1.AppendCommand([]byte("PUT foo bar"))
	r1.AppendCommand([]byte("PUT baz qux"))

	// Create new node with same paths - should restore log
	r2 := NewRaft("leader", []string{}, nil, logPath, statePath)

	if len(r2.log) != 2 {
		t.Errorf("expected 2 log entries, got %d", len(r2.log))
	}
	if string(r2.log[0].Command) != "PUT foo bar" {
		t.Errorf("expected first command 'PUT foo bar', got '%s'", r2.log[0].Command)
	}
	if string(r2.log[1].Command) != "PUT baz qux" {
		t.Errorf("expected second command 'PUT baz qux', got '%s'", r2.log[1].Command)
	}
}

// Test: Fresh start with no existing files
func TestPersistence_FreshStart(t *testing.T) {
	tempDir, _ := os.MkdirTemp("", "raft-fresh-*")
	defer os.RemoveAll(tempDir)

	logPath := tempDir + "/node.log"
	statePath := tempDir + "/node.state"

	// Create node - no existing files
	r := NewRaft("node1", []string{}, nil, logPath, statePath)

	if r.currentTerm != 0 {
		t.Errorf("expected term=0 on fresh start, got %d", r.currentTerm)
	}
	if r.votedFor != "" {
		t.Errorf("expected votedFor='' on fresh start, got %s", r.votedFor)
	}
	if len(r.log) != 0 {
		t.Errorf("expected empty log on fresh start, got %d entries", len(r.log))
	}
}

func TestTakeSnapshot_DiscardsOldLogEntries(t *testing.T) {
	r := newTestRaft(t, "node1", []string{}, nil)

	// Manually add log entries (simulating committed commands)
	// Entries at index 1, 2, 3, 4, 5
	for i := 1; i <= 5; i++ {
		r.log = append(r.log, LogEntry{
			Term:    1,
			Command: []byte(fmt.Sprintf("PUT key%d value%d", i, i)),
		})
	}

	// Verify we have 5 entries
	if len(r.log) != 5 {
		t.Fatalf("expected 5 log entries, got %d", len(r.log))
	}

	// Take snapshot at index 3 (discard entries 1-3, keep 4-5)
	snapshotData := []byte("fake snapshot data")
	r.TakeSnapshot(3, snapshotData)

	// Should have 2 entries left (indices 4 and 5)
	if len(r.log) != 2 {
		t.Errorf("expected 2 log entries after snapshot, got %d", len(r.log))
	}

	// Check snapshot metadata
	if r.lastIncludedIndex != 3 {
		t.Errorf("expected lastIncludedIndex=3, got %d", r.lastIncludedIndex)
	}
	if r.lastIncludedTerm != 1 {
		t.Errorf("expected lastIncludedTerm=1, got %d", r.lastIncludedTerm)
	}
	if string(r.snapshotData) != "fake snapshot data" {
		t.Errorf("snapshot data mismatch")
	}
}

func TestTakeSnapshot_IndexConversion(t *testing.T) {
	r := newTestRaft(t, "node1", []string{}, nil)

	// Add 10 entries (indices 1-10)
	for i := 1; i <= 10; i++ {
		r.log = append(r.log, LogEntry{Term: 1, Command: []byte("cmd")})
	}

	// Snapshot at index 5
	r.TakeSnapshot(5, []byte("snap1"))

	// Now log has entries 6-10 (5 entries)
	if len(r.log) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(r.log))
	}

	// Test index conversion
	// Slice index 0 should be absolute index 6
	if r.getLogIndex(0) != 6 {
		t.Errorf("getLogIndex(0) should be 6, got %d", r.getLogIndex(0))
	}

	// Absolute index 8 should be slice index 2
	if r.getSliceIndex(8) != 2 {
		t.Errorf("getSliceIndex(8) should be 2, got %d", r.getSliceIndex(8))
	}

	// Snapshot again at index 8
	r.TakeSnapshot(8, []byte("snap2"))

	// Now log has entries 9-10 (2 entries)
	if len(r.log) != 2 {
		t.Errorf("expected 2 entries after second snapshot, got %d", len(r.log))
	}

	if r.lastIncludedIndex != 8 {
		t.Errorf("expected lastIncludedIndex=8, got %d", r.lastIncludedIndex)
	}
}
