package kvraft

import (
	"6.824-lab/labgob"
	"6.824-lab/labrpc"
	"6.824-lab/raft"
	"bytes"
	"encoding/gob"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

const Debug = 0

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug > 0 {
		log.Printf(format, a...)
	}
	return
}

func init(){
	rand.Seed(time.Now().UnixNano())
}
type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	Key      string
	Value    string
	Op       string // "Get", "Put" or "Append"
	ClientID int64  // client id
	SeqNo    int    // request sequence number
}

type LatestReply struct {
	Seq   int      // latest request
	Reply GetReply // latest reply
}


type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	// Your definitions here.
	persist       *raft.Persister
	db            map[string]string
	snapshotIndex int
	notifyChs     map[int]chan struct{} // per log

	// shutdown chan
	shutdownCh chan struct{}

	// duplication detection table
	duplicate map[int64]*LatestReply
}


func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	// Your code here.
	if _, isLeader := kv.rf.GetState(); !isLeader {
		reply.WrongLeader = true
		reply.Err = ""
		return
	}

	DPrintf("[%d]: leader %d receive rpc: Get(%q).\n", kv.me, kv.me, args.Key)

	kv.mu.Lock()
	// duplicate put/append request
	if dup, ok := kv.duplicate[args.ClientID]; ok {
		// filter duplicate
		if args.SeqNo <= dup.Seq {
			kv.mu.Unlock()
			reply.WrongLeader = false
			reply.Err = OK
			reply.Value = dup.Reply.Value
			return
		}
	}

	cmd := Op{Key: args.Key, Op: "Get", ClientID: args.ClientID, SeqNo: args.SeqNo}
	index, term, _ := kv.rf.Start(cmd)

	ch := make(chan struct{})
	kv.notifyChs[index] = ch

	kv.mu.Unlock()

	reply.WrongLeader = false
	reply.Err = OK

	// wait for Raft to complete agreement
	select {
	case <-ch:
		// lose leadership
		curTerm, isLeader := kv.rf.GetState()
		// what if still leader, but different term? let client retry
		if !isLeader || term != curTerm {
			reply.WrongLeader = true
			reply.Err = ""
			return
		}

		kv.mu.Lock()
		if value, ok := kv.db[args.Key]; ok {
			reply.Value = value
		} else {
			reply.Err = ErrNoKey
		}
		kv.mu.Unlock()
	case <-kv.shutdownCh:
	}
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	// Your code here.
	// not leader
	if _, isLeader := kv.rf.GetState(); !isLeader {
		reply.WrongLeader = true
		reply.Err = ""
		return
	}

	DPrintf("[%d]: leader %d receive rpc: PutAppend(%q => (%q,%q), (%d-%d).\n", kv.me, kv.me,
		args.Op, args.Key, args.Value, args.ClientID, args.SeqNo)

	kv.mu.Lock()
	// duplicate put/append request
	if dup, ok := kv.duplicate[args.ClientID]; ok {
		// filter duplicate
		if args.SeqNo <= dup.Seq {
			kv.mu.Unlock()
			reply.WrongLeader = false
			reply.Err = OK
			return
		}
	}

	// new request
	cmd := Op{Key: args.Key, Value: args.Value, Op: args.Op, ClientID: args.ClientID, SeqNo: args.SeqNo}
	index, term, _ := kv.rf.Start(cmd)
	ch := make(chan struct{})
	kv.notifyChs[index] = ch
	kv.mu.Unlock()

	reply.WrongLeader = false
	reply.Err = OK

	// wait for Raft to complete agreement
	select {
	case <-ch:
		// lose leadership
		curTerm, isLeader := kv.rf.GetState()
		if !isLeader || term != curTerm {
			reply.WrongLeader = true
			reply.Err = ""
			return
		}
	case <-kv.shutdownCh:
		return
	}
}


