// Shared helpers for IP Timestamp option modes (-o and -o1).

package clockdiff

import (
	"encoding/binary"
	"errors"
	"net"
	"syscall"

	"golang.org/x/net/icmp"
)

const (
	ipOptTimestamp = 68 // IPOPT_TIMESTAMP / IPOPT_TS (RFC 791)
	ipOptTSPrespec = 3  // IPOPT_TS_PRESPEC
)

type ipTimestampSample struct {
	sendtime int64
	histime  int64
	histime1 int64
	recvtime int64
}

func buildIPTimestampOptions(local, remote net.IP, fourTerm bool) []byte {
	optLen := 4 + 3*8
	if fourTerm {
		optLen = 4 + 4*8
	}
	opts := make([]byte, optLen)
	opts[0] = ipOptTimestamp
	opts[1] = byte(optLen)
	opts[2] = 5
	opts[3] = ipOptTSPrespec

	local4 := local.To4()
	remote4 := remote.To4()
	copy(opts[4:8], local4)
	copy(opts[12:16], remote4)
	if fourTerm {
		copy(opts[20:24], remote4)
		copy(opts[28:32], local4)
	} else {
		copy(opts[20:24], local4)
	}
	return opts
}

func parseIPTimestampOption(opt []byte, fourTerm bool) (ipTimestampSample, error) {
	var sample ipTimestampSample
	if len(opt) < 4 || opt[0] != ipOptTimestamp {
		return sample, errors.New("missing IP timestamp option")
	}
	if opt[3]&0x0f != ipOptTSPrespec {
		return sample, ErrNonStdTime
	}

	n := int(opt[2]-5) / 8
	if n <= 0 {
		return sample, errors.New("no IP timestamp samples")
	}

	for i := 0; i < n; i++ {
		off := 4 + i*8
		if off+8 > len(opt) {
			break
		}
		t := int64(binary.BigEndian.Uint32(opt[off+4 : off+8]))
		if t&0x80000000 != 0 {
			return sample, ErrNonStdTime
		}
		switch i {
		case 0:
			sample.sendtime = t
		case 1:
			sample.histime = t
			sample.histime1 = t
		case 2:
			if fourTerm {
				sample.histime1 = t
			} else {
				sample.recvtime = t
			}
		case 3:
			sample.recvtime = t
		}
	}

	if sample.sendtime == 0 || sample.histime == 0 || sample.histime1 == 0 || sample.recvtime == 0 {
		return sample, errors.New("incomplete IP timestamp option")
	}
	return sample, nil
}

func setIPOptions(conn *net.IPConn, opts []byte) error {
	sc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var sysErr error
	err = sc.Control(func(fd uintptr) {
		sysErr = syscall.SetsockoptString(int(fd), syscall.IPPROTO_IP, syscall.IP_OPTIONS, string(opts))
	})
	if err != nil {
		return err
	}
	return sysErr
}

func parseIPv4ICMP(buf []byte) (opts []byte, msg *icmp.Message, err error) {
	if len(buf) < 20 {
		return nil, nil, errors.New("short IPv4 packet")
	}
	if buf[0]>>4 != 4 {
		return nil, nil, errors.New("not IPv4")
	}
	ihl := int(buf[0]&0x0f) * 4
	if len(buf) < ihl+8 {
		return nil, nil, errors.New("short IPv4 header")
	}
	if ihl > 20 {
		opts = buf[20:ihl]
	}
	msg, err = icmp.ParseMessage(protocolICMP, buf[ihl:])
	if err != nil {
		return nil, nil, err
	}
	return opts, msg, nil
}
