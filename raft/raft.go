package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"6.824-lab/labrpc"
)

// import "bytes"
// import "labgob"

//
// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in Lab 3 you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh; at that point you can add fields to
// ApplyMsg, but set CommandValid to false for these other uses.
//
type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int
}

// Log Entry
type LogEntry struct {
	Term    int
	Command interface{}
}

const (
	Follower = iota
	Candidate
	Leader
)

//
// A Go object implementing a single Raft peer.
//
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.
	state             int           // follower, candidate or leader
	resetTimer        chan struct{} // for reset election timer
	electionTimer     *time.Timer   // election timer
	electionTimeout   time.Duration // 400~800ms
	heartbeatInterval time.Duration // 100ms

	CurrentTerm int        // Persisted before responding to RPCs
	VotedFor    int        // Persisted before responding to RPCs
	Logs        []LogEntry // Persisted before responding to RPCs
	commitCond  *sync.Cond // for commitIndex update
	//newEntryCond []*sync.Cond // for new log entry
	commitIndex int           // Volatile state on all servers
	lastApplied int           // Volatile state on all servers
	nextIndex   []int         // Leader only, reinitialized after election
	matchIndex  []int         // Leader only, reinitialized after election
	applyCh     chan ApplyMsg // outgoing channel to service
	shutdownCh  chan struct{} // shutdown channel, shut raft instance gracefully
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {

	var term int
	var isleader bool
	// Your code here (2A).
	rf.mu.Lock()
	defer rf.mu.Unlock()

	term = rf.CurrentTerm
	isleader = rf.state == Leader
	return term, isleader
}

//
// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
//
func (rf *Raft) persist() {
	// Your code here (2C).
	// Example:
	// w := new(bytes.Buffer)
	// e := labgob.NewEncoder(w)
	// e.Encode(rf.xxx)
	// e.Encode(rf.yyy)
	// data := w.Bytes()
	// rf.persister.SaveRaftState(data)
}

//
// restore previously persisted state.
//
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	// Your code here (2C).
	// Example:
	// r := bytes.NewBuffer(data)
	// d := labgob.NewDecoder(r)
	// var xxx
	// var yyy
	// if d.Decode(&xxx) != nil ||
	//    d.Decode(&yyy) != nil {
	//   error...
	// } else {
	//   rf.xxx = xxx
	//   rf.yyy = yyy
	// }
}

//
// example RequestVote RPC arguments structure.
// field names must start with capital letters!
//
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term         int // candidate's term
	CandidateID  int // candidate requesting vote
	LastLogIndex int // index of candidate's last log entry
	LastLogTerm  int // term of candidate's last log entry
}

func (rf *Raft) fillRequestVoteArgs(args *RequestVoteArgs) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// turn to candidate and vote to itself
	rf.VotedFor = rf.me
	rf.CurrentTerm += 1
	rf.state = Candidate

	args.Term = rf.CurrentTerm
	args.CandidateID = rf.me
	args.LastLogIndex, args.LastLogTerm = rf.lastLogIndexAndTerm()
}

//
// example RequestVote RPC reply structure.
// field names must start with capital letters!
//
type RequestVoteReply struct {
	// Your data here (2A).
	CurrentTerm int  // currentTerm, for candidate to update itself
	VoteGranted bool // true means candidate received vote
}

//
// example RequestVote RPC handler.
//
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	select {
	case <-rf.shutdownCh:
		DPrintf("[%d-%s]: peer %d is shutting down, reject RV rpc request.\n", rf.me, rf, rf.me)
		return
	default:
	}

	rf.mu.Lock()
	defer rf.mu.Unlock()

	lastLogIdx, lastLogTerm := rf.lastLogIndexAndTerm()

	DPrintf("[%d-%s]: rpc RV, from peer: %d, arg term: %d, my term: %d (last log idx: %d->%d, term: %d->%d)\n", rf.me, rf, args.CandidateID, args.Term, rf.CurrentTerm, args.LastLogIndex,
		lastLogIdx, args.LastLogTerm, lastLogTerm)

	if args.Term < rf.CurrentTerm {
		reply.CurrentTerm = rf.CurrentTerm
		reply.VoteGranted = false
	} else {
		if args.Term > rf.CurrentTerm {
			// convert to follower
			rf.CurrentTerm = args.Term
			rf.state = Follower
			rf.VotedFor = -1
		}

		// if is null (follower) or itself is a candidate (or stale leader) with same term
		if rf.VotedFor == -1 { //|| (rf.VotedFor == rf.me && !sameTerm) { //|| rf.votedFor == args.CandidateID {
			// check whether candidate's log is at-least-as update
			if (args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIdx) ||
				args.LastLogTerm > lastLogTerm {

				rf.resetTimer <- struct{}{}

				rf.state = Follower
				rf.VotedFor = args.CandidateID
				reply.VoteGranted = true

				DPrintf("[%d-%s]: peer %d vote to peer %d (last log idx: %d->%d, term: %d->%d)\n",
					rf.me, rf, rf.me, args.CandidateID, args.LastLogIndex, lastLogIdx, args.LastLogTerm, lastLogTerm)
			}
		}
	}
}

