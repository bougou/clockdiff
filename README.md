# clockdiff

A Go library for measuring clock skew between your machine and a remote IPv4 host using **ICMP TIMESTAMP** messages per [RFC 792](https://www.rfc-editor.org/rfc/rfc792). The algorithm matches Linux [iputils](https://github.com/iputils/iputils) [`clockdiff(8)`](https://linux.die.net/man/8/clockdiff).

[中文 README](README.zh-CN.md)

## Purpose

`clockdiff` answers a concrete, practical question:

> How much faster (or slower) is the remote machine's clock than mine?

Unlike NTP, it does **not** synchronize time. It sends a short burst of ICMP probes and returns a millisecond-level offset estimate. Useful for:

- Quickly checking clock skew between servers
- Troubleshooting time issues when NTP is unavailable or untrusted
- Learning how ICMP timestamp-based clock comparison works

A **positive** `Result.Delta` means the remote clock is **ahead of** the local clock.

## Quick start

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

`Measure` requires a **raw ICMP socket** (`ip4:icmp`). On Linux you typically need **root** or `CAP_NET_RAW`; on macOS run with `sudo`.

## API

### `Measure(dst string, opts ...Option) (*Result, error)`

Resolves `dst` (hostname or IPv4 address), sends probes, and returns aggregated statistics.

| Field       | Meaning                                                              |
| ----------- | -------------------------------------------------------------------- |
| `Delta`     | Primary clock offset estimate: `(min1 - min2) / 2`                   |
| `DeltaBest` | Offset estimate from the lowest-RTT sample                           |
| `RTT`       | Smoothed round-trip time estimate                                    |
| `RTTSigma`  | Smoothed RTT variation                                               |
| `MinRTT`    | Smallest observed `delta1 + delta2` (proxy for the best RTT sample) |

### Options

| Option            | Default        | Description                                                                                        |
| ----------------- | -------------- | -------------------------------------------------------------------------------------------------- |
| `WithMessages(n)` | `50`           | Number of valid replies to collect                                                                 |
| `WithTrials(n)`   | `10`           | Consecutive unanswered probes before declaring the host unreachable                                  |
| `WithMode(m)`     | ICMP Timestamp | Probe type: `ModeICMPTimestamp`, `ModeIPTimestamp` (`-o`), `ModeIPTimestamp3` (`-o1`)              |
| `WithOnReply(fn)` | none           | Callback after each valid reply (e.g. print a progress dot)                                        |

### Probe send behavior

`Measure` has **no fixed inter-packet interval**. Probes are sent **serially, as fast as possible**:

1. Send one probe;
2. Wait for a matching reply (adaptive read timeout);
3. After a valid reply or timeout, **immediately** send the next.

There is no `sleep` between probes; the effective rate is roughly **1 / RTT** packets per second, depending on network latency.

| Parameter          | Default                      | Meaning                                                                                                                                              |
| ------------------ | ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Messages`         | `50`                         | Stop after collecting this many **valid replies**. Timeouts or unmatched packets **do not** count, but each send still increments the sequence number. |
| `Trials`           | `10`                         | Return `ErrHostDown` when `seq - acked > Trials` (this many consecutive sends with no matching reply).                                               |
| Initial RTT guess  | `1000` ms                    | Used before the first reply arrives.                                                                                                                 |
| Read timeout       | `max(rtt + rtt_sigma, 1)` ms | Timeout for each read; `rtt` and `rtt_sigma` are smoothed from observed round-trip times.                                                            |
| `rangeMS`          | `1` ms                       | When a probe's RTT falls below this threshold, stop waiting for more replies to that probe and move on. The full measurement still requires `Messages` valid samples. |

In the worst case (no replies at all), `Measure` sends at most `Trials` probes before reporting the host as down. Normally (with replies), it keeps sending until `Messages` valid samples are collected—typically at least 50 sends, more if there are timeouts.

This behavior matches [iputils `clockdiff.c`](https://github.com/iputils/iputils/blob/master/clockdiff.c) for all three modes (ICMP Timestamp, `-o`, `-o1`).

### Errors

| Error                | When it occurs                              |
| -------------------- | ------------------------------------------- |
| `ErrHostDown`        | Too many consecutive probes without a match |
| `ErrHostUnreachable` | ICMP probe could not be sent                |
| `ErrNonStdTime`      | Remote timestamp has high bits set (non-RFC 792 format) |

## How it works

### ICMP TIMESTAMP (RFC 792)

The default mode uses ICMP type **13** (Timestamp Request) and **14** (Timestamp Reply). Message body layout:

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

All three timestamp fields are **milliseconds since UTC midnight**, per RFC 792.

Request path:

1. Local host records send time and fills `Originate`;
2. Remote IP stack fills `Receive` on receipt and echoes `Originate` in the reply;
3. Local host records receive time when the reply arrives.

### Offset calculation

For each valid reply, using millisecond timestamps:

```
sendtime  = Originate in reply (remote echo of local send time)
histime   = Receive in reply (remote receive time)
recvtime  = local time when reply was received

delta1 = histime - sendtime   // outbound clock contribution
delta2 = recvtime - histime   // return clock contribution
```

Wraparound correction handles midnight crossings so deltas stay within ±12 hours (see caveats below).

The implementation sends multiple probes (50 valid samples by default), tracks the smallest `delta1` and `delta2` across all samples, and estimates:

```
Delta = (min1 - min2) / 2
```

The measurement strategy follows iputils `clockdiff.c`: prefer low-RTT samples, adapt read timeout to observed RTT, and stop waiting early when a single probe's RTT is below 1 ms.

### Measurement flow

```
 Local                                    Remote
   |                                        |
   |  ICMP Timestamp Request (type 13)      |
   |  Originate = local ms since UTC midnight |
   |--------------------------------------->|
   |                          Receive = remote time
   |                                        |
   |  ICMP Timestamp Reply (type 14)        |
   |  Originate echoed, Receive filled      |
   |<---------------------------------------|
   |                                        |
 Local records recvtime
 Computes delta1, delta2, updates minimums
```

## Limitations and caveats

### Privileges

A raw ICMP socket is required. Unprivileged `udp4` ICMP (used by some ping implementations) only supports Echo Request/Reply and does **not** support Timestamp messages. Without sufficient privileges you get `operation not permitted`.

### IPv4 only

Only IPv4 ICMP TIMESTAMP is implemented; IPv6 is not supported.

### Remote ICMP TIMESTAMP support

Many firewalls and hosts drop or do not implement ICMP Timestamp. Public DNS, routers, and cloud load balancers often do not respond. Timeouts or `ErrHostDown` do not necessarily mean the host is down—it may simply not support this ICMP type.

The original Linux `clockdiff` also offers `-o` / `-o1` modes (IP Timestamp option + ICMP Echo) for hosts that do not support ICMP Timestamp. **This package implements all three modes via `WithMode`.**

### Measurement modes

| Mode                | CLI flag | Mechanism                                   |
| ------------------- | -------- | ------------------------------------------- |
| `ModeICMPTimestamp` | (default) | ICMP Timestamp request/reply (RFC 792)      |
| `ModeIPTimestamp`   | `-o`     | Four-part IP Timestamp option + ICMP Echo   |
| `ModeIPTimestamp3`  | `-o1`    | Three-part IP Timestamp option + ICMP Echo  |

IP Timestamp modes require `setsockopt(IP_OPTIONS)` and are useful when the target drops ICMP Timestamp but responds to ICMP Echo with IP options.

### 24-day modulo

ICMP timestamps are 32-bit "milliseconds since UTC midnight". Comparisons are effectively modulo one day (the man page notes clockdiff displays differences modulo **24 days**). Very large true offsets can be misread.

### ±12 hour assumption

Wraparound correction assumes the two clocks differ by at most **12 hours**. Larger real skew yields wrong results.

### Accuracy

Results are affected by:

- Network asymmetry (unequal outbound and return delay)
- OS scheduling and interrupt latency
- Non-standard remote clock implementations (some devices set high timestamp bits; some systems have noisy timestamps under NTP)

For production time synchronization, use **NTP** or **PTP**, not `clockdiff`.

### Security

Generating raw ICMP traffic may be restricted by host policy; some networks block ICMP entirely. Use only on authorized systems and networks.

## Examples

The CLI lives in `cmd/clockdiff/`:

```bash
# Measure clock offset (sudo required on macOS/Linux)
sudo go run ./cmd/clockdiff/ 8.8.8.8

# Interactive output with ISO-8601 time format
sudo go run ./cmd/clockdiff/ -I 8.8.8.8

# For hosts that do not support ICMP Timestamp, try IP Timestamp modes
sudo go run ./cmd/clockdiff/ -o 192.0.2.1
sudo go run ./cmd/clockdiff/ -o1 192.0.2.1

# Machine-readable output when stdout is not a tty
sudo go run ./cmd/clockdiff/ 8.8.8.8 | cat

# Help and version
go run ./cmd/clockdiff/ -h
go run ./cmd/clockdiff/ -V
```

Command-line flags follow [clockdiff(8)](https://linux.die.net/man/8/clockdiff). In interactive mode, a `.` is printed to stdout for each valid reply.

## Tests

Unit tests (no special privileges required):

```bash
go test .
```

## References

- [RFC 792 – ICMP](https://www.rfc-editor.org/rfc/rfc792) (Timestamp messages, page 16)
- [clockdiff(8) man page](https://linux.die.net/man/8/clockdiff)
- [iputils clockdiff.c](https://github.com/iputils/iputils/blob/master/clockdiff.c)

## Related tools

| Tool                | Method                                      | Typical use                    |
| ------------------- | ------------------------------------------- | ------------------------------ |
| `clockdiff` (this package) | ICMP TIMESTAMP, IP TIMESTAMP (`-o`/`-o1`) | Quick offset estimate, no sync |
| `ntpdate` / NTP     | NTP protocol                                | High-precision time sync       |
| `ping`              | ICMP Echo                                   | Reachability and RTT, not skew |
