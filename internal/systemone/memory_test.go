package systemone_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/systemone"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestAllocationsStayBounded(t *testing.T) {
	testutil.SkipUnderRace(t)
	req := ticketRequest()
	body := []byte(validBody)
	envelope := envelopeBody()
	// Unknown members cost nothing per member, however their keys are spelled:
	// only the scanner's key stack is allocated, once.
	var unknown strings.Builder
	for i := range 200 {
		fmt.Fprintf(&unknown, testutil.Esc(`"~u0078%d":[1],`), i)
	}
	withUnknown := []byte(strings.Replace(validBody, `{"model":`, `{`+unknown.String()+`"model":`, 1))

	// Ceilings sit about 25% above the measured counts, so that a regression,
	// such as per-key or per-field allocations on the success path, fails
	// loudly while toolchain noise does not.
	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "EncodeRequest", Max: 3, Run: func() { _, _ = systemone.EncodeRequest(testModel, req) }},
		{Name: "DecodeResult", Max: 18, Run: func() { _, _ = systemone.DecodeResult(body, req) }},
		{Name: "DecodeResult with escaped unknown members", Max: 19, Run: func() { _, _ = systemone.DecodeResult(withUnknown, req) }},
		{Name: "DecodeEnvelope", Max: 3, Run: func() { _, _ = systemone.DecodeEnvelope(envelope) }},
		{Name: "DecodeEnvelope and its result", Max: 20, Run: func() {
			env, err := systemone.DecodeEnvelope(envelope)
			if err == nil {
				_, _ = env.DecodeResult(req)
			}
		}},
	})
}

func TestChoiceAllocationsDoNotGrowPerOption(t *testing.T) {
	testutil.SkipUnderRace(t)
	measure := func(n int) (encode, decode float64) {
		req := choiceRequest(n)
		data := choiceResponse(n)
		_, err := systemone.DecodeResult(data, req)
		require.NoError(t, err)
		encode = testing.AllocsPerRun(100, func() { _, _ = systemone.EncodeRequest(testModel, req) })
		decode = testing.AllocsPerRun(100, func() { _, _ = systemone.DecodeResult(data, req) })
		return encode, decode
	}
	smallEncode, smallDecode := measure(4)
	largeEncode, largeDecode := measure(255)
	t.Logf("encode: %v allocations with 4 options, %v with 255", smallEncode, largeEncode)
	t.Logf("decode: %v allocations with 4 options, %v with 255", smallDecode, largeDecode)
	// Buffers and the ID set grow geometrically, so 251 more options may cost a
	// few allocations, never one or more per option.
	assert.Less(t, largeEncode-smallEncode, 16.0)
	assert.Less(t, largeDecode-smallDecode, 16.0)
}

func TestEncodeRequestLargeStateAllocatesOneBody(t *testing.T) {
	testutil.SkipUnderRace(t)
	req := ticketRequest()
	req.State = discern.JSON(largeState(4 << 20))
	body, err := systemone.EncodeRequest(testModel, req)
	require.NoError(t, err)

	perOp := testutil.BytesPerOp(func() { _, _ = systemone.EncodeRequest(testModel, req) })
	t.Logf("%d bytes allocated for a %d-byte body", perOp, len(body))
	// One body, plus the small questions buffer; never a second, regrown copy.
	assert.Less(t, perOp, int64(len(body))+int64(len(body))/2)
}

func TestDecodeLargeUnknownFieldDoesNotCopy(t *testing.T) {
	testutil.SkipUnderRace(t)
	req := ticketRequest()
	data := []byte(strings.Replace(validBody, `{"model":`, `{"transcript":"`+strings.Repeat("refund please ", 150_000)+`","model":`, 1))
	obs, err := systemone.DecodeResult(data, req)
	require.NoError(t, err)
	assert.Equal(t, validObservation(), obs)

	perOp := testutil.BytesPerOp(func() { _, _ = systemone.DecodeResult(data, req) })
	t.Logf("%d bytes allocated to decode a %d-byte body", perOp, len(data))
	// Members alias the body, so an ignored field costs no copy at all.
	assert.Less(t, perOp, int64(len(data))/16)
}

