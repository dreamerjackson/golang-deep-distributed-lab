# golang[114]-raft理论与实践[5]-lab2c-持久化
## 准备工作
1、阅读[raft论文](http://nil.csail.mit.edu/6.824/2017/papers/raft-extended.pdf)
2、阅读[raft理论与实践[1]-理论篇](https://dreamerjonson.com/2019/12/29/golang-110-lab-raft/)
3、阅读[raft理论与实践[2]-lab2a](https://dreamerjonson.com/2020/01/06/golang-111-raft-2/)
4、阅读[raft理论与实践[3]-lab2a讲解](https://dreamerjonson.com/2020/01/06/golang-111-raft-3-elect/)
5、阅读[raft理论与实践[4]-lab2b日志复制](https://dreamerjonson.com/2020/01/12/golang-113-raft-4-log/)
6、查看我写的这篇文章： [模拟RPC远程过程调用](https://dreamerjonson.com/2019/12/25/golang-109-lab-simulate-rpc/)

## 持久化
* 如果基于Raft的服务器重新启动，则应从中断的位置恢复服务。 这就要求Raft保持持久状态，使其在重启后仍然有效。
* 论文中Figure 2指出了那些字段需要持久化。
* 并且raft.go包含有关如何保存和恢复持久性状态的示例。

* 一个“真实的服务在每次Raft更改状态时将Raft的持久状态写入磁盘，并在重新启动时从磁盘读取最新的状态来恢复。
* 但是我们的实现将会采用一个结构体的方式来模拟实现persister.go。

* 调用Raft.Make（）会提供一个Persister，它持有Raft的最近持久状态。
* Raft应从该Persister初始化其状态，并在每次状态更改时修改其持久状态。
* 主要使用Persister的ReadRaftState（）和SaveRaftState（）方法。

* 在本实验中，我们需要完善在raft.go中的persist() and readPersist()方法。
* 需要使用到labgob包中的编码与解码函数。
* 你需要明确在什么时候需要持久化。


下面只列出两个重要实现，其他不再赘述，留给读者自己实现。
## 持久化编码
```go
func (rf *Raft) persist() {
	// Your code here (2C).
	w := new(bytes.Buffer)

	e:= labgob.NewEncoder(w)
	e.Encode(rf.CurrentTerm)
	e.Encode(rf.VotedFor)
	e.Encode(rf.Logs)

	data := w.Bytes()
	rf.persister.SaveRaftState(data)
}
```

## 持久化解码
```
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	// Your code here (2C).
	r := bytes.NewBuffer(data)

	d:= labgob.NewDecoder(r)
	d.Decode(&rf.CurrentTerm)
	d.Decode(&rf.VotedFor)
	d.Decode(&rf.Logs)
}

```
## 测试
```
> go test -v -run=2C
```

## 参考资料
https://github.com/dreamerjackson/golang-deep-distributed-lab