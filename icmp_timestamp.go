// ICMP Timestamp request/reply measurement (RFC 792), the default clockdiff mode.

package clockdiff

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func measureICMPTimestamp(host string, ip net.IP, cfg Config) (*Result, error) {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("open icmp socket: %w", err)
	}
	defer conn.Close()

	p4 := conn.IPv4PacketConn()
	if p4 == nil {
		return nil, fmt.Errorf("ipv4 icmp connection required")
	}

	buf := make([]byte, 1500)
	if err := drainICMP(p4, buf); err != nil {
		return nil, err
	}

	id := os.Getpid() & 0xffff
	peer := &net.IPAddr{IP: ip}
	state := newMeasureState(id)

	for msgCount := 0; msgCount < cfg.Messages; {
		if err := state.hostDown(host, cfg.Trials); err != nil {
			return nil, err
		}

		state.seq++
		sendMS := msSinceMidnightUTC(time.Now())

		wb, err := (&icmp.Message{
			Type: ipv4.ICMPTypeTimestamp,
			Code: 0,
			Body: &Timestamp{
				ID:        id,
				Seq:       state.seq,
				Originate: uint32(sendMS),
			},
		}).Marshal(nil)
		if err != nil {
			return nil, err
		}

		if _, err := conn.WriteTo(wb, peer); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrHostUnreachable, host)
		}

		gotReply, err := state.collectICMPTimestampReply(p4, buf, id, cfg.OnReply)
		if err != nil {
			return nil, err
		}
		if gotReply {
			msgCount++
		}
	}

	return state.result(host), nil
}

func (s *measureState) collectICMPTimestampReply(p4 *ipv4.PacketConn, buf []byte, id int, onReply func()) (bool, error) {
	gotReply := false
	for {
		timeout := time.Duration(max64(s.rtt+s.rttSigma, 1)) * time.Millisecond
		if err := p4.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return gotReply, err
		}

		hr, _, _, err := p4.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return gotReply, nil
			}
			return gotReply, err
		}
		if hr <= 0 {
			continue
		}

		recvAt := time.Now()
		m, err := icmp.ParseMessage(protocolICMP, buf[:hr])
		if err != nil {
			continue
		}
		if m.Type != ipv4.ICMPTypeTimestampReply {
			continue
		}

		ts, err := timestampFromMessage(m)
		if err != nil || ts.ID != id || ts.Seq < 0 || ts.Seq > s.seq {
			continue
		}
		s.noteReply(ts.Seq)

		recvMS := msSinceMidnightUTC(recvAt)
		sendMS := int64(ts.Originate)
		diff := recvMS - sendMS
		if diff < 0 {
			continue
		}

		s.updateRTT(diff)

		histime := int64(ts.Receive)
		if histime&0x80000000 != 0 {
			return gotReply, ErrNonStdTime
		}

		delta1 := normalizeDelta(histime - sendMS)
		delta2 := normalizeDelta(recvMS - histime)
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

func drainICMP(p4 *ipv4.PacketConn, buf []byte) error {
	for {
		if err := p4.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
			return err
		}
		_, _, _, err := p4.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return nil
			}
			return err
		}
	}
}