// applyDaemon receive applyMsg from Raft layer, apply to Key-Value state machine
// then notify related client if is leader
func (kv *KVServer) applyDaemon() {
	for {
		select {
		case <-kv.shutdownCh:
			DPrintf("[%d]: server %d is shutting down.\n", kv.me, kv.me)
			return
		case msg, ok := <-kv.applyCh:
			if ok {
				// have snapshot to apply?
				if msg.UseSnapshot {
					kv.mu.Lock()
					kv.readSnapshot(msg.Snapshot)
					// must be persisted, in case of crashing before generating another snapshot
					kv.generateSnapshot(msg.CommandIndex)
					kv.mu.Unlock()
					continue
				}
				// have client's request? must filter duplicate command
				if msg.Command != nil && msg.CommandIndex > kv.snapshotIndex{
					cmd := msg.Command.(Op)
					kv.mu.Lock()
					if dup, ok := kv.duplicate[cmd.ClientID]; !ok || dup.Seq < cmd.SeqNo {
						switch cmd.Op {
						case "Get":
							kv.duplicate[cmd.ClientID] = &LatestReply{Seq: cmd.SeqNo,
								Reply: GetReply{Value: kv.db[cmd.Key],}}
						case "Put":
							kv.db[cmd.Key] = cmd.Value
							kv.duplicate[cmd.ClientID] = &LatestReply{Seq: cmd.SeqNo,}
						case "Append":
							kv.db[cmd.Key] += cmd.Value
							kv.duplicate[cmd.ClientID] = &LatestReply{Seq: cmd.SeqNo,}
						default:
							DPrintf("[%d]: server %d receive invalid cmd: %v\n", kv.me, kv.me, cmd)
							panic("invalid command operation")
						}
						if ok {
							DPrintf("[%d]: server %d apply index: %d, cmd: %v (client: %d, dup seq: %d < %d)\n",
								kv.me, kv.me, msg.CommandIndex, cmd, cmd.ClientID, dup.Seq, cmd.SeqNo)
						}
					}
					// snapshot detection: up through msg.Index
					if needSnapshot(kv) {
						// save snapshot and notify raft
						DPrintf("[%d]: server %d need generate snapshot @ %d (%d vs %d), client: %d.\n",
							kv.me, kv.me, msg.CommandIndex, kv.maxraftstate, kv.persist.RaftStateSize(), cmd.ClientID)
						data:= kv.generateSnapshot(msg.CommandIndex)
						kv.rf.NewSnapShot(data,msg.CommandIndex)
					}

					// notify channel
					if notifyCh, ok := kv.notifyChs[msg.CommandIndex]; ok && notifyCh != nil {
						close(notifyCh)
						delete(kv.notifyChs, msg.CommandIndex)
					}
					kv.mu.Unlock()
				}
			}
		}
	}
}


func needSnapshot(kv *KVServer) bool {
	if kv.maxraftstate < 0 {
		return false
	}
	if kv.maxraftstate < kv.persist.RaftStateSize() {
		return true
	}
	// abs < 10% of max
	var abs = kv.maxraftstate - kv.persist.RaftStateSize()
	var threshold = kv.maxraftstate / 10
	if abs < threshold {
		return true
	}
	return false
}

// which index?
func (kv *KVServer) generateSnapshot(index int) []byte{
	w := new(bytes.Buffer)
	e := gob.NewEncoder(w)
	kv.snapshotIndex = index

	e.Encode(kv.db)
	e.Encode(kv.snapshotIndex)
	e.Encode(kv.duplicate)
	data := w.Bytes()


	kv.persist.SaveSnapshot(data)
	return data
}


func (kv *KVServer) readSnapshot(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	r := bytes.NewBuffer(data)
	d := gob.NewDecoder(r)

	kv.db = make(map[string]string)
	kv.duplicate = make(map[int64]*LatestReply)

	d.Decode(&kv.db)
	d.Decode(&kv.snapshotIndex)
	d.Decode(&kv.duplicate)
}

//
// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
//
func (kv *KVServer) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	// Your code here, if desired.
}

func (kv *KVServer) Killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
//
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate
	kv.dead =0
	// You may need initialization code here.
	kv.db = make(map[string]string)
	kv.notifyChs = make(map[int]chan struct{})
	kv.persist = persister
	// shutdown channel
	kv.shutdownCh = make(chan struct{})
	// duplication detection table: client->seq no.-> reply
	kv.duplicate = make(map[int64]*LatestReply)

	kv.applyCh = make(chan raft.ApplyMsg)
	kv.readSnapshot(kv.persist.ReadSnapshot())
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)
	// You may need initialization code here.
	go kv.applyDaemon()

	return kv
}
