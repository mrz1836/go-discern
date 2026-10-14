package jev_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// These benchmarks measure local CPU work only (encoding, the in-memory HTTP
// exchange and strict decoding); they say nothing about network latency.

func BenchmarkPrepare(b *testing.B) {
	c := inMemory(b, respond(http.StatusOK, ticketResponse))
	req := ticketRequest()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Prepare(&req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCall(b *testing.B) {
	c := inMemory(b, respond(http.StatusOK, ticketResponse))
	req := ticketRequest()
	call, err := c.Prepare(&req)
	require.NoError(b, err)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, callErr := call(ctx); callErr != nil {
			b.Fatal(callErr)
		}
	}
}