// adversarialSize is the body size of the adversarial responses: the default
// response limit.
const adversarialSize = 4 << 20

// fill returns prefix, then item(i) for i = 0, 1, ... joined by commas until
// the body reaches adversarialSize bytes, then suffix.
func fill(prefix string, item func(i int) string, suffix string) []byte {
	var b strings.Builder
	b.Grow(adversarialSize + len(suffix) + 64)
	b.WriteString(prefix)
	for i := 0; b.Len() < adversarialSize; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(item(i))
	}
	b.WriteString(suffix)
	return []byte(b.String())
}

func TestAdversarialResponsesStayWithinFourTimesTheirSize(t *testing.T) {
	testutil.SkipUnderRace(t)
	req := ticketRequest()
	answers := `"answers":{"queue":{"type":"choice","choice":"billing","probabilities":{"account":0.05,"billing":0.9,"technical":0.05}},` +
		`"urgent":{"type":"noul","noul":0.25},"tone":{"type":"score","score":1,"probabilities":{"0":0,"1":1,"2":0}}}`
	key := func(i int) string { return fmt.Sprintf(`"%x":0`, i) }
	escapedKey := func(i int) string { return fmt.Sprintf(testutil.Esc(`"~u0061%x":0`), i) }
	decode := func(body []byte) func() { return func() { _, _ = systemone.DecodeResult(body, req) } }
	envelope := func(body []byte) func() { return func() { _, _ = systemone.DecodeEnvelope(body) } }
	refusal := func(body []byte) func() { return func() { _, _ = systemone.DecodeRefusal(body) } }
	errorItem := func(int) string { return `{"code":3040,"message":"Capacity temporarily exceeded"}` }
	tests := []struct {
		name string
		body []byte
		run  func(body []byte) func()
	}{
		{name: "many top-level keys", body: fill(`{`+answers+`,`, key, `}`), run: decode},
		{name: "many escaped top-level keys", body: fill(`{`+answers+`,`, escapedKey, `}`), run: decode},
		{name: "many keys in an unknown object", body: fill(`{`+answers+`,"extra":{`, key, `}}`), run: decode},
		{name: "many keys in one answer", body: fill(`{"answers":{"urgent":{"type":"noul","noul":0.25,`, key, `}}}`), run: decode},
		{name: "many answer keys", body: fill(`{"answers":{`, key, `}}`), run: decode},
		{name: "many probability keys", body: fill(`{"answers":{"queue":{"type":"choice","choice":"billing","probabilities":{`, key, `}}}}`), run: decode},
		{name: "many legend keys", body: fill(`{"answers":{"tone":{"type":"score","score":1,"probabilities":{"0":0,"1":1,"2":0},"legend":{`, key, `}}}}`), run: decode},
		{name: "many errors in a refusal envelope", body: fill(`{"success":false,"errors":[`, errorItem, `]}`), run: envelope},
		{name: "many empty errors in a refusal envelope", body: fill(`{"success":false,"errors":[`, func(int) string { return `{}` }, `]}`), run: envelope},
		{name: "many errors in a successful envelope", body: fill(`{"success":true,"errors":[`, errorItem, `]}`), run: envelope},
		{name: "many keys in one error", body: fill(`{"success":true,"errors":[{`, key, `}]}`), run: envelope},
		{name: "many envelope keys", body: fill(`{"success":false,`, key, `}`), run: envelope},
		{name: "one huge escaped message", body: fill(`{"success":false,"errors":[{"message":"`, func(int) string { return `\n\n\n\n\n\n` }, `"}]}`), run: envelope},
		{name: "many errors in a refusal body", body: fill(`{"errors":[`, errorItem, `]}`), run: refusal},
		{name: "many string errors in a refusal body", body: fill(`{"errors":[`, func(i int) string { return fmt.Sprintf(`"m%d"`, i) }, `]}`), run: refusal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One run after a warm-up; the bytes allocated also bound its heap
			// growth.
			_, allocated := testutil.AllocStats(1, tt.run(tt.body))
			t.Logf("%.0f bytes allocated for a %d-byte body (%.2fx)", allocated, len(tt.body), allocated/float64(len(tt.body)))
			assert.LessOrEqual(t, allocated, float64(4*len(tt.body)))
		})
	}
}
