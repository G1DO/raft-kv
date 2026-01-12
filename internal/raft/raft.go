package raft

import (
	"math/rand"
	"sync"
	"time"
)

type State int
//like num and iota starts at 0 and increments automatically.
const (
	Follower State = iota
	Candidate
	Leader
)

type Raft struct {
	mu sync.Mutex // protects all fields below (concurrency safety)

	// Persistent state (must survive crash)
	currentTerm int    // latest term this node has seen
	votedFor    string // nodeID we voted for in current term ("" if none)
    //Can vote for only one (including yourself)

	// Volatile state
	state State  // Follower, Candidate, or Leader
	id    string // this node's ID
	peers []string // other nodes' addresses

	// Election
	electionTimer  *time.Timer   // fires when election timeout expires
	votesReceived  int           // count of votes when candidate

	// Heartbeat (leader only)
	heartbeatTimer *time.Timer   // fires to send heartbeats to followers
}

func NewRaft(id string,peers []string) *Raft{
	// Create struct and return and init all values 
	r := &Raft{
        currentTerm:   0,
        votedFor:      "",
        state:         Follower,
        id:            id,
        peers:         peers,
        votesReceived: 0,
    }
	return r

}

// resetElectionTimer sets a new random timeout (150-300ms)
// Random so not everyone times out at the same moment (prevents split votes)
func (r *Raft) resetElectionTimer() {
	// 1. If timer exists, stop it first
	if r.electionTimer != nil {
		r.electionTimer.Stop()
	}

	// 2. Pick random duration between 150-300ms
	duration := time.Duration(150+rand.Intn(150)) * time.Millisecond

	// 3. Create new timer that calls startElection when it fires
	r.electionTimer = time.AfterFunc(duration, func() {
		r.startElection()
	})
}
//idea of lock for goroutines 
// startElection is called when election timeout fires
// Follower becomes Candidate and asks everyone for votes
func (r *Raft) startElection() {
r.mu.Lock()

	// Change state to Candidate
	r.state = Candidate

	// Increment currentTerm (new election = new term)
r.currentTerm++

	// Vote for yourself
	r.votedFor = r.id
	r.votesReceived = 1

	r.mu.Unlock()

	// Reset election timer (in case we don't win, we'll try again)
	r.resetElectionTimer()

	// Send RequestVote to all peers
	for _, peer := range r.peers {
		go r.sendRequestVote(peer)
	}
}

// sendRequestVote sends a vote request to one peer
func (r *Raft) sendRequestVote(peer string) {
	r.mu.Lock()
	args := RequestVoteArgs{
		Term:        r.currentTerm,
		CandidateID: r.id,
	}
	r.mu.Unlock()

	// TODO: Actually send over network (for now just a placeholder)
	// reply := RequestVoteReply{}
	// ok := r.callRPC(peer, "Raft.RequestVote", &args, &reply)

	// For now, simulate: we'll add real networking later
	_ = args
	_ = peer
}

// handleVoteResponse processes a vote response from a peer
func (r *Raft) handleVoteResponse(reply *RequestVoteReply) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If reply has higher term, step down
	if reply.Term > r.currentTerm {
		r.currentTerm = reply.Term
		r.state = Follower
		r.votedFor = ""
		return
	}

	// Only count vote if we're still a candidate
	if r.state != Candidate {
		return
	}

	// Count the vote if granted
	if reply.VoteGranted {
		r.votesReceived++

		// Check if we have majority
		// Majority = more than half of (peers + self)
		if r.votesReceived > (len(r.peers)+1)/2 {
			r.becomeLeader()
		}
	}
}

// becomeLeader transitions from candidate to leader
func (r *Raft) becomeLeader() {
	r.state = Leader
	if r.electionTimer != nil {
		r.electionTimer.Stop() // leaders don't need election timer
	}

	// Start sending heartbeats to all peers
	r.startHeartbeats()
}

