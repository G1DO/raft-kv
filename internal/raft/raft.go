package raft

import (
	"github.com/G1DO/raft-kv/internal/log"
	"math/rand"
	"sync"
	"time"
	"encoding/json"
    "net"
)

type State int
//like num and iota starts at 0 and increments automatically.
const (
	Follower State = iota
	Candidate
	Leader
)

// ApplyMsg is sent to the application when a command is committed
type ApplyMsg struct {
	CommandIndex int    // index of the committed command
	Command      []byte // the command to apply
}

type Raft struct {
	mu sync.Mutex // protects all fields below (concurrency safety)

	// Persistent state (must survive crash)
	currentTerm int    // latest term this node has seen
	votedFor    string // nodeID we voted for in current term ("" if none)

	// Volatile state
	state State  // Follower, Candidate, or Leader
	id    string // this node's ID
	peers []string // other nodes' addresses

	// Election
	electionTimer  *time.Timer // fires when election timeout expires
	votesReceived  int         // count of votes when candidate

	// Heartbeat (leader only)
	heartbeatTimer *time.Timer // fires to send heartbeats to followers

	// Networking
	rpcAddr  string       // address this node listens on for RPCs
	listener net.Listener // TCP listener for incoming RPCs

	// Log replication
	log         []LogEntry // the log entries
	commitIndex int        // highest entry known to be committed
	lastApplied int        // highest entry applied to state machine

	// Leader state (reinitialized after election)
	nextIndex  map[string]int // for each peer: index of next entry to send
	matchIndex map[string]int // for each peer: highest entry known to be replicated

	// Channel to send committed commands to application
	applyCh chan ApplyMsg
	// Persistence
    persistentLog *log.Log  // persistent storage for log entries
    statePath     string    // path to state file (term/votedFor)
}

// NewRaft creates a new Raft node
// applyCh is optional - if nil, committed commands won't be sent anywhere
func NewRaft(id string, peers []string, applyCh chan ApplyMsg,logPath string,statePath string) *Raft {
	persistentLog, err := log.NewLog(logPath)
  if err != nil {
    panic(err)  // or handle properly later
	}

	
	// Load saved state (term/votedFor) if it exists
savedState, err := LoadRaftState(statePath)
if err != nil {
    panic(err)  // or handle properly
}

// Replay log entries from disk
entries, err := persistentLog.Replay()
if err != nil {
	panic(err)
}

// Convert bytes back to LogEntry
var logEntries []LogEntry
for _, data := range entries {
	var entry LogEntry
	json.Unmarshal(data, &entry)
	logEntries = append(logEntries, entry)
}

	r := &Raft{
		currentTerm:   savedState.CurrentTerm,
		votedFor:      savedState.VotedFor,
		state:         Follower,
		id:            id,
		peers:         peers,
		votesReceived: 0,
		applyCh:       applyCh,
		persistentLog: persistentLog,
		statePath:     statePath,
		log:           logEntries,
	}
	return r
}

