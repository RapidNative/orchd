// Package metering counts the gateway's tenant traffic per workload per minute
// (requests, bytes in, bytes out, status class) and reports it to the website,
// which bills it against the team's monthly traffic allowance. It is
// notify-only: nothing here may refuse, slow or fail a request. The counting
// sits on the gateway hot path, so a request costs one small allocation (its
// writer wrapper) and one short sharded-mutex section when it ends.
package metering

import (
	"sync"
	"time"
)

// Counters are one bucket's totals. Bytes are body bytes: what the client
// sent in request bodies (In) and what the gateway wrote back (Out), plus
// every byte on a hijacked (WebSocket) connection in either direction.
type Counters struct {
	Requests int64
	In       int64
	Out      int64
	// Status counts final statuses by class: [0]=2xx [1]=3xx [2]=4xx [3]=5xx.
	// A 101 Switching Protocols (a WebSocket) counts as 2xx: the upgrade
	// succeeded and the connection served.
	Status [4]int64
}

func (c *Counters) add(o Counters) {
	c.Requests += o.Requests
	c.In += o.In
	c.Out += o.Out
	for i := range c.Status {
		c.Status[i] += o.Status[i]
	}
}

// Bucket is one workload's traffic in one UTC minute.
type Bucket struct {
	Minute    time.Time // start of the minute, UTC
	Workload  string    // orchd workload id
	ProjectID string    // orchd project id (resolved to a RapidNative id when reported)
	Counters
}

type bucketKey struct {
	minute   int64 // unix minutes
	workload string
}

type bucketVal struct {
	project string
	Counters
}

const shardCount = 32

type shard struct {
	mu sync.Mutex
	m  map[bucketKey]*bucketVal
}

// Meter holds the open buckets. The zero value is not usable; use NewMeter.
// A nil *Meter is valid and counts nothing (metering off).
type Meter struct {
	shards [shardCount]shard
	now    func() time.Time

	liveMu sync.Mutex
	live   map[*countingConn]struct{}
}

func NewMeter() *Meter {
	m := &Meter{now: time.Now, live: map[*countingConn]struct{}{}}
	for i := range m.shards {
		m.shards[i].m = map[bucketKey]*bucketVal{}
	}
	return m
}

// fnv-1a over the workload id, inline so the hot path neither allocates nor
// goes through hash.Hash.
func shardOf(workload string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(workload); i++ {
		h ^= uint32(workload[i])
		h *= 16777619
	}
	return h % shardCount
}

func unixMinute(t time.Time) int64 { return t.Unix() / 60 }

// add folds c into the (minute, workload) bucket.
func (m *Meter) add(minute int64, workload, project string, c Counters) {
	s := &m.shards[shardOf(workload)]
	k := bucketKey{minute: minute, workload: workload}
	s.mu.Lock()
	b := s.m[k]
	if b == nil {
		b = &bucketVal{project: project}
		s.m[k] = b
	}
	b.Counters.add(c)
	s.mu.Unlock()
}

// Record counts one finished request at the current minute.
func (m *Meter) Record(workload, project string, status int, in, out int64) {
	if m == nil {
		return
	}
	c := Counters{Requests: 1, In: in, Out: out}
	if i := statusClass(status); i >= 0 {
		c.Status[i] = 1
	}
	m.add(unixMinute(m.now()), workload, project, c)
}

// statusClass maps a status code to its Counters.Status index, or -1.
func statusClass(code int) int {
	switch {
	case code == http101:
		return 0
	case code >= 200 && code < 600:
		return code/100 - 2
	}
	return -1
}

const http101 = 101

// Drain removes and returns every bucket for a minute before `before`
// (exclusive), after moving the bytes open hijacked connections carried since
// the last drain into the minute just ended. Pass the zero time to drain
// everything, including the current minute (shutdown).
func (m *Meter) Drain(before time.Time) []Bucket {
	if m == nil {
		return nil
	}
	now := m.now()
	limit := unixMinute(before)
	liveMinute := unixMinute(now) - 1
	if before.IsZero() {
		limit = 1<<62 - 1
		liveMinute = unixMinute(now)
	}
	m.liveMu.Lock()
	for c := range m.live {
		if in, out := c.in.Swap(0), c.out.Swap(0); in != 0 || out != 0 {
			m.add(liveMinute, c.workload, c.project, Counters{In: in, Out: out})
		}
	}
	m.liveMu.Unlock()

	var out []Bucket
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.Lock()
		for k, v := range s.m {
			if k.minute >= limit {
				continue
			}
			out = append(out, Bucket{
				Minute:    time.Unix(k.minute*60, 0).UTC(),
				Workload:  k.workload,
				ProjectID: v.project,
				Counters:  v.Counters,
			})
			delete(s.m, k)
		}
		s.mu.Unlock()
	}
	return out
}

// Open reports how many buckets and live hijacked connections the meter holds.
func (m *Meter) Open() (buckets, conns int) {
	if m == nil {
		return 0, 0
	}
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.Lock()
		buckets += len(s.m)
		s.mu.Unlock()
	}
	m.liveMu.Lock()
	conns = len(m.live)
	m.liveMu.Unlock()
	return buckets, conns
}

func (m *Meter) track(c *countingConn) {
	m.liveMu.Lock()
	m.live[c] = struct{}{}
	m.liveMu.Unlock()
}

// untrack removes a closed connection and books the bytes it carried since
// the last drain at the current minute.
func (m *Meter) untrack(c *countingConn) {
	m.liveMu.Lock()
	delete(m.live, c)
	m.liveMu.Unlock()
	if in, out := c.in.Swap(0), c.out.Swap(0); in != 0 || out != 0 {
		m.add(unixMinute(m.now()), c.workload, c.project, Counters{In: in, Out: out})
	}
}
