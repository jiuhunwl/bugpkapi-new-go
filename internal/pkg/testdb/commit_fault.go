package testdb

import (
	"bytes"
	"io"
	"net"
	"strings"
	"sync/atomic"
)

// CommitFault is a one-shot local MySQL fault injector. In after-commit mode it
// consumes a successful server OK packet before reporting EOF to the client.
// In before-commit mode it disconnects before forwarding COMMIT at all.
type CommitFault struct {
	// AfterCommit runs after a real server OK, before the caller observes EOF.
	// Set it before using the wrapped connection. It is only a test barrier.
	AfterCommit      func() error
	before           bool
	armed, triggered atomic.Bool
}

func NewCommitFault(before bool) *CommitFault {
	fault := &CommitFault{before: before}
	fault.armed.Store(true)
	return fault
}
func (f *CommitFault) Triggered() bool             { return f.triggered.Load() }
func (f *CommitFault) Wrap(conn net.Conn) net.Conn { return &commitFaultConn{Conn: conn, fault: f} }

type commitFaultConn struct {
	net.Conn
	fault   *CommitFault
	drop    atomic.Bool
	pending bytes.Buffer
}

func (c *commitFaultConn) Write(data []byte) (int, error) {
	if len(data) > 5 && data[4] == 3 && strings.EqualFold(strings.TrimSpace(string(data[5:])), "commit") && c.fault.armed.CompareAndSwap(true, false) {
		if c.fault.before {
			c.fault.triggered.Store(true)
			c.Conn.Close()
			return 0, io.ErrClosedPipe
		}
		c.drop.Store(true)
	}
	return c.Conn.Write(data)
}
func (c *commitFaultConn) Read(data []byte) (int, error) {
	if c.pending.Len() > 0 {
		return c.pending.Read(data)
	}
	if c.drop.Swap(false) {
		header := make([]byte, 4)
		if _, err := io.ReadFull(c.Conn, header); err != nil {
			return 0, err
		}
		size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
		if size < 1 || size > 1024*1024 {
			return 0, io.ErrUnexpectedEOF
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(c.Conn, payload); err != nil {
			return 0, err
		}
		if payload[0] == 0 {
			c.fault.triggered.Store(true)
			c.Conn.Close()
			if c.fault.AfterCommit != nil {
				if err := c.fault.AfterCommit(); err != nil {
					return 0, err
				}
			}
			return 0, io.ErrUnexpectedEOF
		}
		c.pending.Write(header)
		c.pending.Write(payload)
		return c.pending.Read(data)
	}
	return c.Conn.Read(data)
}
