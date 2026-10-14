package httpx_test

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// HTTP/2 frame types and flags used by resetServer.
const (
	frameData     = 0x0
	frameHeaders  = 0x1
	frameRST      = 0x3
	frameSettings = 0x4
	framePing     = 0x6
	frameWindow   = 0x8
	flagEnd       = 0x1 // END_STREAM on HEADERS/DATA, ACK on SETTINGS/PING
	h2Preface     = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	codeRefused   = 0x7 // REFUSED_STREAM
	codeProtocol  = 0x1 // PROTOCOL_ERROR
	maxFrameBytes = 1 << 24
)

var errFrameTooLarge = errors.New("frame too large")

// resetServer is a minimal HTTP/2 server that reads each request completely and
// then resets its stream with a fixed error code. A server that has the whole
// request may already have acted on it, so the client must not send it again.
type resetServer struct {
	ln       net.Listener
	code     uint32
	requests atomic.Int64
	wg       sync.WaitGroup
}

func startResetServer(t *testing.T, code uint32) (*resetServer, *http.Client) {
	t.Helper()
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	cert := ts.TLS.Certificates[0]
	tr := ts.Client().Transport.(*http.Transport).Clone()
	ts.Close()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12})
	require.NoError(t, err)
	s := &resetServer{ln: ln, code: code}
	s.wg.Go(s.accept)
	t.Cleanup(func() {
		_ = ln.Close()
		tr.CloseIdleConnections()
		s.wg.Wait()
	})
	// Same settings as http.DefaultTransport, which negotiates HTTP/2.
	tr.ForceAttemptHTTP2 = true
	return s, &http.Client{Transport: tr}
}

func (s *resetServer) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Go(func() { s.serve(c) })
	}
}

func (s *resetServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	if _, err := io.ReadFull(r, make([]byte, len(h2Preface))); err != nil {
		return
	}
	_ = writeFrame(c, frameSettings, 0, 0, nil)
	for {
		typ, flags, stream, payload, err := readFrame(r)
		if err != nil {
			return
		}
		s.handle(c, typ, flags, stream, payload)
	}
}

// handle answers one client frame: it acknowledges settings and pings, counts
// requests, returns flow-control credit, and resets each stream once its request
// is complete.
func (s *resetServer) handle(w io.Writer, typ, flags byte, stream uint32, payload []byte) {
	end := flags&flagEnd != 0
	switch typ {
	case frameSettings:
		if !end {
			_ = writeFrame(w, frameSettings, flagEnd, 0, nil) // an ACK carries no payload
		}
		return
	case framePing:
		if !end {
			_ = writeFrame(w, framePing, flagEnd, 0, payload)
		}
		return
	case frameHeaders:
		s.requests.Add(1)
	case frameData:
		if n := len(payload); n > 0 && n <= maxFrameBytes {
			_ = writeFrame(w, frameWindow, 0, 0, binary.BigEndian.AppendUint32(nil, uint32(n)))
		}
	default:
		return
	}
	if end {
		_ = writeFrame(w, frameRST, 0, stream, binary.BigEndian.AppendUint32(nil, s.code))
	}
}

func readFrame(r io.Reader) (typ, flags byte, stream uint32, payload []byte, err error) {
	var hdr [9]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, 0, nil, err
	}
	n := int(hdr[0])<<16 | int(hdr[1])<<8 | int(hdr[2])
	if n > maxFrameBytes {
		return 0, 0, 0, nil, errFrameTooLarge
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, 0, 0, nil, err
	}
	return hdr[3], hdr[4], binary.BigEndian.Uint32(hdr[5:]) & 0x7fffffff, payload, nil
}

func writeFrame(w io.Writer, typ, flags byte, stream uint32, payload []byte) error {
	n := len(payload)
	if n > maxFrameBytes {
		return errFrameTooLarge
	}
	frame := make([]byte, 9, 9+n)
	binary.BigEndian.PutUint32(frame[0:4], uint32(n)<<8) // 24-bit length, then the type byte
	frame[3], frame[4] = typ, flags
	binary.BigEndian.PutUint32(frame[5:], stream)
	_, err := w.Write(append(frame, payload...))
	return err
}

// TestDoNeverReplaysAPostOverHTTP2 guards the one-attempt contract against the
// standard HTTP/2 client, which resends a request whose body it can rewind when a
// stream is refused or reset, even after the server has read the whole body.
func TestDoNeverReplaysAPostOverHTTP2(t *testing.T) {
	tests := []struct {
		name string
		code uint32
	}{
		{"refused stream", codeRefused},
		{"protocol error", codeProtocol},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, hc := startResetServer(t, tt.code)
			r := baseRequest("https://" + s.ln.Addr().String() + "/v1/systemone")
			r.Timeout = 3 * time.Second
			start := time.Now()
			resp, err := httpx.Do(t.Context(), httpx.NewClient(hc), r)
			require.Error(t, err)
			assert.Nil(t, resp)
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureTransport, pe.Kind)
			assert.Equal(t, int64(1), s.requests.Load(), "the POST must be sent exactly once")
			assert.Less(t, time.Since(start), r.Timeout, "the call must fail at once, not loop until the timeout")
		})
	}
}
