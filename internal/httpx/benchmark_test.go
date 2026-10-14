package httpx_test

import (
	"testing"

	"github.com/mrz1836/go-discern/internal/httpx"
)

func BenchmarkDo(b *testing.B) {
	// The in-memory transport stands in for the network, so this measures the
	// local cost of one exchange: building the request, the client's own
	// bookkeeping and reading the body.
	const body = `{"model":"jev-1","answers":{"urgent":{"type":"noul","noul":0.25}}}`
	hc := staticClient(body, int64(len(body)))
	req := baseRequest("https://127.0.0.1/v1/systemone")
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := httpx.Do(ctx, hc, req); err != nil {
			b.Fatal(err)
		}
	}
}
