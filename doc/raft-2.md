
## 准备工作
*  1、阅读[raft论文](http://nil.csail.mit.edu/6.824/2017/papers/raft-extended.pdf)
*  2、阅读我写的[raft理论与实践[1]-理论篇](https://dreamerjonson.com/2019/12/29/golang-110-lab-raft/)
*  3、由于我们需要模拟rpc远程调用， 因此需要查看我写的这篇文章： [模拟RPC远程过程调用](https://dreamerjonson.com/2019/12/25/golang-109-lab-simulate-rpc/)
*  4、实验开始，我们首先需要拉取代码：
```
git clone git@github.com:dreamerjackson/golang-deep-distributed-lab.git
git checkout --hard   4e6446c
```

## 实验说明
* 此代码中labrpc 与 labgob 为模拟rpc的 package。
* raft文件夹为此实验用到的代码框架。 在其中已经写好了一部分代码，还需要我们通过实验来完善它。
* 在本实验中，我们只需要关注raft.go文件，并实现选举逻辑和心跳检测逻辑。
* 本实验的目标是要保证，唯一的leader能够被选举。
* 当leader被选举后，如果没有任何失败，其将会保持leader。
* 当leader被选举后，如果leader奔溃或者to/from leader 的网络包丢失，则新的leader将会产生。
* 要验证代码的正确性，运行go test -run 2A

## 实验提示
* 1、raft.go 的raft结构体 补充字段。 字段应该尽量与raft论文的Figure2接近。
* 2、填充RequestVoteArgs和RequestVoteReply结构。 修改Make()以创建一个后台goroutine，该后台goroutine将在有一段时间没有收到其他节点的请求时通过发出RequestVote RPC来定期启动领导者选举。 这样，节点将了解谁是leader（如果已经有leader），或者成为leader本身。 实现RequestVote（）RPC处理程序，以便节点之间相互投票。
* 3、要实现心跳检测，请定义AppendEntries RPC结构（尽管您可能还不需要所有参数），并让leader定期调用其他节点此方法。 编写AppendEntries RPC方法，该方法将重置选举超时，以便在已经有leader时，阻止其他节点成为leader。
* 4、确保不同对等方的选举超时不会总是同时触发，否则所有节点都只会为自己投票，而没有人会成为领导者。
* 5、测试要求leader每秒发送心跳RPC的次数不得超过十次。
* 6、测试要求在leader失败后，也能够再5秒之内选出新的leader。 您必须选择足够短的选举超时时间（以及因此产生的心跳间隔），
 - 以使选举很有可能在不到五秒钟的时间内完成，即使需要进行多轮选举也是如此。
* 7、raft论文的5.2提到选举超时的范围是150到300毫秒，但是仅当领导者发送心跳的频率大大超过每150毫秒一次的频率时，此范围才有意义。
 - 由于测试要求您的心跳检测为每秒10个，因此您将必须使用大于150到300毫秒的选举超时时间，但不能太大，因为那样的话，您可能会在五秒钟内无法选举领导者。
* 8、使用go的rand方法产生随机数。
* 9、go的time.Timer 和 time.Ticker 很难使用正确。
* 10、要调试代码，可以将util.go 的debug设置为1.
* 11、您应该使用go test -race检查代码，并修复它报告的所有问题。

## 参考
[讲义](https://github.com/dreamerjackson/Distributed-Systems/blob/master/Lec05_Fault_Tolerance_Raft/l-raft.txt)
[讲义新](https://pdos.csail.mit.edu/6.824/notes/l-raft.txt)