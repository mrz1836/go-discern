package clef_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
)

func ExampleNew() {
	// A local stand-in for Workers AI, so the example runs offline.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":{"model":"clef-flash","answers":{"queue":{"type":"choice","choice":"billing",`+
			`"probabilities":{"billing":0.9854,"technical":0.0146},"confidence":0.9423}},`+
			`"usage":{"input_tokens":145,"output_tokens":0}},"success":true,"errors":[],"messages":[]}`)
	}))
	defer srv.Close()

	client, err := clef.New("0123456789abcdef0123456789abcdef", "test-token",
		clef.WithModel(clef.ModelFlash),
		// Tests only: without these two options, calls go to api.cloudflare.com.
		clef.WithTestEndpoint(srv.URL),
		clef.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	ev, err := discern.New(discern.WithAdapter(client))
	if err != nil {
		fmt.Println(err)
		return
	}

	res, err := ev.Evaluate(context.Background(), discern.Request{
		Providers: discern.ClefOnly,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month."),
		Questions: []discern.Question{
			discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
				discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
				discern.Opt("technical", discern.Text("Errors, outages, integration problems"))),
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	obs, _ := res.Observation(discern.Clef)
	q, _ := obs.Choice("queue")
	fmt.Println(client, res.Status, q.Option)
	// Output: clef.Client{model: "clef-flash"} complete billing
}
