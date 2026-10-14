package jev_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

func TestClientFormattingNeverLeaksSecrets(t *testing.T) {
	const endpoint = "https://127.0.0.1:8443"
	c, err := jev.New(sentinelKey, jev.WithModel(pinnedModel), jev.WithTestEndpoint(endpoint))
	require.NoError(t, err)
	secrets := []string{sentinelKey, "Bearer", "127.0.0.1", "8443", systemOnePath, "api.typesafe.ai"}

	// Most fmt verbs bypass String and GoString and print a Client's fields by
	// reflection, however a caller holds it.
	for _, shape := range testutil.Shapes(c) {
		t.Run(shape.Name, func(t *testing.T) {
			testutil.AssertNeverPrints(t, shape.Value, secrets...)
		})
	}

	t.Run("testify diff", func(t *testing.T) {
		other, err := jev.New("other-key", jev.WithModel(jev.DefaultModel), jev.WithTestEndpoint(endpoint))
		require.NoError(t, err)
		testutil.AssertDiffRedacted(t, c, other, append(secrets, "other-key")...)
	})
}
