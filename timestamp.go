package clockdiff

import (
	"encoding/binary"
	"errors"

	"golang.org/x/net/icmp"
)

const timestampBodyLen = 16

var errInvalidTimestamp = errors.New("invalid icmp timestamp body")

// Timestamp is the body of an ICMP Timestamp or Timestamp Reply message (RFC 792).
// All timestamp fields are milliseconds since midnight UTC.
type Timestamp struct {
	ID        int
	Seq       int
	Originate uint32
	Receive   uint32
	Transmit  uint32
}

// Len implements icmp.MessageBody.
func (p *Timestamp) Len(proto int) int {
	if p == nil {
		return 0
	}
	return timestampBodyLen
}

// Marshal implements icmp.MessageBody.
func (p *Timestamp) Marshal(proto int) ([]byte, error) {
	b := make([]byte, timestampBodyLen)
	binary.BigEndian.PutUint16(b[0:2], uint16(p.ID))
	binary.BigEndian.PutUint16(b[2:4], uint16(p.Seq))
	binary.BigEndian.PutUint32(b[4:8], p.Originate)
	binary.BigEndian.PutUint32(b[8:12], p.Receive)
	binary.BigEndian.PutUint32(b[12:16], p.Transmit)
	return b, nil
}

func parseTimestampBody(b []byte) (*Timestamp, error) {
	if len(b) < timestampBodyLen {
		return nil, errInvalidTimestamp
	}
	return &Timestamp{
		ID:        int(binary.BigEndian.Uint16(b[0:2])),
		Seq:       int(binary.BigEndian.Uint16(b[2:4])),
		Originate: binary.BigEndian.Uint32(b[4:8]),
		Receive:   binary.BigEndian.Uint32(b[8:12]),
		Transmit:  binary.BigEndian.Uint32(b[12:16]),
	}, nil
}

func timestampFromMessage(m *icmp.Message) (*Timestamp, error) {
	if m == nil {
		return nil, errInvalidTimestamp
	}
	switch body := m.Body.(type) {
	case *Timestamp:
		return body, nil
	default:
		if body == nil {
			return nil, errInvalidTimestamp
		}
		b, err := body.Marshal(1)
		if err != nil {
			return nil, err
		}
		return parseTimestampBody(b)
	}
}