// startHeartbeats begins the heartbeat loop (leader only)
// Sends heartbeat to all peers every 50ms
func (r *Raft) startHeartbeats() {
	// Send immediately, then repeat every 50ms
	r.sendHeartbeats()

	r.heartbeatTimer = time.AfterFunc(50*time.Millisecond, func() {
		r.mu.Lock()
		if r.state != Leader {
			r.mu.Unlock()
			return // not leader anymore, stop heartbeats
		}
		r.mu.Unlock()

		// Send heartbeats and schedule next round
		r.startHeartbeats()
	})
}

// sendHeartbeats sends a heartbeat to all peers
func (r *Raft) sendHeartbeats() {
	for _, peer := range r.peers {
		go r.sendHeartbeat(peer)
	}
}

// sendHeartbeat sends a heartbeat to one peer
func (r *Raft) sendHeartbeat(peer string) {
	r.mu.Lock()
	args := AppendEntriesArgs{
		Term:     r.currentTerm,
		LeaderID: r.id,
	}
	r.mu.Unlock()

	// TODO: Actually send over network (for now just a placeholder)
	// reply := AppendEntriesReply{}
	// ok := r.callRPC(peer, "Raft.AppendEntries", &args, &reply)

	_ = args
	_ = peer
}

// handleAppendEntriesResponse processes a heartbeat response from a peer
func (r *Raft) handleAppendEntriesResponse(reply *AppendEntriesReply) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If reply has higher term, step down to follower
	if reply.Term > r.currentTerm {
		r.currentTerm = reply.Term
		r.state = Follower
		r.votedFor = ""

		// Stop heartbeat timer since we're no longer leader
		if r.heartbeatTimer != nil {
			r.heartbeatTimer.Stop()
		}
	}
}

// RequestVoteArgs is what a candidate sends when asking for votes
type RequestVoteArgs struct {
	Term        int    // candidate's term
	CandidateID string // who is asking for vote
}

// RequestVoteReply is the response to a vote request
type RequestVoteReply struct {
	Term        int  // responder's current term (so candidate can update if behind)
	VoteGranted bool // true = you got my vote
}

// RequestVote handles incoming vote requests from candidates
// Returns: should I vote for this candidate?
func (r *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Always tell them our term
	reply.Term = r.currentTerm
	reply.VoteGranted = false

	// Rule 1: If candidate's term < my term, reject (they're outdated)
	if args.Term < r.currentTerm {
		return // reply.VoteGranted already false
	}

	// Rule 2: If candidate's term > my term, update my term and become follower
	if args.Term > r.currentTerm {
		r.currentTerm = args.Term  // update to their term
		r.state = Follower         // step down
		r.votedFor = ""            // new term = can vote again
	}

	// Rule 3: Grant vote if I haven't voted OR already voted for this candidate
	if r.votedFor == "" || r.votedFor == args.CandidateID {
		r.votedFor = args.CandidateID
		reply.VoteGranted = true
	}
}

// AppendEntriesArgs is what leader sends (heartbeat or log entries)
type AppendEntriesArgs struct {
	Term     int    // leader's term
	LeaderID string // so follower knows who the leader is
}

// AppendEntriesReply is the response to AppendEntries
type AppendEntriesReply struct {
	Term    int  // follower's current term
	Success bool // true if follower accepted
}

// AppendEntries handles incoming heartbeats (and later, log entries) from leader
func (r *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Always tell them our term
	reply.Term = r.currentTerm
	reply.Success = false

	// Rule 1: If leader's term < my term, reject (they're outdated)
	if args.Term < r.currentTerm {
		return
	}

	// Rule 2: If leader's term >= my term, accept them as leader
	if args.Term > r.currentTerm {
		r.currentTerm = args.Term
		r.votedFor = ""
	}

	// Step down to follower (even if we were candidate or leader)
	r.state = Follower

	// Reset election timer - leader is alive!
	r.resetElectionTimer()

	reply.Success = true
}