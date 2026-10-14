package clef_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/clef"
)

func BenchmarkPrepare(b *testing.B) {
	c := okTransport(envelope(ticketResult(clef.ModelClef))).client(b)
	req := ticketRequest()
	b.ReportAllocs()
	for b.Loop() {
		if _, perr := c.Prepare(&req); perr != nil {
			b.Fatal(perr)
		}
	}
}

func BenchmarkCall(b *testing.B) {
	c := okTransport(envelope(ticketResult(clef.ModelClef))).client(b)
	req := ticketRequest()
	call, err := c.Prepare(&req)
	require.NoError(b, err)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, cerr := call(ctx); cerr != nil {
			b.Fatal(cerr)
		}
	}
}