// should be called when holding the lock
func (rf *Raft) lastLogIndexAndTerm() (int, int) {
	index := len(rf.Logs) - 1
	term := rf.Logs[index].Term
	return index, term
}

//
// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
//
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

// Log Replication and HeartBeat
type AppendEntriesArgs struct {
	Term         int        // leader's term
	LeaderID     int        // so follower can redirect clients
	PrevLogIndex int        // index of log entry immediately preceding new ones
	PrevLogTerm  int        // term of prevLogIndex entry
	Entries      []LogEntry // log entries to store(empty for heartbeat; may send more than one for efficiency)
	LeaderCommit int        // leader's commitIndex
}

type AppendEntriesReply struct {
	CurrentTerm int  // currentTerm, for leader to update itself
	Success     bool // true if follower contained entry matching prevLogIndex and prevLogTerm
}

// should be called when holding lock
func (rf *Raft) turnToFollow() {
	rf.state = Follower
	rf.VotedFor = -1
}

func (rf *Raft) String() string {
	switch rf.state {
	case Leader:
		return "l"
	case Candidate:
		return "c"
	case Follower:
		return "f"
	default:
		return ""
	}
}

// AppendEntries handler, including heartbeat, must backup quickly
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	select {
	case <-rf.shutdownCh:
		DPrintf("[%d-%s]: peer %d is shutting down, reject AE rpc request.\n", rf.me, rf, rf.me)
		return
	default:
	}

	DPrintf("[%d-%s]: rpc AE, from peer: %d, term: %d\n", rf.me, rf, args.LeaderID, args.Term)
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if args.Term < rf.CurrentTerm {
		//DPrintf("[%d-%s]: AE failed from leader %d. (heartbeat: leader's term < follower's term (%d < %d))\n",
		//	rf.me, rf, args.LeaderID, args.Term, rf.currentTerm)
		reply.CurrentTerm = rf.CurrentTerm
		reply.Success = false
		return
	}
	if rf.CurrentTerm < args.Term {
		rf.CurrentTerm = args.Term
	}

	// for stale leader
	if rf.state == Leader {
		rf.turnToFollow()
	}
	// for straggler (follower)
	if rf.VotedFor != args.LeaderID {
		rf.VotedFor = args.LeaderID
	}

	// valid AE, reset election timer
	// if the node recieve heartbeat. then it will reset the election timeout
	rf.resetTimer <- struct{}{}

	reply.Success = true
	reply.CurrentTerm = rf.CurrentTerm
	return
}

//
// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
//
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := -1
	isLeader := true

	// Your code here (2B).

	return index, term, isLeader
}

//
// the tester calls Kill() when a Raft instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
//
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

// should be called when holding the lock
func (rf *Raft) resetOnElection() {
	count := len(rf.peers)
	length := len(rf.Logs)

	for i := 0; i < count; i++ {
		rf.matchIndex[i] = 0
		rf.nextIndex[i] = length
		if i == rf.me {
			rf.matchIndex[i] = length - 1
		}
	}
}

// n: which follower
func (rf *Raft) consistencyCheckReplyHandler(n int, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != Leader {
		return
	}
	if reply.Success {

	} else {
		// found a new leader? turn to follower
		if rf.state == Leader && reply.CurrentTerm > rf.CurrentTerm {
			rf.turnToFollow()
			rf.resetTimer <- struct{}{}
			DPrintf("[%d-%s]: leader %d found new term (heartbeat resp from peer %d), turn to follower.",
				rf.me, rf, rf.me, n)
			return
		}
	}
}

