package gateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// resettingServer closes its first `drop` connections without answering, the
// way Docker's port proxy accepts and resets while the app inside isn't
// listening yet, then answers every request with 200 "ok".
func resettingServer(t *testing.T, drop int32) (addr string, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if accepted.Add(1) <= drop {
				c.Close()
				continue
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}(c)
		}
	}()
	return ln.Addr().String(), accepted
}

func get(t *testing.T, tr http.RoundTripper, method, url string, body io.Reader) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		req.Body = nil
	}
	return tr.RoundTrip(req)
}

func newTransport(window time.Duration) *startupTransport {
	return &startupTransport{base: &http.Transport{DisableKeepAlives: true}, until: time.Now().Add(window)}
}

func TestStartupRetriesUntilTheServerAnswers(t *testing.T) {
	addr, accepted := resettingServer(t, 3)
	resp, err := get(t, newTransport(10*time.Second), http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatalf("GET during startup failed: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("got %d %q", resp.StatusCode, b)
	}
	if n := accepted.Load(); n < 4 {
		t.Fatalf("server saw %d connections, want the 3 resets plus the answered one", n)
	}
}

func TestStartupWaitsForAPortThatIsNotListeningYet(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // refused until the server below starts
	go func() {
		time.Sleep(1200 * time.Millisecond)
		ln2, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "up") })}
		t.Cleanup(func() { srv.Close() })
		srv.Serve(ln2)
	}()
	resp, err := get(t, newTransport(10*time.Second), http.MethodGet, "http://"+addr+"/status", nil)
	if err != nil {
		t.Fatalf("GET before the port listened failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestStartupNeverRetriesARequestWithABody(t *testing.T) {
	addr, accepted := resettingServer(t, 100)
	_, err := get(t, newTransport(10*time.Second), http.MethodPost, "http://"+addr+"/api/upload", strings.NewReader("data"))
	if err == nil {
		t.Fatal("POST to a resetting server should fail")
	}
	if n := accepted.Load(); n != 1 {
		t.Fatalf("POST was sent %d times, want once", n)
	}
}

func TestStartupGivesUpWhenTheWindowHasPassed(t *testing.T) {
	addr, accepted := resettingServer(t, 100)
	start := time.Now()
	_, err := get(t, newTransport(0), http.MethodGet, "http://"+addr+"/", nil)
	if err == nil {
		t.Fatal("GET to a resetting server after the window should fail")
	}
	if n := accepted.Load(); n != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("retried %d times in %v after the window", n, time.Since(start))
	}
}
