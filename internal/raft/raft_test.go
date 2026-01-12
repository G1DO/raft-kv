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