// bool is not useful
func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

func (rf *Raft) consistencyCheck(n int) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	pre := rf.nextIndex[n] - 1
	var args = AppendEntriesArgs{
		Term:         rf.CurrentTerm,
		LeaderID:     rf.me,
		PrevLogIndex: pre,
		PrevLogTerm:  rf.Logs[pre].Term,
		Entries:      nil,
		LeaderCommit: rf.commitIndex,
	}
	go func() {
		DPrintf("[%d-%s]: consistency Check to peer %d.\n", rf.me, rf, n)
		var reply AppendEntriesReply
		if rf.sendAppendEntries(n, &args, &reply) {
			rf.consistencyCheckReplyHandler(n, &reply)
		}
	}()
}

// heartbeatDaemon will exit when is not leader any more
// Only leader can issue heartbeat message.
func (rf *Raft) heartbeatDaemon() {
	for {
		if _, isLeader := rf.GetState(); !isLeader {
			return
		}
		// reset leader's election timer
		rf.resetTimer <- struct{}{}

		select {
		case <-rf.shutdownCh:
			return
		default:
			for i := 0; i < len(rf.peers); i++ {
				if i != rf.me {
					go rf.consistencyCheck(i) // routine heartbeat
				}
			}
		}
		time.Sleep(rf.heartbeatInterval)
	}
}

// electionDaemon
func (rf *Raft) electionDaemon() {
	for {
		select {
		case <-rf.shutdownCh:
			DPrintf("[%d-%s]: peer %d is shutting down electionDaemon.\n", rf.me, rf, rf.me)
			return
		case <-rf.resetTimer:
			if !rf.electionTimer.Stop() {
				<-rf.electionTimer.C
			}
			rf.electionTimer.Reset(rf.electionTimeout)
		case <-rf.electionTimer.C:
			rf.mu.Lock()
			DPrintf("[%d-%s]: peer %d election timeout, issue election @ term %d\n", rf.me, rf, rf.me, rf.CurrentTerm)
			rf.mu.Unlock()
			go rf.canvassVotes()
			rf.electionTimer.Reset(rf.electionTimeout)
		}
	}
}

// canvassVotes issues RequestVote RPC
func (rf *Raft) canvassVotes() {
	var voteArgs RequestVoteArgs
	rf.fillRequestVoteArgs(&voteArgs)
	peers := len(rf.peers)

	var votes = 1
	replyHandler := func(reply *RequestVoteReply) {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		if rf.state == Candidate {
			if reply.CurrentTerm > voteArgs.Term {
				rf.CurrentTerm = reply.CurrentTerm
				rf.turnToFollow()
				//rf.persist()
				rf.resetTimer <- struct{}{} // reset timer
				return
			}
			if reply.VoteGranted {
				if votes == peers/2 {
					rf.state = Leader
					rf.resetOnElection()    // reset leader state
					go rf.heartbeatDaemon() // new leader, start heartbeat daemon
					DPrintf("[%d-%s]: peer %d become new leader.\n", rf.me, rf, rf.me)
					return
				}
				votes++
			}
		}
	}
	for i := 0; i < peers; i++ {
		if i != rf.me {
			go func(n int) {
				var reply RequestVoteReply
				if rf.sendRequestVote(n, &voteArgs, &reply) {
					replyHandler(&reply)
				}
			}(i)
		}
	}
}

//
// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
//
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me
	rf.applyCh = applyCh
	// Your initialization code here (2A, 2B, 2C).
	rf.state = Follower
	rf.VotedFor = -1
	rf.Logs = make([]LogEntry, 1) // first index is 1
	rf.Logs[0] = LogEntry{        // placeholder
		Term:    0,
		Command: nil,
	}
	rf.nextIndex = make([]int, len(peers))
	rf.matchIndex = make([]int, len(peers))

	rf.electionTimeout = time.Millisecond * time.Duration(400+rand.Intn(100)*4)
	rf.electionTimer = time.NewTimer(rf.electionTimeout)
	rf.resetTimer = make(chan struct{})
	rf.shutdownCh = make(chan struct{})           // shutdown raft gracefully
	rf.commitCond = sync.NewCond(&rf.mu)          // commitCh, a distinct goroutine
	rf.heartbeatInterval = time.Millisecond * 40 // small enough, not too small

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())
	go rf.electionDaemon() // kick off election
	return rf
}
