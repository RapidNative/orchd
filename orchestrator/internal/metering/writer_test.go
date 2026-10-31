package metering

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable time source for the meter and reporter.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClockedMeter() (*Meter, *clock) {
	c := &clock{t: time.Date(2026, 10, 31, 10, 0, 30, 0, time.UTC)}
	m := NewMeter()
	m.now = c.now
	return m, c
}

// metered serves h behind the meter as workload "w" of project "p".
func metered(m *Meter, h http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw := m.Begin(w, r, "w", "p")
		defer mw.Finish()
		h(mw, r)
	}))
}

func only(t *testing.T, m *Meter) Bucket {
	t.Helper()
	bs := m.Drain(time.Time{})
	if len(bs) != 1 {
		t.Fatalf("buckets = %+v, want one", bs)
	}
	return bs[0]
}

func TestWriterCountsBodiesAndStatus(t *testing.T) {
	m, _ := newClockedMeter()
	srv := metered(m, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "hello world") // 11
	})
	defer srv.Close()

	res, err := http.Post(srv.URL, "text/plain", strings.NewReader("0123456789")) // 10
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res.Body)
	res.Body.Close()

	b := only(t, m)
	want := Counters{Requests: 1, In: 10, Out: 11, Status: [4]int64{1, 0, 0, 0}}
	if b.Counters != want || b.Workload != "w" || b.ProjectID != "p" {
		t.Fatalf("bucket = %+v, want %+v", b, want)
	}
	if !b.Minute.Equal(time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("minute = %s", b.Minute)
	}
}

func TestWriterStatusClasses(t *testing.T) {
	m, _ := newClockedMeter()
	srv := metered(m, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hints":
			w.WriteHeader(http.StatusEarlyHints) // informational, then the real one
			w.WriteHeader(http.StatusNoContent)
		case "/implicit":
			// no write at all: net/http answers 200
		case "/redirect":
			http.Redirect(w, r, "/x", http.StatusFound)
		case "/missing":
			http.NotFound(w, r)
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	})
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"/hints", "/implicit", "/redirect", "/missing", "/fail", "/fail"} {
		res, err := client.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	b := only(t, m)
	if b.Requests != 6 || b.Status != [4]int64{2, 1, 1, 2} {
		t.Fatalf("bucket = %+v, want 6 requests, status 2/1/1/2", b.Counters)
	}
}

// A streamed response reaches the client chunk by chunk through the wrapper,
// by either flush path, and every chunk is counted.
func TestWriterStreamsAndFlushes(t *testing.T) {
	m, _ := newClockedMeter()
	next := make(chan struct{})
	srv := metered(m, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "data: tick\n\n") // 12
			if i%2 == 0 {
				w.(http.Flusher).Flush()
			} else if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("ResponseController.Flush: %v", err)
			}
			<-next // the client must have seen this chunk before the next is written
		}
	})
	defer srv.Close()

	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(res.Body)
	for i := 0; i < 3; i++ {
		line, err := br.ReadString('\n')
		if err != nil || line != "data: tick\n" {
			t.Fatalf("chunk %d: %q %v", i, line, err)
		}
		_, _ = br.ReadString('\n')
		next <- struct{}{}
	}
	_, _ = io.ReadAll(br)
	res.Body.Close()

	b := only(t, m)
	if b.Requests != 1 || b.Out != 36 || b.Status[0] != 1 {
		t.Fatalf("bucket = %+v, want 1 request, 36 bytes out", b.Counters)
	}
}

func TestWriterKeepsInterfaces(t *testing.T) {
	m := NewMeter()
	rec := httptest.NewRecorder()
	mw := m.Begin(rec, httptest.NewRequest("GET", "/", nil), "w", "p")
	var w http.ResponseWriter = mw
	if _, ok := w.(http.Flusher); !ok {
		t.Fatal("not a Flusher")
	}
	if _, ok := w.(http.Hijacker); !ok {
		t.Fatal("not a Hijacker")
	}
	p, ok := w.(http.Pusher)
	if !ok {
		t.Fatal("not a Pusher")
	}
	if err := p.Push("/x", nil); err != http.ErrNotSupported {
		t.Fatalf("Push on HTTP/1 = %v, want ErrNotSupported", err)
	}
	// A recorder can't be hijacked: the error comes through, nothing is tracked.
	if _, _, err := w.(http.Hijacker).Hijack(); err == nil {
		t.Fatal("hijacked a recorder")
	}
	if _, conns := m.Open(); conns != 0 {
		t.Fatalf("live conns = %d", conns)
	}
	if rc := http.NewResponseController(w); rc.SetWriteDeadline(time.Now().Add(time.Second)) == nil {
		t.Fatal("a recorder has no deadlines; the call should reach it and fail")
	}
}

