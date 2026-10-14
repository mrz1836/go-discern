package systemone_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/systemone"
)

// These benchmarks measure local CPU and memory only: encoding and strict
// decoding, with no I/O.

// largeState returns a synthetic JSON support-ticket thread of about n bytes.
func largeState(n int) []byte {
	var buf bytes.Buffer
	buf.Grow(n + 128)
	buf.WriteString(`{"messages":[`)
	for i := 0; buf.Len() < n; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"id":%d,"text":"I was charged twice for invoice %d, please refund the duplicate."}`, i, i)
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}

// choiceRequest is a one-question request whose Choice has n options, o000 to
// o<n-1>.
func choiceRequest(n int) *discern.Request {
	opts := make([]discern.ChoiceOption, n)
	for i := range opts {
		opts[i] = discern.Opt(fmt.Sprintf("o%03d", i), discern.Text("Route to queue "+fmt.Sprint(i)))
	}
	return &discern.Request{
		Providers: discern.JevOnly,
		State:     discern.Text("Customer: I was charged twice."),
		Questions: []discern.Question{discern.Choice("queue", discern.Text("Which queue?"), opts...)},
	}
}

// choiceResponse answers choiceRequest(n), putting all weight on o000.
func choiceResponse(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"jev-1","answers":{"queue":{"type":"choice","choice":"o000","probabilities":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		p := 0
		if i == 0 {
			p = 1
		}
		fmt.Fprintf(&b, `"o%03d":%d`, i, p)
	}
	b.WriteString(`},"confidence":0.9}},"usage":{"input_tokens":120,"output_tokens":3}}`)
	return []byte(b.String())
}

// envelopeBody wraps validBody in a successful Cloudflare envelope.
func envelopeBody() []byte {
	return []byte(`{"result":` + validBody + `,"success":true,"errors":[],"messages":[]}`)
}

func BenchmarkEncodeRequest(b *testing.B) {
	req := ticketRequest()
	if _, err := systemone.EncodeRequest(testModel, req); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = systemone.EncodeRequest(testModel, req)
	}
}

func BenchmarkEncodeRequestLargeState(b *testing.B) {
	req := ticketRequest()
	req.State = discern.JSON(largeState(4 << 20))
	body, err := systemone.EncodeRequest(testModel, req)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		_, _ = systemone.EncodeRequest(testModel, req)
	}
}

func BenchmarkDecodeResult(b *testing.B) {
	req := ticketRequest()
	data := []byte(validBody)
	if _, err := systemone.DecodeResult(data, req); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		_, _ = systemone.DecodeResult(data, req)
	}
}

func BenchmarkDecodeResult255Options(b *testing.B) {
	req := choiceRequest(255)
	data := choiceResponse(255)
	if _, err := systemone.DecodeResult(data, req); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		_, _ = systemone.DecodeResult(data, req)
	}
}

func BenchmarkDecodeEnvelope(b *testing.B) {
	data := envelopeBody()
	if _, err := systemone.DecodeEnvelope(data); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		_, _ = systemone.DecodeEnvelope(data)
	}
}

func BenchmarkDecodeEnvelopeResult(b *testing.B) {
	req := ticketRequest()
	data := envelopeBody()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		env, err := systemone.DecodeEnvelope(data)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = env.DecodeResult(req); err != nil {
			b.Fatal(err)
		}
	}
}
