package jev_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/jev"
)

func ExampleNew() {
	// A local TLS server stands in for api.typesafe.ai, so the example runs offline.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-typesafe-request-id", "req-example")
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"queue":{"type":"choice","choice":"billing",`+
			`"probabilities":{"billing":0.91,"technical":0.06,"account":0.03},"confidence":0.87}},`+
			`"usage":{"input_tokens":96,"output_tokens":0}}`)
	}))
	defer srv.Close()

	// In production: jev.New(apiKey, jev.WithModel("jev-1.13.0")).
	jc, err := jev.New("test-key",
		jev.WithModel("jev-1.13.0"),
		jev.WithTestEndpoint(srv.URL),
		jev.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	ev, err := discern.New(discern.WithAdapter(jc))
	if err != nil {
		fmt.Println(err)
		return
	}

	res, err := ev.Evaluate(context.Background(), discern.Request{
		Providers: discern.JevOnly,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month."),
		Questions: []discern.Question{
			discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
				discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
				discern.Opt("technical", discern.Text("Errors, outages, integration problems")),
				discern.Opt("account", discern.Text("Login, profile, permissions"))),
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	obs, _ := res.Observation(discern.Jev)
	queue, _ := obs.Choice("queue")
	fmt.Println(res.Status, queue.Option)
	fmt.Println(res.Outcomes[0].RequestedModel, obs.Model, obs.RequestID)
	// Output:
	// complete billing
	// jev-1.13.0 jev-1.13.0 req-example
}