// A hijacked connection counts both directions until it closes. Bytes it
// carries while open are booked at each drain to the minute just ended; the
// rest when it closes.
func TestWriterHijackedConnection(t *testing.T) {
	m, clk := newClockedMeter()
	closed := make(chan struct{})
	srv := metered(m, func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer close(closed)
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		buf := make([]byte, 64)
		for {
			n, err := brw.Read(buf)
			if err != nil {
				return
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
		}
	})
	defer srv.Close()

	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	req := "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n"
	_, _ = io.WriteString(c, req)
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", res, err)
	}
	headerOut := int64(len("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n"))
	echo := func(s string) {
		_, _ = io.WriteString(c, s)
		got := make([]byte, len(s))
		if _, err := io.ReadFull(br, got); err != nil || string(got) != s {
			t.Fatalf("echo %q: %q %v", s, got, err)
		}
	}
	echo("first-minute") // 12 each way

	// The minute ends; a drain books the open conn's bytes so far to it.
	clk.advance(time.Minute)
	bs := m.Drain(clk.now())
	if len(bs) != 1 {
		t.Fatalf("mid-connection drain = %+v", bs)
	}
	if b := bs[0]; b.Requests != 0 || b.In != 12 || b.Out != headerOut+12 || b.Minute.Minute() != 0 {
		t.Fatalf("mid-connection bucket = %+v, want 12 in, %d out, no request yet", b, headerOut+12)
	}
	if _, conns := m.Open(); conns != 1 {
		t.Fatalf("live conns = %d, want 1", conns)
	}

	echo("second") // 6 each way
	c.Close()
	<-closed
	time.Sleep(20 * time.Millisecond) // let Finish run after the handler returns

	b := only(t, m)
	if b.Requests != 1 || b.In != 6 || b.Out != 6 || b.Status[0] != 1 || b.Minute.Minute() != 1 {
		t.Fatalf("closing bucket = %+v, want the request (101 as 2xx) and 6 bytes each way in minute 1", b)
	}
	if _, conns := m.Open(); conns != 0 {
		t.Fatalf("live conns = %d after close", conns)
	}
}

func TestNilMeterIsOff(t *testing.T) {
	var m *Meter
	if mw := m.Begin(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), "w", "p"); mw != nil {
		t.Fatal("a nil meter returned a writer")
	}
	m.Record("w", "p", 200, 1, 1)
	if m.Drain(time.Time{}) != nil {
		t.Fatal("a nil meter drained buckets")
	}
}

// Many goroutines recording while another drains lose nothing.
func TestMeterConcurrentRecordAndDrain(t *testing.T) {
	m := NewMeter()
	const workers, each = 8, 5000
	var wg sync.WaitGroup
	var total Counters
	var mu sync.Mutex
	stop := make(chan struct{})
	drained := make(chan struct{})
	collect := func(bs []Bucket) {
		mu.Lock()
		for _, b := range bs {
			total.add(b.Counters)
		}
		mu.Unlock()
	}
	go func() {
		defer close(drained)
		for {
			select {
			case <-stop:
				return
			default:
				collect(m.Drain(time.Time{}))
			}
		}
	}()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wl := string(rune('a' + i))
			for j := 0; j < each; j++ {
				m.Record(wl, "p", 200+(j%4)*100, 2, 3)
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	<-drained
	collect(m.Drain(time.Time{}))
	n := int64(workers * each)
	want := Counters{Requests: n, In: 2 * n, Out: 3 * n, Status: [4]int64{n / 4, n / 4, n / 4, n / 4}}
	if total != want {
		t.Fatalf("totals = %+v, want %+v", total, want)
	}
}

func BenchmarkRecordParallel(b *testing.B) {
	m := NewMeter()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			m.Record(workloads[i%len(workloads)], "p", 200, 100, 1000)
			i++
		}
	})
}

var workloads = []string{"w-a1b2c3", "w-d4e5f6", "w-g7h8i9", "w-j0k1l2", "w-m3n4o5", "w-p6q7r8", "w-s9t0u1", "w-v2w3x4"}

func BenchmarkBeginFinish(b *testing.B) {
	m := NewMeter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader("x"))
	payload := []byte("hello")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mw := m.Begin(rec, req, "w", "p")
		_, _ = mw.Write(payload)
		mw.Finish()
		req.Body = mw.body.rc
	}
}
