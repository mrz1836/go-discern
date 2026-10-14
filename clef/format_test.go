package clef_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestClientFormattingNeverLeaksSecrets(t *testing.T) {
	const endpoint = "https://127.0.0.1:8443"
	c, err := clef.New(testAccount, sentinelToken, clef.WithModel(clef.ModelFlash), clef.WithTestEndpoint(endpoint))
	require.NoError(t, err)
	secrets := []string{sentinelToken, testAccount, "Bearer", "127.0.0.1", "8443", "/client/v4", "@cf/cloudflare", "api.cloudflare.com"}

	// Most fmt verbs bypass String and GoString and print a Client's fields by
	// reflection, however a caller holds it.
	for _, shape := range testutil.Shapes(c) {
		t.Run(shape.Name, func(t *testing.T) {
			testutil.AssertNeverPrints(t, shape.Value, secrets...)
		})
	}

	t.Run("testify diff", func(t *testing.T) {
		const otherAccount = "fedcba9876543210fedcba9876543210"
		other, err := clef.New(otherAccount, "other-token", clef.WithTestEndpoint(endpoint))
		require.NoError(t, err)
		testutil.AssertDiffRedacted(t, c, other, append(secrets, otherAccount, "other-token")...)
	})
}
