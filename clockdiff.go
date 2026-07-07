// Package clockdiff measures clock difference between hosts using ICMP
// TIMESTAMP messages as described in RFC 792 and the clockdiff(8) utility.
package clockdiff

import (
	"errors"
	"fmt"
	"net"
	"time"
)

const protocolICMP = 1

const (
	defaultMessages = 50
	defaultTrials   = 10
	rangeMS         = 1 // best expected round-trip time in milliseconds

	moduloMS = 24 * 60 * 60 * 1000
	biasN    = -12 * 60 * 60 * 1000
	biasP    = 12*60*60*1000 - 1
)

var (
	// ErrHostDown is returned when the destination does not answer consecutive probes.
	ErrHostDown = errors.New("host down")
	// ErrHostUnreachable is returned when probes cannot be sent.
	ErrHostUnreachable = errors.New("host unreachable")
	// ErrNonStdTime is returned when the remote host uses a non-standard timestamp format.
	ErrNonStdTime = errors.New("non-standard remote timestamp format")
)

// Result holds the outcome of a clockdiff measurement.
type Result struct {
	Host      string
	Delta     time.Duration // primary estimate: (min1-min2)/2
	DeltaBest time.Duration // estimate from the lowest RTT sample
	RTT       time.Duration // smoothed round-trip time estimate
	RTTSigma  time.Duration
	MinRTT    time.Duration
}

// Mode selects the probe type used for measurement.
type Mode int

const (
	// ModeICMPTimestamp uses ICMP Timestamp request/reply (RFC 792).
	ModeICMPTimestamp Mode = iota
	// ModeIPTimestamp uses IP Timestamp option with ICMP Echo (-o).
	ModeIPTimestamp
	// ModeIPTimestamp3 uses three-term IP Timestamp with ICMP Echo (-o1).
	ModeIPTimestamp3
)

// Config configures a clockdiff measurement.
type Config struct {
	Messages int
	Trials   int
	Mode     Mode
	OnReply  func() // called after each valid reply (e.g. progress dots)
}

// Option configures Measure.
type Option func(*Config)

// WithMessages sets how many probes to send (default 50).
func WithMessages(n int) Option {
	return func(c *Config) {
		if n > 0 {
			c.Messages = n
		}
	}
}

// WithTrials sets unanswered probes before declaring the host down (default 10).
func WithTrials(n int) Option {
	return func(c *Config) {
		if n > 0 {
			c.Trials = n
		}
	}
}

// WithMode sets the measurement mode (default ICMP Timestamp).
func WithMode(m Mode) Option {
	return func(c *Config) {
		c.Mode = m
	}
}

// WithOnReply registers a callback invoked after each valid reply.
func WithOnReply(fn func()) Option {
	return func(c *Config) {
		c.OnReply = fn
	}
}

// Measure estimates the clock offset between the local host and dst.
// A positive Delta means the remote clock is ahead of the local clock.
//
// Raw ICMP sockets are required (typically root or CAP_NET_RAW).
func Measure(dst string, opts ...Option) (*Result, error) {
	cfg := Config{
		Messages: defaultMessages,
		Trials:   defaultTrials,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	host, ip, err := resolve(dst)
	if err != nil {
		return nil, err
	}

	switch cfg.Mode {
	case ModeIPTimestamp:
		return measureIPTimestamp(host, ip, cfg)
	case ModeIPTimestamp3:
		return measureIPTimestamp3(host, ip, cfg)
	default:
		return measureICMPTimestamp(host, ip, cfg)
	}
}

type measureState struct {
	id        int
	seq       int
	acked     int
	rtt       int64
	rttSigma  int64
	minRTT    int64
	min1      int64
	min2      int64
	deltaBest int64
}

func newMeasureState(id int) *measureState {
	return &measureState{
		id:     id,
		rtt:    1000,
		minRTT: maxInt64,
		min1:   maxInt64,
		min2:   maxInt64,
	}
}

func (s *measureState) result(host string) *Result {
	delta := (s.min1 - s.min2) / 2
	return &Result{
		Host:      host,
		Delta:     time.Duration(delta) * time.Millisecond,
		DeltaBest: time.Duration(s.deltaBest) * time.Millisecond,
		RTT:       time.Duration(s.rtt) * time.Millisecond,
		RTTSigma:  time.Duration(s.rttSigma) * time.Millisecond,
		MinRTT:    time.Duration(s.minRTT) * time.Millisecond,
	}
}

func (s *measureState) hostDown(host string, trials int) error {
	if s.seq-s.acked > trials {
		return fmt.Errorf("%w: %s", ErrHostDown, host)
	}
	return nil
}

func (s *measureState) noteReply(seq int) {
	if seq > s.acked {
		s.acked = seq
	}
}

func (s *measureState) updateRTT(diff int64) {
	s.rtt = (s.rtt*3 + diff) / 4
	s.rttSigma = (s.rttSigma*3 + abs64(diff-s.rtt)) / 4
}

func (s *measureState) updateDeltas(delta1, delta2 int64) {
	if delta1 < s.min1 {
		s.min1 = delta1
	}
	if delta2 < s.min2 {
		s.min2 = delta2
	}
	if delta1+delta2 < s.minRTT {
		s.minRTT = delta1 + delta2
		s.deltaBest = (delta1 - delta2) / 2
	}
}

func (s *measureState) earlyExit(delta1, delta2, diff int64) bool {
	if diff < rangeMS {
		s.min1 = delta1
		s.min2 = delta2
		return true
	}
	return false
}

func resolve(dst string) (host string, ip net.IP, err error) {
	if ip := net.ParseIP(dst); ip != nil {
		return dst, ip.To4(), nil
	}
	names, err := net.LookupHost(dst)
	if err != nil {
		return "", nil, err
	}
	if len(names) == 0 {
		return "", nil, fmt.Errorf("no addresses for %q", dst)
	}
	ip = net.ParseIP(names[0])
	if ip == nil {
		return "", nil, fmt.Errorf("invalid address for %q", dst)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", nil, fmt.Errorf("%q is not an IPv4 address", dst)
	}
	return dst, ip4, nil
}

func msSinceMidnightUTC(t time.Time) int64 {
	utc := t.UTC()
	midnight := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	return utc.Sub(midnight).Milliseconds()
}

func normalizeDelta(delta int64) int64 {
	if delta < biasN {
		return delta + moduloMS
	}
	if delta > biasP {
		return delta - moduloMS
	}
	return delta
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

const maxInt64 = int64(^uint64(0) >> 1)
