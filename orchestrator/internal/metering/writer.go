package metering

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

// Writer meters one request: it wraps the ResponseWriter (status, bytes out)
// and the request body (bytes in). Begin allocates one per request; nothing
// else on the request path allocates. It keeps Flusher, Hijacker and Pusher
// working (Flush and Push are no-ops or ErrNotSupported when the underlying
// writer lacks them) and unwraps for http.ResponseController.
type Writer struct {
	w        http.ResponseWriter
	m        *Meter
	workload string
	project  string
	status   int
	out      int64
	body     countingBody
	conn     *countingConn // set once hijacked
}

// countingBody counts request body bytes. The transport may read a body from
// its own goroutine, hence the atomic.
type countingBody struct {
	rc io.ReadCloser
	n  atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.n.Add(int64(n))
	return n, err
}

func (b *countingBody) Close() error { return b.rc.Close() }

// Begin starts metering a request to a workload. It replaces r.Body with a
// counting reader; serve the request with the returned Writer and call Finish
// when the handler is done. A nil Meter returns nil: callers check.
func (m *Meter) Begin(w http.ResponseWriter, r *http.Request, workload, project string) *Writer {
	if m == nil {
		return nil
	}
	mw := &Writer{w: w, m: m, workload: workload, project: project}
	if r.Body != nil && r.Body != http.NoBody {
		mw.body.rc = r.Body
		r.Body = &mw.body
	}
	return mw
}

// Finish records the request. A hijacked connection still open is released:
// its remaining bytes are booked and it stops being tracked.
func (mw *Writer) Finish() {
	status := mw.status
	if status == 0 {
		status = http.StatusOK // net/http's implicit status
	}
	if mw.conn != nil {
		mw.conn.release()
	}
	mw.m.Record(mw.workload, mw.project, status, mw.body.n.Load(), mw.out)
}

func (mw *Writer) Header() http.Header { return mw.w.Header() }

func (mw *Writer) WriteHeader(code int) {
	// 1xx informational responses (103 Early Hints) may precede the final one.
	if mw.status == 0 && code >= 200 {
		mw.status = code
	}
	mw.w.WriteHeader(code)
}

func (mw *Writer) Write(p []byte) (int, error) {
	if mw.status == 0 {
		mw.status = http.StatusOK
	}
	n, err := mw.w.Write(p)
	mw.out += int64(n)
	return n, err
}

func (mw *Writer) Flush() {
	if f, ok := mw.w.(http.Flusher); ok {
		f.Flush()
	}
}

// FlushError is what http.ResponseController.Flush calls first.
func (mw *Writer) FlushError() error { return http.NewResponseController(mw.w).Flush() }

func (mw *Writer) Push(target string, opts *http.PushOptions) error {
	if p, ok := mw.w.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// Unwrap lets http.ResponseController reach the underlying writer for what
// Writer doesn't implement itself (deadlines, full duplex).
func (mw *Writer) Unwrap() http.ResponseWriter { return mw.w }

// Hijack hands over a connection that counts every byte read from and written
// to it. The bytes are booked to the meter at each Drain while it is open and
// when it closes, so a long WebSocket lands minute by minute.
func (mw *Writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(mw.w).Hijack()
	if err != nil {
		return nil, nil, err
	}
	if mw.status == 0 {
		mw.status = http.StatusSwitchingProtocols
	}
	c := &countingConn{Conn: conn, m: mw.m, workload: mw.workload, project: mw.project}
	mw.conn = c
	mw.m.track(c)

	// Route the buffered reader and writer through the counting conn too.
	// Bytes the server already buffered from the client count as read.
	if n := brw.Reader.Buffered(); n == 0 {
		brw.Reader.Reset(c)
	} else {
		pending, _ := brw.Reader.Peek(n)
		pending = bytes.Clone(pending)
		c.in.Add(int64(n))
		brw.Reader = bufio.NewReader(io.MultiReader(bytes.NewReader(pending), c))
	}
	if brw.Writer.Buffered() > 0 {
		if err := brw.Writer.Flush(); err != nil {
			c.release()
			return nil, nil, err
		}
	}
	brw.Writer.Reset(c)
	return c, brw, nil
}

// countingConn counts a hijacked connection's bytes until it is closed.
type countingConn struct {
	net.Conn
	m        *Meter
	workload string
	project  string
	in, out  atomic.Int64
	once     sync.Once
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.in.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.out.Add(int64(n))
	return n, err
}

func (c *countingConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}

// release books the bytes not yet drained and stops tracking the conn.
func (c *countingConn) release() { c.once.Do(func() { c.m.untrack(c) }) }
