# clockdiff

Go 语言实现的时钟差测量库，通过 **ICMP TIMESTAMP** 报文估算本机与远端 IPv4 主机之间的时钟偏移，遵循 [RFC 792](https://www.rfc-editor.org/rfc/rfc792)，算法与 Linux [iputils](https://github.com/iputils/iputils) 中的 [`clockdiff(8)`](https://linux.die.net/man/8/clockdiff) 一致。

[English README](README.md)

## 用途

`clockdiff` 回答一个具体而实用的问题：

> 远端机器的时钟比我的快多少（或慢多少）？

与 NTP 不同，它**不会**同步时间，而是发送一小段 ICMP 探测并返回毫秒级偏移估计。适用于：

- 快速检查服务器之间的时钟偏差
- 在 NTP 不可用或不可信时排查时间问题
- 学习基于 ICMP 时间戳的时钟比较原理

`Result.Delta` 为**正**表示远端时钟**快于**本机。

## 快速开始

```go
package main

import (
	"fmt"
	"log"

	"github.com/bougou/clockdiff"
)

func main() {
	result, err := clockdiff.Measure("192.0.2.1",
		clockdiff.WithMessages(50),
		clockdiff.WithTrials(10),
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("host=%s delta=%v rtt=%v min_rtt=%v\n",
		result.Host, result.Delta, result.RTT, result.MinRTT)
}
```

`Measure` 需要**原始 ICMP socket**（`ip4:icmp`）。在 Linux 上通常需要 **root** 或 `CAP_NET_RAW`；在 macOS 上请使用 `sudo` 运行。

## API

### `Measure(dst string, opts ...Option) (*Result, error)`

解析 `dst`（主机名或 IPv4 地址），发送探测并返回聚合统计结果。

| 字段        | 含义                                                      |
| ----------- | --------------------------------------------------------- |
| `Delta`     | 主时钟偏移估计：`(min1 - min2) / 2`                       |
| `DeltaBest` | 来自 RTT 最低样本的偏移估计                               |
| `RTT`       | 平滑后的往返时延估计                                      |
| `RTTSigma`  | 平滑后的 RTT 波动                                         |
| `MinRTT`    | 观测到的最小 `delta1 + delta2`（最佳 RTT 样本的代理指标） |

### 选项

| 选项              | 默认值         | 说明                                                                                  |
| ----------------- | -------------- | ------------------------------------------------------------------------------------- |
| `WithMessages(n)` | `50`           | 需要收集的有效回复数量                                                                |
| `WithTrials(n)`   | `10`           | 连续无回复多少次后判定主机不可达                                                      |
| `WithMode(m)`     | ICMP Timestamp | 探测类型：`ModeICMPTimestamp`、`ModeIPTimestamp`（`-o`）、`ModeIPTimestamp3`（`-o1`） |
| `WithOnReply(fn)` | 无             | 每次收到有效回复后的回调（例如打印进度点）                                            |

### 探测发送行为

`Measure` **没有固定的发包间隔**。探测按**串行、尽快**的方式发送：

1. 发送一个探测包；
2. 等待匹配的回复（自适应读超时）；
3. 收到有效回复或超时后，**立即**发送下一个。

探测之间没有 `sleep`；实际速率约为 **1 / RTT** 包/秒，取决于网络时延。

| 参数          | 默认值                       | 含义                                                                                                                |
| ------------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `Messages`    | `50`                         | 收集到这么多**有效回复**后结束。超时或未匹配的包**不计入**此数量，但每次发送仍会递增序列号。                        |
| `Trials`      | `10`                         | 当 `seq - acked > Trials`（连续这么多次发送都没有匹配回复）时，返回 `ErrHostDown`。                                 |
| 初始 RTT 估计 | `1000` ms                    | 在收到第一个回复之前使用。                                                                                          |
| 读超时        | `max(rtt + rtt_sigma, 1)` ms | 每次读操作的超时；`rtt` 与 `rtt_sigma` 由观测到的往返时延平滑更新。                                                 |
| `rangeMS`     | `1` ms                       | 当某次探测的 RTT 低于此阈值时，停止等待该探测的更多回复，继续下一次发送。完整测量仍须收集满 `Messages` 个有效样本。 |

最坏情况下（完全没有回复），`Measure` 最多发送 `Trials` 次后报告主机不可达。正常情况下（有回复），会持续发送直到收集满 `Messages` 个有效样本——通常至少发送 50 次，若有超时则会更多。

此行为与 [iputils `clockdiff.c`](https://github.com/iputils/iputils/blob/master/clockdiff.c) 一致，三种模式（ICMP Timestamp、`-o`、`-o1`）均适用。

### 错误

| 错误                 | 触发条件                                |
| -------------------- | --------------------------------------- |
| `ErrHostDown`        | 连续多次探测无匹配回复                  |
| `ErrHostUnreachable` | ICMP 探测无法发出                       |
| `ErrNonStdTime`      | 远端时间戳高位被置位（非 RFC 792 格式） |

## 工作原理

### ICMP TIMESTAMP (RFC 792)

默认模式使用 ICMP 类型 **13**（Timestamp Request）和 **14**（Timestamp Reply）。报文体结构：

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|         Identifier            |       Sequence Number         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Originate Timestamp                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        Receive Timestamp                      |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       Transmit Timestamp                      |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

三个时间戳字段均为 **UTC 午夜以来的毫秒数**，符合 RFC 792。

请求路径：

1. 本机记录发送时间，填入 `Originate`；
2. 远端 IP 栈收到请求时填写 `Receive`，并在回复中回显 `Originate`；
3. 本机收到回复时记录接收时间。

### 偏移计算

对每个有效回复，使用毫秒时间戳：

```
sendtime  = 回复中的 Originate（远端回显的本机发送时间）
histime   = 回复中的 Receive（远端接收时间）
recvtime  = 本机收到回复的时间

delta1 = histime - sendtime   // 去程时钟贡献
delta2 = recvtime - histime   // 回程时钟贡献
```

对跨午夜的情况做环绕修正，使 delta 保持在 ±12 小时以内（见下文注意事项）。

实现会发送多个探测（默认 50 个有效样本），跟踪所有样本中最小的 `delta1` 和 `delta2`，最终估计为：

```
Delta = (min1 - min2) / 2
```

测量策略遵循 iputils `clockdiff.c`：偏好低 RTT 样本、根据观测 RTT 自适应读超时、单次探测 RTT 低于 1 ms 时提前结束该轮等待。

### 测量流程

```
 本机                                    远端
   |                                       |
   |  ICMP Timestamp Request (type 13)     |
   |  Originate = 本机 UTC 午夜以来毫秒数   |
   |-------------------------------------->|
   |                         Receive = 远端时间
   |                                       |
   |  ICMP Timestamp Reply (type 14)       |
   |  Originate 回显, Receive 已填写        |
   |<--------------------------------------|
   |                                       |
 本机记录 recvtime
 计算 delta1、delta2，更新最小值
```

## 限制与注意事项

### 权限

必须使用原始 ICMP socket。无特权的 `udp4` ICMP（部分 ping 实现所用）仅支持 Echo Request/Reply，**不支持** Timestamp 报文。权限不足时会得到 `operation not permitted`。

### 仅支持 IPv4

仅实现 IPv4 ICMP TIMESTAMP，不支持 IPv6。

### 目标主机的 ICMP TIMESTAMP 支持

许多防火墙和主机会丢弃或不实现 ICMP Timestamp。公共 DNS、路由器、云负载均衡器通常不响应。超时或 `ErrHostDown` 不一定表示主机宕机，可能只是不支持此 ICMP 类型。

Linux 原版 `clockdiff` 还提供 `-o` / `-o1` 模式（IP Timestamp 选项 + ICMP Echo），用于不支持 ICMP Timestamp 的主机。**本包通过 `WithMode` 实现了全部三种模式。**

### 测量模式

| 模式                | CLI 标志 | 机制                                |
| ------------------- | -------- | ----------------------------------- |
| `ModeICMPTimestamp` | （默认） | ICMP Timestamp 请求/回复（RFC 792） |
| `ModeIPTimestamp`   | `-o`     | 四段 IP Timestamp 选项 + ICMP Echo  |
| `ModeIPTimestamp3`  | `-o1`    | 三段 IP Timestamp 选项 + ICMP Echo  |

IP Timestamp 模式需要 `setsockopt(IP_OPTIONS)`，适用于目标丢弃 ICMP Timestamp 但对带 IP 选项的 ICMP Echo 有响应的场景。

### 24 天取模

ICMP 时间戳为 32 位「UTC 午夜以来毫秒数」。比较实际上按一天取模（手册页注明 clockdiff 显示的差值按 **24 天** 取模）。真实偏移很大时可能读错。

### ±12 小时假设

环绕修正假设两台时钟相差不超过 **12 小时**。真实偏差更大时结果错误。

### 精度

结果受以下因素影响：

- 网络不对称（去程与回程时延不同）
- 操作系统调度与中断延迟
- 远端非标准时钟实现（部分设备置时间戳高位；部分系统在 NTP 下时间戳噪声较大）

生产环境时间同步请使用 **NTP** 或 **PTP**，不要用 `clockdiff`。

### 安全

生成原始 ICMP 流量可能受主机策略限制，部分网络完全屏蔽 ICMP。仅在授权的系统与网络上使用。

## 示例

CLI 位于 `cmd/clockdiff/`：

```bash
# 测量时钟偏移（macOS/Linux 需要 sudo）
sudo go run ./cmd/clockdiff/ 8.8.8.8

# 交互式输出使用 ISO-8601 时间格式
sudo go run ./cmd/clockdiff/ -I 8.8.8.8

# 不支持 ICMP Timestamp 的主机可尝试 IP Timestamp 模式
sudo go run ./cmd/clockdiff/ -o 192.0.2.1
sudo go run ./cmd/clockdiff/ -o1 192.0.2.1

# stdout 非 tty 时输出机器可读格式
sudo go run ./cmd/clockdiff/ 8.8.8.8 | cat

# 帮助与版本
go run ./cmd/clockdiff/ -h
go run ./cmd/clockdiff/ -V
```

命令行参数遵循 [clockdiff(8)](https://linux.die.net/man/8/clockdiff)。交互模式下每收到一个有效回复会在 stdout 打印一个 `.`。

## 测试

单元测试（无需特殊权限）：

```bash
go test .
```

## 参考

- [RFC 792 – ICMP](https://www.rfc-editor.org/rfc/rfc792)（Timestamp 报文，第 16 页）
- [clockdiff(8) 手册页](https://linux.die.net/man/8/clockdiff)
- [iputils clockdiff.c](https://github.com/iputils/iputils/blob/master/clockdiff.c)

## 查询 NTP 服务器时间（[beevik/ntp](https://github.com/beevik/ntp)）

若需要从**专用 NTP 服务器**（而非任意主机）获取时间，可使用 [beevik/ntp](https://github.com/beevik/ntp) —— 基于 [RFC 5905](https://www.rfc-editor.org/rfc/rfc5905) 的 Go SNTP 客户端。

```go
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/beevik/ntp"
)

func main() {
	// 最简单：直接得到服务器时间
	serverTime, err := ntp.Time("pool.ntp.org")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("NTP server time:", serverTime)

	// 完整同步数据
	resp, err := ntp.Query("pool.ntp.org")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("clock offset: %v  RTT: %v  stratum: %d\n",
		resp.ClockOffset, resp.RTT, resp.Stratum)

	// 将偏移量应用到本机读数
	corrected := time.Now().Add(resp.ClockOffset)
	fmt.Println("corrected local time:", corrected)
}
```

`Query` 返回包含 `ClockOffset`、`RTT`、`Stratum`、`RootDelay` 等字段的 `Response`。可调用 `resp.Validate()` 判断响应是否适合用于时间同步。无需原始 socket 或 root 权限，只需通过 UDP 连接 123 端口。

安装：

```bash
go get github.com/beevik/ntp
```

## clockdiff 与 [beevik/ntp](https://github.com/beevik/ntp) 的区别

两个库都能估算本机时钟与远端时钟的偏差，但面向的场景不同：

| 维度         | clockdiff（本包）                                                                        | [beevik/ntp](https://github.com/beevik/ntp)                      |
| ------------ | ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| **协议**     | ICMP TIMESTAMP / IP Timestamp + Echo（[RFC 792](https://www.rfc-editor.org/rfc/rfc792)） | NTP / SNTP（[RFC 5905](https://www.rfc-editor.org/rfc/rfc5905)） |
| **目标**     | 任意可达的 **IPv4 主机**（服务器、路由器、DNS 等）                                       | 仅 **NTP 服务器**（如 `pool.ntp.org`）                           |
| **用途**     | 快速一次性**偏移估计**，不同步时间                                                       | 获取时钟偏移与服务器时间，用于**时间同步**                       |
| **权限**     | 原始 ICMP socket — Linux 需 root 或 `CAP_NET_RAW`                                        | 普通 UDP — 无需特殊权限                                          |
| **传输层**   | ICMP（类型 13/14）或带 IP 选项的 ICMP Echo                                               | UDP 123 端口                                                     |
| **精度**     | 毫秒级；受网络不对称影响较大                                                             | 亚毫秒级；NTP 四时间戳算法                                       |
| **可用性**   | 许多防火墙和主机会丢弃 ICMP Timestamp                                                    | NTP 服务器专门用于应答查询                                       |
| **IPv6**     | 不支持                                                                                   | 取决于解析器与服务器                                             |
| **附加信息** | RTT、最小 RTT、平滑 RTT 波动                                                             | 层级（stratum）、根延迟/离散度、闰秒、kiss-of-death 等           |

**适合用 clockdiff 的场景：** 你想知道*某台具体机器*（如同机房另一台服务器、网关、未跑 NTP 的主机）的时钟与本机相差多少，且 ICMP（或 IP Timestamp echo）可用。

**适合用 beevik/ntp 的场景：** 你需要 NTP 池或 stratum 服务器的权威时间，或正在构建需要根据 NTP 偏移/RTT 调整、监控本机时钟的应用。

生产环境时间同步请优先使用 NTP（如 [beevik/ntp](https://github.com/beevik/ntp) 或 `chrony`/`ntpd`），而非基于 ICMP 的 clockdiff。

## 相关工具

| 工具                                        | 方法                                       | 典型用途                  |
| ------------------------------------------- | ------------------------------------------ | ------------------------- |
| `clockdiff`（本包）                         | ICMP TIMESTAMP、IP TIMESTAMP（`-o`/`-o1`） | 快速偏移估计，不同步      |
| [beevik/ntp](https://github.com/beevik/ntp) | NTP / SNTP（UDP 123）                      | 查询 NTP 服务器、同步数据 |
| `ntpdate` / NTP                             | NTP 协议                                   | 高精度时间同步            |
| `ping`                                      | ICMP Echo                                  | 可达性与 RTT，非时钟偏移  |
