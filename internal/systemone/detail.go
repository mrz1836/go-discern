package systemone

import (
	"encoding/json"
	"unicode/utf8"
)

// DecodeDetailRefusal reads the messages from the body of a refusal in the
// FastAPI "detail" style that Jev sends but does not document:
//
//	{"detail":{"error_type":"api_usage_error","message":"Unknown model: jev"}}
//	{"detail":[{"loc":["body","model"],"msg":"Field required","type":"missing"}]}
//	{"detail":"Not Found"}
//
// Each error becomes one message: "error_type: message" for an object detail,
// "type: msg" for each object in a detail array, or whichever of the two is a
// non-empty string; a string detail is the message itself. Messages are kept as
// DecodeRefusal keeps Cloudflare's (the first 64, each cut to 1 KiB at a UTF-8
// boundary), and there are no codes. Anything else, such as an HTML error page,
// invalid UTF-8, an empty body or an entry of another shape, contributes
// nothing. When a key repeats, the last occurrence counts, as in encoding/json.
func DecodeDetailRefusal(data []byte) (codes []int, messages []string) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, nil
	}
	var detail []byte
	r := readObject(data)
	for key, value, more := r.next(); more; key, value, more = r.next() {
		if keyIn(key, "detail") {
			detail = value
		}
	}
	var env Envelope
	switch firstByte(detail) {
	case '"':
		if text, ok := unquoteKey(detail); ok && len(text) > 0 {
			env.keepMessage(text)
		}
	case '{':
		env.addDetail(detail, "error_type", "message")
	case '[':
		items := readArray(detail)
		for item, more := items.next(); more; item, more = items.next() {
			env.addDetail(item, "type", "msg")
		}
	}
	return nil, env.Messages
}

// addDetail keeps one message for the error object raw: the strings under
// kindKey and textKey joined as "kind: text", or whichever of them is a
// non-empty string. raw of another shape contributes nothing, and nothing is
// decoded once maxRetainedErrors messages are kept.
func (e *Envelope) addDetail(raw []byte, kindKey, textKey string) {
	if len(e.Messages) >= maxRetainedErrors || firstByte(raw) != '{' {
		return
	}
	var kind, text []byte
	members := readObject(raw)
	for key, value, more := members.next(); more; key, value, more = members.next() {
		switch {
		case keyIn(key, kindKey):
			kind = value
		case keyIn(key, textKey):
			text = value
		}
	}
	k, t := stringContent(kind), stringContent(text)
	switch {
	case len(k) > 0 && len(t) > 0:
		joined := make([]byte, 0, len(k)+2+len(t))
		joined = append(append(append(joined, k...), ": "...), t...)
		e.keepMessage(joined)
	case len(k) > 0:
		e.keepMessage(k)
	case len(t) > 0:
		e.keepMessage(t)
	}
}

// stringContent returns the decoded content of raw when it is a JSON string,
// else nil.
func stringContent(raw []byte) []byte {
	if firstByte(raw) != '"' {
		return nil
	}
	text, _ := unquoteKey(raw)
	return text
}
