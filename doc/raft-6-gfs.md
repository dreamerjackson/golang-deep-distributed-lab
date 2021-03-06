# 6.824分布式系统[2]-GFS案例学习

## 准备工作
阅读：[GFS论文](https://dreamerjonson.com/2020/01/21/distributed-systerm-2-GFS/)

## 背景
* GFS是Google在2003年发出的经典论文，其作为分布式文件系统,实际应用在Google的MapReduce框架实现中,作为原始数据和最终结果存储的基础服务。
* 为其他上层基础系统比如BigTable提供服务,Hadoop中的HDFS就是其开源实现。
* 这篇文章讨论了诸如一致性、容错、网络性能等分布式系统工程中的经典问题，启发了后续很多分布式文件系统的发展。

## 为什么阅读GFS的论文
* GFS使用了map/reduce的架构
* 好的性能 & 良好的并行I/O性能
* 其是一篇好的系统论文:从apps到网络都有细节说明
* 包含分布式的许多概念：performance, fault-tolerance, consistency
* 使用广泛（Bigtable、Spanner、Hadoop）

## 一致性是什么
* 一致性是保证分布式系统正确的条件。在有多个副本，并且并发下变得尤其困难。
* 一致性是分布式系统中最关键的问题，基本所有分布式系统都必须讨论这个问题。
* 考虑如果在一个程序中写，但是在另一个程序的副本中读取的情况。
    + 强的一致性会保证在另一个程序中读取到的一定是发生在最后一次写入之后。
    + 弱的一致性无法对其进行保证，可能会读取到过时的数据。
* 强一致性可以保证读到最新的写信息，但是对性能肯定会造成影响，好的系统设计就是在这两点中进行平衡。

## 理想的一致性模型
分布式文件系统需要达成的“理想的“一致性就是多节点上面操作文件表现处理单机跟本地文件系统一样。
* 如果两个应用程序同时写入同一文件怎么办？
    + 在单机，数据常常有一些混合的内容。即两个程序的处理可能是交错的。
* 如果两个应用程序同时写入同一目录怎么办
    + 在单机，使用了锁。一个应用程序处理完毕后，再处理第二个。
* 挑战
    + 多磁盘
    + 机器故障，操作无法完成。
    + 网络分区，可能不能够到达所有的机器和磁盘。
* 挑战难以解决的原因
    + 需要客户端和服务器之间的通信
    + 协议可能会变得复杂

## 不同的模型一致性考虑不同的权衡
    + 可串行性(serializability)
    + 顺序一致性(sequential consistency)
    + 线性一致性(linearizability)
    + 单项一致性模型(entry consistency)
    + 松散一致性(release consistency)

## GFS的目标
* GFS中节点失效就是常见的（每天1000台机器中大约3台失效）
* 高性能：大量
* 有效的使用网络

## 设计
* master存储（directories, files, names）
* 存储64MB的块，每个块就作为一个linux文件。
* 每个块在三台服务器上面做备份（保证可用性，负载均衡）
* 块为什么这么大（摊销间接费用、减少master中的状态大小）
* master掌握目录的层次结构
    + 文件夹中有哪些文件
    + 对于文件，维护信息:哪些节点存储了此文件。
    + master在内存中维护状态（每个块的64字节元数据）
    + 主数据库具有用于元数据的私有、可恢复数据库
        + 操作日志刷新到磁盘
        + 检查点
        + master快速恢复
    + shadow masters略微落后于master

## 客户端读
* 发送文件名和块索引给master
* master回复具有该块的服务器集
    + 回复版本号
    + client缓存信息
* 请求块服务器
    + 版本号（如果版本号错误，重新连接master）

## primary
primary是一个副本节点中比较高级的节点。

## 修改现有文件
* client请求master 块的位置 和primary的位置
* master回复块服务器，版本以及primary的位置
* client根据网络拓扑计算副本链
* 客户端将数据发送到第一个副本节点，然后此副本节点转发给其他人
* 副本节点会ack，表明数据已被接收
* client 告诉primary写入数据
* primary分配序列号并写入
    + primary 告诉其他副本写入
    + 全部成功后，回复client
* 如果另一个客户端并发在同一位置写入数据，该怎么办？
    + c1 与c2 可能会交替的写入，结果是一致性的，但是不能保证的。

## 添加文件
* client 请求master 块的位置。
* client将数据发送给副本，但是没有指定偏移量。
* 当数据在所有的块服务器上后，client联系primary
* primary分配序列号
* primary检查是否能够刚好添加到一个块中
* primary为添加选择偏移量，进行添加
* 转发请求到其他副本
* 失败后进行重试
    + 不能使用下一个可用偏移量，因为在失败的节点中可能会有空字节。
* GFS支持原子操作，保证至少一次添加，主Chunk服务器选择记录需要添加到的文件位置，然后发送给其他副本。
* 如果和一个副本的联系失败，那么主Chunk服务器会告诉客户端重试，如果重试成功，有些副本会出现追加两次的情况(因为这个副本追加成功两次)。
* 当GFS要去填塞chunk的边缘时，如果追加操作跨越chunk的边缘，那么文件也可能存在空洞。

## 失败情况
块服务器的失败会引起client重试。
master失败会导致GFS不可用，shadow master会服务只读的状态。可能会返回过时的数据。

## 总结
性能，容错，一致性（performance, fault-tolerance, consistency）的案例研究
* 优势
    + 大量的顺序读取和写入
    + 追加文件高效
    + 大的吞吐量
    + 数据容错（3个副本）
* 劣势
    + master服务器的容错
    + 小文件（master服务器的瓶颈）
    + client会看到过时的数据（弱的一致性）
    + 追加可能重复

## GFS案例问答

###  为什么添加数据执行至少一次"at-least-once"语义，而不是精准的一次？
实现困难，primary需要保留重复的状态。状态必须在服务器之间复制，以便如果primary出现故障, 此信息不会丢失。

### 应用程序如何知道块的哪些是有数据的块，哪些是重复的数据
可以在有效记录的开头做标识（magic number）就可以知道有数据的块。
检测重复的块可以为每个记录都有一个特殊的UID标识。

### 论文中提到的reference counts是什么意思
* 他们是实现快照copy-on-write（写时复制）的一部分。
* 当GFS创建快照时，它不复制块，而是增加每个块的引用计数器。这使得创建快照的成本不高。 如果客户端写入了一个块，并且主服务器注意到引用计数大于1，此时，主服务器会首先创建一个副本，以便客户端可以更新副本（而不是快照的一部分）。

## 参考资料
* [my blog](https://dreamerjonson.com/2020/01/21/distributed-systerm-2-GFS/)

## 技术交流群
    * QQ群：713385260