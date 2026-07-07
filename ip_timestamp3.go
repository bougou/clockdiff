// Three-term IP Timestamp option with ICMP Echo (-o1).

package clockdiff

import "net"

func measureIPTimestamp3(host string, ip net.IP, cfg Config) (*Result, error) {
	return measureIPOpts(host, ip, cfg, false)
}
