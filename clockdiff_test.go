package clockdiff

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func TestTimestampMarshalParse(t *testing.T) {
	in := &Timestamp{
		ID:        42,
		Seq:       7,
		Originate: 123456,
		Receive:   234567,
		Transmit:  345678,
	}

	b, err := in.Marshal(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != timestampBodyLen {
		t.Fatalf("len = %d, want %d", len(b), timestampBodyLen)
	}

	out, err := parseTimestampBody(b)
	if err != nil {
		t.Fatal(err)
	}
	if *out != *in {
		t.Fatalf("got %+v, want %+v", out, in)
	}

	msg, err := icmp.ParseMessage(1, append([]byte{14, 0, 0, 0}, b...))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != ipv4.ICMPTypeTimestampReply {
		t.Fatalf("type = %v", msg.Type)
	}
	ts, err := timestampFromMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Receive != in.Receive {
		t.Fatalf("receive = %d, want %d", ts.Receive, in.Receive)
	}
}

func TestMsSinceMidnightUTC(t *testing.T) {
	ts := time.Date(2026, 7, 7, 12, 34, 56, 789_000_000, time.UTC)
	got := msSinceMidnightUTC(ts)
	want := int64((12*60*60+34*60+56)*1000 + 789)
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

func TestNormalizeDelta(t *testing.T) {
	tests := []struct {
		in, want int64
	}{
		{100, 100},
		{biasN - 1, biasN - 1 + moduloMS},
		{biasP + 1, biasP + 1 - moduloMS},
	}
	for _, tc := range tests {
		if got := normalizeDelta(tc.in); got != tc.want {
			t.Fatalf("normalizeDelta(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseTimestampBodyTooShort(t *testing.T) {
	_, err := parseTimestampBody(make([]byte, 8))
	if err != errInvalidTimestamp {
		t.Fatalf("err = %v, want %v", err, errInvalidTimestamp)
	}
}

func TestBuildIPTimestampOptions(t *testing.T) {
	local := net.IPv4(10, 0, 0, 1)
	remote := net.IPv4(10, 0, 10, 203)

	three := buildIPTimestampOptions(local, remote, false)
	if len(three) != 28 || three[0] != ipOptTimestamp || three[3] != ipOptTSPrespec {
		t.Fatalf("three-term option = %v", three)
	}
	if !bytes.Equal(three[4:8], local.To4()) || !bytes.Equal(three[12:16], remote.To4()) || !bytes.Equal(three[20:24], local.To4()) {
		t.Fatalf("three-term addresses = % x", three)
	}

	four := buildIPTimestampOptions(local, remote, true)
	if len(four) != 36 {
		t.Fatalf("four-term len = %d", len(four))
	}
	if !bytes.Equal(four[20:24], remote.To4()) || !bytes.Equal(four[28:32], local.To4()) {
		t.Fatalf("four-term addresses = % x", four)
	}
}

func TestParseIPTimestampOption(t *testing.T) {
	opt := make([]byte, 36)
	opt[0] = ipOptTimestamp
	opt[1] = 36
	opt[2] = 5 + 4*8
	opt[3] = ipOptTSPrespec
	binary.BigEndian.PutUint32(opt[8:12], 1000)
	binary.BigEndian.PutUint32(opt[16:20], 1100)
	binary.BigEndian.PutUint32(opt[24:28], 1150)
	binary.BigEndian.PutUint32(opt[32:36], 1200)

	sample, err := parseIPTimestampOption(opt, true)
	if err != nil {
		t.Fatal(err)
	}
	if sample.sendtime != 1000 || sample.histime != 1100 || sample.histime1 != 1150 || sample.recvtime != 1200 {
		t.Fatalf("got %+v", sample)
	}
}

func TestParseIPTimestampOptionNonStd(t *testing.T) {
	opt := make([]byte, 28)
	opt[0] = ipOptTimestamp
	opt[1] = 28
	opt[2] = 13
	opt[3] = ipOptTSPrespec
	binary.BigEndian.PutUint32(opt[8:12], 0x80000001)
	_, err := parseIPTimestampOption(opt, false)
	if err != ErrNonStdTime {
		t.Fatalf("err = %v, want %v", err, ErrNonStdTime)
	}
}

func TestTimestampFromRawBody(t *testing.T) {
	raw := make([]byte, timestampBodyLen)
	binary.BigEndian.PutUint16(raw[0:2], 9)
	binary.BigEndian.PutUint16(raw[2:4], 3)
	binary.BigEndian.PutUint32(raw[8:12], 999)

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeTimestampReply,
		Body: &icmp.RawBody{Data: raw},
	}
	ts, err := timestampFromMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if ts.ID != 9 || ts.Seq != 3 || ts.Receive != 999 {
		t.Fatalf("got %+v", ts)
	}
}
