// Package fixture provides a minimal in-process HTTP server for exercising
// deadline-budget behavior in tests and the demo, without any real network
// dependency.
package fixture

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// Server is a fake downstream service. GET /slow?delay=100ms waits for the
// given delay (or until the caller's context is canceled/times out) before
// responding 200 OK. It also counts requests per path so tests can assert
// that a call was skipped entirely.
type Server struct {
	*httptest.Server

	mu     sync.Mutex
	counts map[string]int
}

// NewServer starts a new fixture server. Call Close when done.
func NewServer() *Server {
	s := &Server{counts: make(map[string]int)}

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", s.handleSlow)
	s.Server = httptest.NewServer(mux)

	return s
}

func (s *Server) handleSlow(w http.ResponseWriter, r *http.Request) {
	s.record("/slow")

	delay, err := time.ParseDuration(r.URL.Query().Get("delay"))
	if err != nil {
		delay = 0
	}

	select {
	case <-time.After(delay):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	case <-r.Context().Done():
		// Caller gave up (timeout/cancel) before the delay elapsed.
		// Nothing to write back to a client that's no longer listening.
	}
}

func (s *Server) record(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[path]++
}

// Count returns how many requests path has received so far.
func (s *Server) Count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[path]
}

// Reset clears all recorded request counts.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = make(map[string]int)
}
