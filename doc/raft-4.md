# golang[113]-raft理论与实践[4]-lab2b日志复制
## 准备工作
*  1、阅读[raft论文](http://nil.csail.mit.edu/6.824/2017/papers/raft-extended.pdf)
*  2、阅读[raft理论与实践[1]-理论篇](https://dreamerjonson.com/2019/12/29/golang-110-lab-raft/)
*  3、阅读[raft理论与实践[2]-lab2a](https://dreamerjonson.com/2020/01/06/golang-111-raft-2/)
*  4、阅读[raft理论与实践[3]-lab2a讲解](https://dreamerjonson.com/2020/01/06/golang-111-raft-3-elect/)
*  5、查看我写的这篇文章： [模拟RPC远程过程调用](https://dreamerjonson.com/2019/12/25/golang-109-lab-simulate-rpc/)

## 执行日志
我们需要执行日志中的命令，因此在make函数中，新开一个协程:applyLogEntryDaemon()


```go

func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
    ...
	go rf.applyLogEntryDaemon() // start apply log
	DPrintf("[%d-%s]: newborn election(%s) heartbeat(%s) term(%d) voted(%d)\n",
		rf.me, rf, rf.electionTimeout, rf.heartbeatInterval, rf.CurrentTerm, rf.VotedFor)
	return rf
}
```

*  一个死循环如下
*  1、如果rf.lastApplied == rf.commitIndex, 意味着commit log entry命令都已经被执行了，这时用信号量陷入等待。
*  一旦收到信号，说明需要执行命令。这时会把最后执行的log entry之后，一直到最后一个commit log entry的所有log都传入通道apply中进行执行。
由于是测试，处理apply的逻辑会在测试代码中。

```
// applyLogEntryDaemon exit when shutdown channel is closed
func (rf *Raft) applyLogEntryDaemon() {
	for {
		var logs []LogEntry
		// wait
		rf.mu.Lock()
		for rf.lastApplied == rf.commitIndex {
			rf.commitCond.Wait()
			select {
			case <-rf.shutdownCh:
				rf.mu.Unlock()
				DPrintf("[%d-%s]: peer %d is shutting down apply log entry to client daemon.\n", rf.me, rf, rf.me)
				close(rf.applyCh)
				return
			default:
			}
		}
		last, cur := rf.lastApplied, rf.commitIndex
		if last < cur {
			rf.lastApplied = rf.commitIndex
			logs = make([]LogEntry, cur-last)
			copy(logs, rf.Logs[last+1:cur+1])
		}
		rf.mu.Unlock()
		for i := 0; i < cur-last; i++ {
			// current command is replicated, ignore nil command
			reply := ApplyMsg{
				CommandIndex: last + i + 1,
				Command:      logs[i].Command,
				CommandValid: true,
			}
			// reply to outer service
			// DPrintf("[%d-%s]: peer %d apply %v to client.\n", rf.me, rf, rf.me)
			DPrintf("[%d-%s]: peer %d apply to client.\n", rf.me, rf, rf.me)
			// Note: must in the same goroutine, or may result in out of order apply
			rf.applyCh <- reply
		}
	}
}

```

* 新增 Start函数，此函数为leader执行从client发送过来的命令。
* 当client发送过来之后，首先需要做的就是新增entry 到leader的log中。并且将自身的nextIndex 与matchIndex 更新。

```
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := 0
	isLeader := false

	// Your code here (2B).
	select {
	case <-rf.shutdownCh:
		return -1, 0, false
	default:
		rf.mu.Lock()
		defer rf.mu.Unlock()
		// Your code here (2B).
		if rf.state == Leader {
			log := LogEntry{rf.CurrentTerm, command}
			rf.Logs = append(rf.Logs, log)

			index = len(rf.Logs) - 1
			term = rf.CurrentTerm
			isLeader = true

			//DPrintf("[%d-%s]: client add new entry (%d-%v), logs: %v\n", rf.me, rf, index, command, rf.logs)
			DPrintf("[%d-%s]: client add new entry (%d)\n", rf.me, rf, index)
			//DPrintf("[%d-%s]: client add new entry (%d-%v)\n", rf.me, rf, index, command)

			// only update leader
			rf.nextIndex[rf.me] = index + 1
			rf.matchIndex[rf.me] = index
		}
	}

	return index, term, isLeader
}
```

* 接下来最重要的部分涉及到日志复制，这是通过AppendEntries实现的。我们知道leader会不时的调用consistencyCheck(n)进行一致性检查。
* 在给第n号节点一致性检查时，首先获取pre = rf.nextIndex，pre至少要为1。代表要给n节点发送的log index。因此AppendEntriesArgs参数中，PrevLogIndex 与 prevlogTerm 都为pre - 1位置。
* 代表leader相信PrevLogIndex及其之前的节点都是与leader相同的。
* 将pre及其之后的entry 加入到AppendEntriesArgs参数中。 这些log entry可能是与leader不相同的，或者是follower根本就没有的。

```go
func (rf *Raft) consistencyCheck(n int) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	pre := max(1,rf.nextIndex[n])
	var args = AppendEntriesArgs{
		Term:         rf.CurrentTerm,
		LeaderID:     rf.me,
		PrevLogIndex: pre - 1,
		PrevLogTerm:  rf.Logs[pre - 1].Term,
		Entries:      nil,
		LeaderCommit: rf.commitIndex,
	}

	if rf.nextIndex[n] < len(rf.Logs){
		args.Entries = append(args.Entries, rf.Logs[pre:]...)
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

* 接下来查看follower执行AppendEntries时的反应。
* AppendEntries会新增两个返回参数：
    + ConflictTerm代表可能发生冲突的term
    + FirstIndex 代表可能发生冲突的第一个index。

```
type AppendEntriesReply struct {
	CurrentTerm int  // currentTerm, for leader to update itself
	Success     bool // true if follower contained entry matching prevLogIndex and prevLogTerm
	// extra info for heartbeat from follower
	ConflictTerm int // term of the conflicting entry
	FirstIndex   int // the first index it stores for ConflictTerm
}

```


* 如果args.PrevLogIndex < len(rf.Logs), 表明至少当前节点的log长度是合理的。
* 令preLogIdx 与 args.PrevLogIndex相等。prelogTerm为当前follower节点preLogIdx位置的term。
* 如果拥有相同的term，说明follower与leader 在preLogIdx之前的log entry都是相同的。因此请求是成功的。
* 此时会截断follower的log，将传递过来的entry加入到follower的log之后，执行此步骤后，强制要求与leader的log相同了。
* 请求成功后，reply的ConflictTerm为最后一个log entry的term,reply的FirstIndex为最后一个log entry的index。

* 否则说明leader与follower的日志是有冲突的，冲突的原因可能是：
    + leader认为的match log entry超出了follower的log个数，或者follower 还没有任何log entry（除了index为0的entry是每一个节点都有的）。
    + log在相同的index下，leader的term 与follower的term确是不同的。
* 这时找到follower冲突的term即为ConflictTerm。
* 获取此term的第一个entry的index即为FirstIndex。
* 所以最后，AppendEntries会返回冲突的term以及第一个可能冲突的index。
```
// AppendEntries handler, including heartbeat, must backup quickly
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
    ...
    preLogIdx, preLogTerm := 0, 0
	if args.PrevLogIndex < len(rf.Logs) {
		preLogIdx = args.PrevLogIndex
		preLogTerm = rf.Logs[preLogIdx].Term
	}

	// last log is match
	if preLogIdx == args.PrevLogIndex && preLogTerm == args.PrevLogTerm {
		reply.Success = true
		// truncate to known match
		rf.Logs = rf.Logs[:preLogIdx+1]
		rf.Logs = append(rf.Logs, args.Entries...)
		var last = len(rf.Logs) - 1

		// min(leaderCommit, index of last new entry)
		if args.LeaderCommit > rf.commitIndex {
			rf.commitIndex = min(args.LeaderCommit, last)
			// signal possible update commit index
			go func() { rf.commitCond.Broadcast() }()
		}
		// tell leader to update matched index
		reply.ConflictTerm = rf.Logs[last].Term
		reply.FirstIndex = last

		if len(args.Entries) > 0 {
			DPrintf("[%d-%s]: AE success from leader %d (%d cmd @ %d), commit index: l->%d, f->%d.\n",
				rf.me, rf, args.LeaderID, len(args.Entries), preLogIdx+1, args.LeaderCommit, rf.commitIndex)
		} else {
			DPrintf("[%d-%s]: <heartbeat> current logs: %v\n", rf.me, rf, rf.Logs)
		}
	} else {
		reply.Success = false

		// extra info for restore missing entries quickly: from original paper and lecture note
		// if follower rejects, includes this in reply:
		//
		// the follower's term in the conflicting entry
		// the index of follower's first entry with that term
		//
		// if leader knows about the conflicting term:
		// 		move nextIndex[i] back to leader's last entry for the conflicting term
		// else:
		// 		move nextIndex[i] back to follower's first index
		var first = 1
		reply.ConflictTerm = preLogTerm
		if reply.ConflictTerm == 0 {
			// which means leader has more logs or follower has no log at all
			first = len(rf.Logs)
			reply.ConflictTerm = rf.Logs[first-1].Term
		} else {
			i := preLogIdx
			// term的第一个log entry
			for ; i > 0; i-- {
				if rf.Logs[i].Term != preLogTerm {
					first = i + 1
					break
				}
			}
		}
		reply.FirstIndex = first
		if len(rf.Logs) <= args.PrevLogIndex {
			DPrintf("[%d-%s]: AE failed from leader %d, leader has more logs (%d > %d), reply: %d - %d.\n",
				rf.me, rf, args.LeaderID, args.PrevLogIndex, len(rf.Logs)-1, reply.ConflictTerm,
				reply.FirstIndex)
		} else {
			DPrintf("[%d-%s]: AE failed from leader %d, pre idx/term mismatch (%d != %d, %d != %d).\n",
				rf.me, rf, args.LeaderID, args.PrevLogIndex, preLogIdx, args.PrevLogTerm, preLogTerm)
		}
	}
}

```

* leader调用AppendEntries后，会执行回调函数consistencyCheckReplyHandler。
* 如果调用是成功的，那么正常的跟新matchIndex，nextIndex即下一个要发送的index应该为matchIndex + 1。
* 如果调用失败，说明有冲突。
* 如果confiicting term等于0，说明了leader认为的match log entry超出了follower的log个数，或者follower 还没有任何log entry（除了index为0的entry是每一个节点都有的）。
* 此时简单的让nextIndex 为reply.FirstIndex即可。

* 如果confiicting term不为0，获取leader节点confiicting term 的最后一个log index，此时nextIndex 应该为此index与reply.FirstIndex的最小值。
* 检查最小值是必须的：
* 假设
* s1: 0-0 1-1 1-2 1-3 1-4 1-5
* s2: 0-0 1-1 1-2 1-3 1-4 1-5
* s3: 0-0 1-1

* 此时s1为leader，并一致性检查s3, 从1-5开始检查，此时由于leader有更多的log，因此检查不成功，返回confict term 1， firstindex：2
* 如果只是获取confiicting term 的最后一个log index，那么nextIndex又是1-5，陷入了死循环。


```
func (rf *Raft) consistencyCheckReplyHandler(n int, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != Leader {
		return
	}
	if reply.Success {
		// RPC and consistency check successful
		rf.matchIndex[n] = reply.FirstIndex
		rf.nextIndex[n] = rf.matchIndex[n] + 1
		rf.updateCommitIndex() // try to update commitIndex
	} else {
		// found a new leader? turn to follower
		if rf.state == Leader && reply.CurrentTerm > rf.CurrentTerm {
			rf.turnToFollow()
			rf.resetTimer <- struct{}{}
			DPrintf("[%d-%s]: leader %d found new term (heartbeat resp from peer %d), turn to follower.",
				rf.me, rf, rf.me, n)
			return
		}

		// Does leader know conflicting term?
		var know, lastIndex = false, 0
		if reply.ConflictTerm != 0 {
			for i := len(rf.Logs) - 1; i > 0; i-- {
				if rf.Logs[i].Term == reply.ConflictTerm {
					know = true
					lastIndex = i
					DPrintf("[%d-%s]: leader %d have entry %d is the last entry in term %d.",
						rf.me, rf, rf.me, i, reply.ConflictTerm)
					break
				}
			}
			if know {
				rf.nextIndex[n] = min(lastIndex, reply.FirstIndex)
			} else {
				rf.nextIndex[n] = reply.FirstIndex
			}
		} else {
			rf.nextIndex[n] = reply.FirstIndex
		}
		rf.nextIndex[n] = min(rf.nextIndex[n], len(rf.Logs))
		DPrintf("[%d-%s]: nextIndex for peer %d  => %d.\n",
			rf.me, rf, n, rf.nextIndex[n])
	}
}
```

* 当调用AppendEntry成功后，说明follower与leader的log是匹配的。此时leader会找到commited的log并且执行其命令。
* 这里有一个比较巧妙的方法，对matchIndex排序后取最中间的数。
* 由于matchIndex代表follower有多少log与leader的log匹配，因此中间的log index意味着其得到了大部分节点的认可。
* 因此会将此中间的index之前的所有log entry都执行了。
* rf.Logs[target].Term == rf.CurrentTerm 是必要的：
* 这是由于当一个entry出现在大多数节点的log中，并不意味着其一定会成为commit。考虑下面的情况：
```
  S1: 1 2     1 2 4
  S2: 1 2     1 2
  S3: 1   --> 1 2
  S4: 1       1
  S5: 1       1 3
```
* s1在term2成为leader，只有s1，s2添加了entry2.
* s5变成了term3的leader，之后s1变为了term4的leader，接着继续发送entry2到s3中。
* 此时，如果s5再次变为了leader，那么即便没有S1的支持，S5任然变为了leader，并且应用entry3，覆盖掉entry2。
* 所以一个entry要变为commit，必须：
* 1、在其term周期内，就复制到大多数。
* 2、如果随后的entry被提交。在上例中，如果s1持续成为term4的leader，那么entry2就会成为commit。

*这是由于以下原因造成的：更高任期为最新的投票规则，以及leader将其日志强加给follower。

```
// updateCommitIndex find new commit id, must be called when hold lock
func (rf *Raft) updateCommitIndex() {
	match := make([]int, len(rf.matchIndex))
	copy(match, rf.matchIndex)
	sort.Ints(match)

	DPrintf("[%d-%s]: leader %d try to update commit index: %v @ term %d.\n",
		rf.me, rf, rf.me, rf.matchIndex, rf.CurrentTerm)

	target := match[len(rf.peers)/2]
	if rf.commitIndex < target {
		//fmt.Println("target:",target,match)
		if rf.Logs[target].Term == rf.CurrentTerm {
			//DPrintf("[%d-%s]: leader %d update commit index %d -> %d @ term %d command:%v\n",
			//	rf.me, rf, rf.me, rf.commitIndex, target, rf.CurrentTerm,rf.Logs[target].Command)

			DPrintf("[%d-%s]: leader %d update commit index %d -> %d @ term %d\n",
				rf.me, rf, rf.me, rf.commitIndex, target, rf.CurrentTerm)

			rf.commitIndex = target
			go func() { rf.commitCond.Broadcast() }()
		} else {
			DPrintf("[%d-%s]: leader %d update commit index %d failed (log term %d != current Term %d)\n",
				rf.me, rf, rf.me, rf.commitIndex, rf.Logs[target].Term, rf.CurrentTerm)
		}
	}
}
```

## 参考
* [讲义](https://github.com/dreamerjackson/Distributed-Systems/blob/master/Lec06_Fault_Tolerance_Raft/l-raft2.txt)
* [讲义新](https://pdos.csail.mit.edu/6.824/notes/l-raft2.txt)
