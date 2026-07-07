// Four-term IP Timestamp option with ICMP Echo (-o).

package clockdiff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func measureIPTimestamp(host string, ip net.IP, cfg Config) (*Result, error) {
	return measureIPOpts(host, ip, cfg, true)
}

func measureIPOpts(host string, ip net.IP, cfg Config, fourTerm bool) (*Result, error) {
	conn, err := net.DialIP("ip4:1", &net.IPAddr{IP: net.IPv4zero}, &net.IPAddr{IP: ip})
	if err != nil {
		return nil, fmt.Errorf("open icmp socket: %w", err)
	}
	defer conn.Close()

	localIP := conn.LocalAddr().(*net.IPAddr).IP.To4()
	if localIP == nil {
		return nil, errors.New("local IPv4 address required")
	}

	optBuf := buildIPTimestampOptions(localIP, ip, fourTerm)
	if err := setIPOptions(conn, optBuf); err != nil {
		return nil, fmt.Errorf("set IP options: %w", err)
	}

	buf := make([]byte, 1500)
	if err := drainIP(conn, buf); err != nil {
		return nil, err
	}

	id := os.Getpid() & 0xffff
	state := newMeasureState(id)

	for msgCount := 0; msgCount < cfg.Messages; {
		if err := state.hostDown(host, cfg.Trials); err != nil {
			return nil, err
		}

		state.seq++
		sendMS := msSinceMidnightUTC(time.Now())
		echoData := make([]byte, 12)
		binary.BigEndian.PutUint32(echoData[0:4], uint32(sendMS))

		wb, err := (&icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{
				ID:   id,
				Seq:  state.seq,
				Data: echoData,
			},
		}).Marshal(nil)
		if err != nil {
			return nil, err
		}

		if _, err := conn.Write(wb); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrHostUnreachable, host)
		}

		gotReply, err := state.collectIPOptReply(conn, buf, id, fourTerm, cfg.OnReply)
		if err != nil {
			return nil, err
		}
		if gotReply {
			msgCount++
		}
	}

	return state.result(host), nil
}

func drainIP(conn *net.IPConn, buf []byte) error {
	for {
		if err := conn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
			return err
		}
		_, err := conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return nil
			}
			return err
		}
	}
}

func (s *measureState) collectIPOptReply(conn *net.IPConn, buf []byte, id int, fourTerm bool, onReply func()) (bool, error) {
	gotReply := false
	for {
		timeout := time.Duration(max64(s.rtt+s.rttSigma, 1)) * time.Millisecond
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return gotReply, err
		}

		n, err := conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return gotReply, nil
			}
			return gotReply, err
		}
		if n <= 0 {
			continue
		}

		opts, m, err := parseIPv4ICMP(buf[:n])
		if err != nil {
			continue
		}
		if m.Type != ipv4.ICMPTypeEchoReply {
			continue
		}

		echo, ok := m.Body.(*icmp.Echo)
		if !ok || echo.ID != id || echo.Seq < 0 || echo.Seq > s.seq {
			continue
		}
		s.noteReply(echo.Seq)

		sample, err := parseIPTimestampOption(opts, fourTerm)
		if err != nil {
			if errors.Is(err, ErrNonStdTime) {
				return gotReply, err
			}
			continue
		}

		diff := sample.recvtime - sample.sendtime
		if diff < 0 {
			continue
		}

		s.updateRTT(diff)

		delta1 := normalizeDelta(sample.histime - sample.sendtime)
		delta2 := normalizeDelta(sample.recvtime - sample.histime1)
		s.updateDeltas(delta1, delta2)

		gotReply = true
		if onReply != nil {
			onReply()
		}
		if s.earlyExit(delta1, delta2, diff) {
			return true, nil
		}
	}
}
