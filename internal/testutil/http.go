package testutil

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

// RoundTripFunc adapts a function to http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip returns f(r).
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// CountingTransport counts the round trips it passes on to Next.
type CountingTransport struct {
	Next     http.RoundTripper
	attempts atomic.Int32
}

// RoundTrip counts the attempt and delegates to Next.
func (c *CountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.attempts.Add(1)
	return c.Next.RoundTrip(r)
}

// Attempts returns the number of round trips so far.
func (c *CountingTransport) Attempts() int { return int(c.attempts.Load()) }

// Drain reads and closes the request body, as a real transport would, and
// returns its length.
func Drain(r *http.Request) int64 {
	if r.Body == nil {
		return 0
	}
	n, _ := io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	return n
}

// BlockUntilDone is an in-memory round trip that never answers: it drains the
// request, waits for the request's context to end and returns its error.
func BlockUntilDone(r *http.Request) (*http.Response, error) {
	Drain(r)
	<-r.Context().Done()
	return nil, r.Context().Err()
}

// Request is one request as a test saw it.
type Request struct {
	Method string
	// URL is the request's URL as received: the path and query on a server,
	// the full URL in a transport.
	URL    string
	Header http.Header
	Body   []byte
}

// Record reads and closes r's body and returns a copy of the request.
func Record(r *http.Request) Request {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
	}
	return Request{Method: r.Method, URL: r.URL.String(), Header: r.Header.Clone(), Body: body}
}

// Server is a local TLS server that records every request before handing it
// to its handler. Handlers run on server goroutines, so they should only
// answer; tests assert on Requests afterward.
type Server struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []Request
}

// NewServer starts a Server that records each request and then calls h. It is
// closed when the test ends. Its error log is discarded: when a test ends while
// the client is still finishing a spare TLS handshake, the closing server would
// log a harmless "use of closed network connection".
func NewServer(tb testing.TB, h http.HandlerFunc) *Server {
	tb.Helper()
	s := &Server{}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := Record(r)
		s.mu.Lock()
		s.reqs = append(s.reqs, rec)
		s.mu.Unlock()
		h(w, r)
	}))
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.srv.StartTLS()
	tb.Cleanup(s.srv.Close)
	return s
}

// URL returns the server's base URL, such as https://127.0.0.1:port.
func (s *Server) URL() string { return s.srv.URL }

// Client returns an HTTP client that trusts the server's certificate.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// Requests returns a copy of the recorded requests, in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// Hits returns the number of requests received.
func (s *Server) Hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}
