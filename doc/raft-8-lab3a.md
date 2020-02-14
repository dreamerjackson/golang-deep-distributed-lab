# raft理论与实践[6]-lab3a-基于raft构建分布式容错kv服务
## 准备工作
*  阅读[raft论文](http://nil.csail.mit.edu/6.824/2017/papers/raft-extended.pdf)
*  阅读[raft理论与实践[1]-理论篇](https://zhuanlan.zhihu.com/p/102023809)
*  阅读[raft理论与实践[2]-lab2a](https://zhuanlan.zhihu.com/p/102948740)
*  阅读[raft理论与实践[3]-lab2a讲解](https://zhuanlan.zhihu.com/p/103223270)
*  阅读[raft理论与实践[4]-lab2b日志复制](https://zhuanlan.zhihu.com/p/103386687)
*  阅读[raft理论与实践[5]-lab2c日志复制](https://zhuanlan.zhihu.com/p/103473049)
*  阅读[模拟RPC远程过程调用](https://dreamerjonson.com/2019/12/25/golang-109-lab-simulate-rpc/)

## 前言
* 在之前的文章中，我们实现了raft算法的基本框架
* 在本实验中，我们将基于raft算法实现分布式容错的kv服务器
* 客户端用于交互raft服务器
* kvraft/client.go文件用于书写我们的客户端代码，调用Clerk的Get/Put/Append方法为系统提供强一致性的保证
* 这里的强一致性指的是，如果我们一个一个的调用（而不是并发）Clerk的Get/Put/Append方法，那么我们的系统就好像是只有一个raft服务器存在一样，并且调用是序列的，即后面的调用比前面的调用后执行
* 对于并发调用，最终状态可能难以预料，但是必须与这些方法按某种顺序序列化后执行一次的结果相同
* 如果调用在时间上重叠，则这些调用是并发的。例如，如果客户端X调用Clerk.Put(),同时客户端Y调用Clerk.Append()
* 同时，后面的方法在执行之前，必须保证已经观察到前面所有方法执行后的状态（技术上叫做线性化（linearizability））
* 强一致性保证对应用程序很方便，因为这意味着所有客户端都看到相同的最新状态
* 对于单个服务器，强一致性相对简单。多台的副本服务器却相对困难，因为所有服务器必须为并发请求选择相同的执行顺序，并且必须避免使用最新状态来回复客户端

## 本服务实现的功能
*  本服务支持3种基本的操作，`Put(key, value)`, `Append(key, arg)`, and `Get(key)`
*  维护着一个简单的键/值对数据库
* `Put(key, value)`将数据库中特定key的值绑定为value
* `Append(key, arg)`添加，将arg与key对应。如果key的值不存在，则其行为类似于Put
* `Get(key)` 获取当前key的值
* 在本实验中，我们将实现服务具体的功能，而不必担心Raft log日志会无限增长

## 实验思路
* 对lab2中的raft服务器架构进行封装，封装上一些数据库、数据库快照、并会处理log的具体执行逻辑。
* 对于数据库执行的Get/Put/Append方法都对其进行序列化并放入到lab2 raft的体系中，这样就能保证这些方法的一致性
## 获取源代码
* 假设读者已经阅读了准备工作中的一系列文章
* 在此基础上我们增加了本实验的基本框架kvraft文件以及linearizability文件
* 读者需要在kvraft文件夹中，实验本实验的具体功能
* 获取实验代码如下
```
git clone git@github.com:dreamerjackson/golang-deep-distributed-lab.git
git reset --hard   d345b34bc
```

## 客户端
* Clerk结构体存储了所有raft服务器的客户端`servers []*labrpc.ClientEnd`，因此我们可以通过Clerk结构体与所有raft服务器通信
* 我们需要为Clerk结构体实现`Put(key, value)`, `Append(key, arg)`, `Get(key)`方法
* Clerk结构体是我们连接raft服务器的桥梁
* 注意Clerk必须将方法发送到当前的leader节点中，由于其可能并不会知道哪一个节点为leader，因此需要重试。但是记住保存上一个leader的id会加快这一过程，因为leader在稳定的系统里面是不会变的。
* 客户端必须要等到此操作不仅为commit，而且已经被完全应用后，才能够返回，这才能够保证下次get操作能够得到最新的
* 需要注意的是，如果raft服务器出现了分区，可能会陷入一直等待，直到分区消失
### 补充Clerk
* leader记录最后一个leader的序号
* seq 记录rpc的序号
* id记录客户端的唯一id
```
type Clerk struct {
    ...
	leader int   // remember last leader
	seq    int   // RPC sequence number
	id     int64 // client id
}
```

### 补充Get方法
* Get方法会遍历访问每一个raft服务，直到找到leader
* 调用时会陷入堵塞，等待rpc方法返回
* 设置有超时时间，一旦超时，会重新发送
* 为了保证Get方法到的数据是准确最新的，也必须要将其加入到raft算法中
* 客户端必须要等到此操作不仅为commit，而且已经被完全应用后，才能够返回，这才能够保证下次get操作能够得到最新的。
```
func (ck *Clerk) Get(key string) string {
	DPrintf("Clerk: Get: %q\n", key)
	cnt := len(ck.servers)
	for {
		args := &GetArgs{Key: key, ClientID: ck.id, SeqNo: ck.seq}
		reply := new(GetReply)

		ck.leader %= cnt
		done := make(chan bool, 1)
		go func() {
			ok := ck.servers[ck.leader].Call("KVServer.Get", args, reply)
			done <- ok
		}()
		select {
		case <-time.After(200 * time.Millisecond): // rpc timeout: 200ms
			ck.leader++
			continue
		case ok := <-done:
			if ok && !reply.WrongLeader {
				ck.seq++
				if reply.Err == OK {
					return reply.Value
				}
				return ""
			}
			ck.leader++
		}
	}

	return ""
}

```

### 补充Append和Put方法
* 调用同一个PutAppend方法，但是最后一个参数用于标识具体的操作
```
func (ck *Clerk) Put(key string, value string) {
	ck.PutAppend(key, value, "Put")
}
func (ck *Clerk) Append(key string, value string) {
	ck.PutAppend(key, value, "Append")
}

```
* 和Get方法相似，遍历访问每一个raft服务，直到找到leader
* 调用时会陷入堵塞，等待rpc方法返回
* 设置有超时时间，一旦超时，会重新发送
* 客户端必须要等到此操作不仅为commit，而且已经被完全应用后，才能够返回，这才能够保证下次get操作能够得到最新的。

```
func (ck *Clerk) PutAppend(key string, value string, op string) {
	// You will have to modify this function.
	DPrintf("Clerk: PutAppend: %q => (%q,%q) from: %d\n", op, key, value, ck.id)
	cnt := len(ck.servers)
	for {
		args := &PutAppendArgs{Key: key, Value: value, Op: op, ClientID: ck.id, SeqNo: ck.seq}
		reply := new(PutAppendReply)

		ck.leader %= cnt
		done := make(chan bool, 1)
		go func() {
			ok := ck.servers[ck.leader].Call("KVServer.PutAppend", args, reply)
			done <- ok
		}()
		select {
		case <-time.After(200 * time.Millisecond): // rpc timeout: 200ms
			ck.leader++
			continue
		case ok := <-done:
			if ok && !reply.WrongLeader && reply.Err == OK {
				ck.seq++
				return
			}
			ck.leader++
		}
	}
}

```

### Server
* kvraft/server.go文件用于书写我们的客户端代码
* KVServer结构是对于之前书写的raft架构的封装
* `applyCh chan raft.ApplyMsg` 用于状态虚拟机应用coommit log，执行操作
* `db map[string]string` 是模拟的一个数据库
* `notifyChs     map[int]chan struct{}`  commandID => notify chan 状态虚拟机应用此command后，会通知此通道
* `duplicate map[int64]*LatestReply`  检测重复请求

```go
type KVServer struct {
    ...
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	// Your definitions here.
	persist       *raft.Persister
	db            map[string]string
	notifyChs     map[int]chan struct{} // per log
	// duplication detection table
	duplicate map[int64]*LatestReply
}

```

## 完成PutAppend、Get方法
* 下面以PutAppend为例，Get方法类似
* 检测当前是否leader状态
* 检测是否重复请求
* 将此command通过`rf.Start(cmd)`  放入raft中
* `select`等待直到ch被激活，即command index被此kv服务器应用
* ch被激活后,需要再次检测当前节点是否为leader
    + 如果不是,说明leader更换,立即返回错误，这时由于如果不再是leader，那么虽然此kv服务器应用了此command index，但不一定是相同的command
    + 这个时候会堵塞直到序号为commandIndex的命令被应用，但是，如果leader更换，此commandIndex的命令不一定就是我们的当前的命令
    + 但是完全有可能新的leader已经应用了此状态，我们这时候虽然仍然返回错误，希望客户端重试，这是由于操作是幂等的并且重复操作无影响。
    + 优化方案是为command指定一个唯一的标识，这样就能够明确此特定操作是否被应用
```
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

```

## 完成对于log的应用操作
* `<-kv.applyCh` 是当log成为commit状态时，状态机对于log的应用操作
* 本系列构建的为kv-raft服务，根据不同的服务其应用操作的方式不同
* 下面的操作是简单的操作内存map数据库
* 同时，将最后一个操作记录下来，避免同一个log应用了两次。
```
func (kv *KVServer) applyDaemon() {
	for {
		select {
		case msg, ok := <-kv.applyCh:
			if ok {
				// have client's request? must filter duplicate command
				if msg.Command != nil {
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

```


## 测试
```
> go test -v -run=3A
```

* 注意，如果上面的测试出现错误也不一定是程序本身的问题，可能是单个进程运行多个测试程序带来的影响
* 同时，我们可以运行多次避免偶然的影响
* 因此，如果出现了这种情况，我们可以为单个测试程序独立的运行n次，保证正确性，下面是每10个测试程序独立运行，运行n次的脚本
```
rm -rf res
mkdir res
set int j = 0
for ((i = 0; i < 2; i++))
do
    for ((c = $((i*10)); c < $(( (i+1)*10)); c++))
    do
         (go test -v -run TestPersistPartitionUnreliableLinearizable3A) &> ./res/$c &
    done

    sleep 40

    if grep -nr "FAIL.*raft.*" res; then
        echo "fail"
    fi

done
```

## 总结
* 在本实验中，我们封装了lab2a raft框架实现了容错的kv服务
* 如果出现了问题，需要仔细查看log，思考问题出现的原因
* 下一个实验中，我们将实现日志的压缩

## 参考资料
*  [项目链接](https://github.com/dreamerjackson/golang-deep-distributed-lab)
*  [lab3实验介绍]http://nil.csail.mit.edu/6.824/2020/labs/lab-kvraft.html)
*  阅读[raft论文](http://nil.csail.mit.edu/6.824/2017/papers/raft-extended.pdf)
*  阅读[raft理论与实践[1]-理论篇](https://zhuanlan.zhihu.com/p/102023809)
*  阅读[raft理论与实践[2]-lab2a](https://zhuanlan.zhihu.com/p/102948740)
*  阅读[raft理论与实践[3]-lab2a讲解](https://zhuanlan.zhihu.com/p/103223270)
*  阅读[raft理论与实践[4]-lab2b日志复制](https://zhuanlan.zhihu.com/p/103386687)
*  阅读[raft理论与实践[5]-lab2c日志复制](https://zhuanlan.zhihu.com/p/103473049)
*  阅读[模拟RPC远程过程调用](https://dreamerjonson.com/2019/12/25/golang-109-lab-simulate-rpc/)
