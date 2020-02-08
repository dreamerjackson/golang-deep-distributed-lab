# lab2a实验讲解
*  1、raft.go 的raft结构体 补充字段。 字段应该尽量与raft论文的Figure2接近。
```go
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
```

## 获取当前raft节点的term与状态

```
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
```

## 2、填充RequestVoteArgs和RequestVoteReply结构。

```
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term         int // candidate's term
	CandidateID  int // candidate requesting vote
	LastLogIndex int // index of candidate's last log entry
	LastLogTerm  int // term of candidate's last log entry
}

type RequestVoteReply struct {
	// Your data here (2A).
	CurrentTerm int  // currentTerm, for candidate to update itself
	VoteGranted bool // true means candidate received vote
}
```


## 实现RPC方法RequestVote
* 1、获取当前节点的log个数，以及最后一个log的term 确定当前节点的term。
* 2、如果调用节点的term小于当前节点，返回当前term，并且不为其投票。
* 3、如果调用节点的term大于当前节点，修改当前节点的term，当前节点转为follower.
* 4、如果调用节点的term大于当前节点，或者等于当前节点term并且调用节点的log个数大于等于当前节点的log，则为调用节点投票。
* 5、投票后重置当前节点的选举超时时间。
```
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
```

## 修改make
*  除了一些基本的初始化过程,新开了一个goroutine。
```
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

```

## 选举核心electionDaemon
* 除了shutdown，还有两个通道，一个是electionTimer，用于选举超时。
* 一个是resetTimer,用于重置选举超时。
* 注意time.reset是很难正确使用的。
* 一旦选举超时，调用go rf.canvassVotes()
```
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

```


## 拉票
* replyHandler是进行请求返回后的处理。
* 当前节点为了成为leader，会调用每一个节点的RequestVote方法。
* 如果返回过来的term大于当前term，那么当前节点变为follower,重置选举超时时间。
* 否则，如果收到了超过一半节点的投票,那么其变为了leader，并立即给其他节点发送心跳检测。
```
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
```


## 心跳检测
* 1、leader调用每一个节点的AppendEntries方法。
* 2、如果当前节点大于调用节点，那么AppendEntries失败。否则，修改当前的term为最大。
* 3、如果当前节点是leader，始终将其变为follower（为了让leader稳定）
* 4、将当前节点投票给调用者（对于落后的节点）。
* 5、重置当前节点的超时时间。
```
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
```



```
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


```

## 处理心跳检测返回
如果心跳检测失败了，那么变为follower，重置选举超时。
```
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


```