// persist saves currentTerm and votedFor to disk
// Called whenever these values change
func (r *Raft) persist() {
	state := RaftState{
		CurrentTerm: r.currentTerm,
		VotedFor:    r.votedFor,
	}
	SaveRaftState(r.statePath, state)
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
	r.persist() // save term and vote to disk

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

	//calculate last log index and term
	lastLogIndex := len(r.log)
    lastLogTerm := 0
    if lastLogIndex > 0 {
        lastLogTerm = r.log[lastLogIndex-1].Term
    }
	args := RequestVoteArgs{
		Term:        r.currentTerm,
		CandidateID: r.id,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm, 
	}
	r.mu.Unlock()

	// Send the RPC
	var reply RequestVoteReply
	ok := r.callRPC(peer, "RequestVote", &args, &reply)
	if !ok {
		return // peer unreachable, ignore
	}

	// Process the response
	r.handleVoteResponse(&reply)

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
		r.persist() // save term and vote to disk
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

	// Initialize nextIndex and matchIndex for all peers
	r.nextIndex = make(map[string]int)
	r.matchIndex = make(map[string]int)

	for _, peer := range r.peers {
		r.nextIndex[peer] = len(r.log) + 1 // optimistic: assume peer is caught up
		r.matchIndex[peer] = 0             // pessimistic: nothing confirmed yet
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

// sendHeartbeat sends a heartbeat (with log entries) to one peer
func (r *Raft) sendHeartbeat(peer string) {
	r.mu.Lock()

	// Step 1: Get nextIndex for this peer
	nextIdx := r.nextIndex[peer]

	// Step 2: Calculate PrevLogIndex and PrevLogTerm
	// PrevLogIndex = entry right before what we're sending
	prevLogIndex := nextIdx - 1
	prevLogTerm := 0
	if prevLogIndex > 0 && prevLogIndex <= len(r.log) {
		prevLogTerm = r.log[prevLogIndex-1].Term // -1 because log is 0-indexed
	}

	// Step 3: Get entries to send (from nextIndex to end of log)
	var entries []LogEntry
	if nextIdx <= len(r.log) {
		entries = r.log[nextIdx-1:] // -1 because log is 0-indexed
	}

	// Step 4: Build the args with all fields
	args := AppendEntriesArgs{
		Term:         r.currentTerm,
		LeaderID:     r.id,
		PrevLogIndex: prevLogIndex,
		PrevLogTerm:  prevLogTerm,
		Entries:      entries,
		LeaderCommit: r.commitIndex,
	}

	// Save these for updating indices after RPC
	numEntries := len(entries)
	r.mu.Unlock()

	// Send the RPC
	var reply AppendEntriesReply
	ok := r.callRPC(peer, "AppendEntries", &args, &reply)
	if !ok {
		return // peer unreachable, ignore
	}

	// Step 5: Handle success/failure
	r.mu.Lock()
	defer r.mu.Unlock()

	// If reply has higher term, step down
	if reply.Term > r.currentTerm {
		r.currentTerm = reply.Term
		r.state = Follower
		r.votedFor = ""
		r.persist() // save term and vote to disk
		if r.heartbeatTimer != nil {
			r.heartbeatTimer.Stop()
		}
		return
	}

	// Only update if we're still leader
	if r.state != Leader {
		return
	}

	if reply.Success {
		// Follower accepted - update indices
		// matchIndex = prevLogIndex + number of entries sent
		r.matchIndex[peer] = prevLogIndex + numEntries
		// nextIndex = matchIndex + 1
		r.nextIndex[peer] = r.matchIndex[peer] + 1

		// Check if we can commit new entries
		r.updateCommitIndex()
	} else {
		// Follower rejected - back off nextIndex
		if r.nextIndex[peer] > 1 {
			r.nextIndex[peer]--
		}
	}
}

// updateCommitIndex checks if any new entries can be committed
// An entry is committed when majority of cluster has it
// Only commits entries from current term (Raft safety rule)
func (r *Raft) updateCommitIndex() {
	// For each index from commitIndex+1 to len(log)
	for n := r.commitIndex + 1; n <= len(r.log); n++ {
		// Only commit entries from current term
		if r.log[n-1].Term != r.currentTerm {
			continue
		}

		// Count how many peers have this entry
		count := 1 // leader has it

		for _, peer := range r.peers {
			if r.matchIndex[peer] >= n {
				count++
			}
		}

		// Majority = more than half of cluster
		clusterSize := len(r.peers) + 1
		if count > clusterSize/2 {
			r.commitIndex = n
		}
	}

	// Apply newly committed entries
	r.applyCommitted()
}

// applyCommitted sends committed entries to the application via applyCh
// Called whenever commitIndex advances
func (r *Raft) applyCommitted() {
	// Skip if no channel configured
	if r.applyCh == nil {
		return
	}

	// Apply all entries from lastApplied+1 to commitIndex
	for r.lastApplied < r.commitIndex {
		r.lastApplied++
		msg := ApplyMsg{
			CommandIndex: r.lastApplied,
			Command:      r.log[r.lastApplied-1].Command,
		}
		r.applyCh <- msg
	}
}

// RequestVoteArgs is what a candidate sends when asking for votes
type RequestVoteArgs struct {
	Term        int    // candidate's term
	CandidateID string // who is asking for vote
	LastLogIndex int
	LastLogTerm  int
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
		r.persist() // save term and vote to disk
	}
	// Get my last log info
	myLastLogIndex := len(r.log)
	myLastLogTerm := 0
	if myLastLogIndex > 0 {
		myLastLogTerm = r.log[myLastLogIndex-1].Term
	}

	// Check if candidate's log is at least as up-to-date as mine
	candidateUpToDate := false
	if args.LastLogTerm > myLastLogTerm {
		candidateUpToDate = true
	} else if args.LastLogTerm == myLastLogTerm && args.LastLogIndex >= myLastLogIndex {
		candidateUpToDate = true
	}

	if !candidateUpToDate {
		return // reject - candidate's log is behind mine
	}

	// Rule 3: Grant vote if I haven't voted OR already voted for this candidate
	if r.votedFor == "" || r.votedFor == args.CandidateID {
		r.votedFor = args.CandidateID
		r.persist() // save vote to disk
		reply.VoteGranted = true
	}
}

// AppendEntriesArgs is what leader sends (heartbeat or log entries)
type AppendEntriesArgs struct {
	Term     int    // leader's term
	LeaderID string // so follower knows who the leader is

	// Log replication fields
	PrevLogIndex int        // index of entry immediately before new ones
	PrevLogTerm  int        // term of PrevLogIndex entry
	Entries      []LogEntry // entries to store (empty for heartbeat)
	LeaderCommit int        // leader's commitIndex
}

// AppendEntriesReply is the response to AppendEntries
type AppendEntriesReply struct {
	Term    int  // follower's current term
	Success bool // true if follower accepted
}

// AppendEntries handles incoming heartbeats and log entries from leader
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
		r.persist() // save term and vote to disk
	}

	// Step down to follower (even if we were candidate or leader)
	r.state = Follower

	// Reset election timer - leader is alive!
	r.resetElectionTimer()

	// Log consistency check
	// If PrevLogIndex > 0, we need to verify we have that entry with matching term
	if args.PrevLogIndex > 0 {
		// Check: do we have an entry at PrevLogIndex?
		if args.PrevLogIndex > len(r.log) {
			return // we don't have this entry, reject
		}
		// Check: does the term match?
		if r.log[args.PrevLogIndex-1].Term != args.PrevLogTerm {
			return // term mismatch, reject
		}
	}

	// Append new entries (if any)
	// First, remove any conflicting entries after PrevLogIndex
	if args.PrevLogIndex < len(r.log) {
		r.log = r.log[:args.PrevLogIndex] // truncate
	}
	// Then append the new entries
	r.log = append(r.log, args.Entries...)

	// Persist new entries to disk
	for _, entry := range args.Entries {
		data, _ := json.Marshal(entry)
		r.persistentLog.Append(data)
	}

	reply.Success = true

	// Update commitIndex if leader has committed more
	if args.LeaderCommit > r.commitIndex {
		// Take the minimum: can't commit more than we have
		if args.LeaderCommit < len(r.log) {
			r.commitIndex = args.LeaderCommit
		} else {
			r.commitIndex = len(r.log)
		}
		// Apply newly committed entries
		r.applyCommitted()
	}
}

// RPCMessage wraps all RPC requests
type RPCMessage struct {
    Type string
    Data json.RawMessage
}

// RPCResponse wraps all RPC responses
type RPCResponse struct {
    Data json.RawMessage
}

// StartRPCServer starts listening for incoming RPCs from other nodes
func (r *Raft) StartRPCServer(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	r.listener = listener
	r.rpcAddr = listener.Addr().String()

	// Handle incoming connections in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // listener closed
			}
			go r.handleRPC(conn)
		}
	}()

	return nil
}

// handleRPC processes one incoming RPC request
func (r *Raft) handleRPC(conn net.Conn) {
	defer conn.Close()

	// Decode the incoming message
	decoder := json.NewDecoder(conn)
	var msg RPCMessage
	if err := decoder.Decode(&msg); err != nil {
		return
	}

	// Route to the right handler based on Type
	var response RPCResponse

	switch msg.Type {
	case "RequestVote":
		var args RequestVoteArgs
		json.Unmarshal(msg.Data, &args)
		var reply RequestVoteReply
		r.RequestVote(&args, &reply)
		response.Data, _ = json.Marshal(reply)

	case "AppendEntries":
		var args AppendEntriesArgs
		json.Unmarshal(msg.Data, &args)
		var reply AppendEntriesReply
		r.AppendEntries(&args, &reply)
		response.Data, _ = json.Marshal(reply)
	}

	// Send response back
	encoder := json.NewEncoder(conn)
	encoder.Encode(response)
}

// callRPC sends an RPC to a peer and waits for response
// Returns false if the call failed (network error, timeout, etc.)
func (r *Raft) callRPC(peer string, rpcType string, args interface{}, reply interface{}) bool {
	// Connect to peer
	conn, err := net.DialTimeout("tcp", peer, 500*time.Millisecond)
	if err != nil {
		return false // peer unreachable
	}
	defer conn.Close()

	// Set deadline for the whole RPC
	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))

	// Encode args to JSON
	argsData, err := json.Marshal(args)
	if err != nil {
		return false
	}

	// Send the request
	msg := RPCMessage{
		Type: rpcType,
		Data: argsData,
	}
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(msg); err != nil {
		return false
	}

	// Read the response
	decoder := json.NewDecoder(conn)
	var response RPCResponse
	if err := decoder.Decode(&response); err != nil {
		return false
	}

	// Decode response into reply
	if err := json.Unmarshal(response.Data, reply); err != nil {
		return false
	}

	return true
}
// LogEntry represents one entry in the Raft log
type LogEntry struct {
    Term    int    // term when entry was received by leader
    Command []byte // the command (e.g., "PUT foo bar")
}
// AppendCommand adds a new command to the leader's log.
// Returns the index where stored, the term, and whether this node is the leader.
// If not leader, client should retry with another node.
func (r *Raft) AppendCommand(command []byte) (index int, term int, isLeader bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != Leader {
		return 0, 0, false
	}

	// Create new log entry with current term
	entry := LogEntry{
		Term:    r.currentTerm,
		Command: command,
	}

	// Append to our log slice (Array data structure)
	r.log = append(r.log, entry)

	// Persist to disk
	data, _ := json.Marshal(entry)
	r.persistentLog.Append(data)

	// Return index (1-based), term, and true (we are leader)
	index = len(r.log)
	term = r.currentTerm
	isLeader = true
	return
}

