package systemone

import (
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/mrz1836/go-discern"
)

// Retention limits for errors[]. Entries beyond the first maxRetainedErrors
// codes or messages are counted but not kept, and each kept message is cut to
// maxMessageBytes, so a refusal holds a bounded amount of provider text however
// large the response.
const (
	maxRetainedErrors = 64
	maxMessageBytes   = 1 << 10
)

// envelopeMembers is the member capacity DecodeEnvelope reserves: the three
// envelope fields it reads and the two fields of the errors[] entry being read.
const envelopeMembers = 5

// Envelope is a decoded Cloudflare REST envelope.
type Envelope struct {
	// Success is the envelope's required success flag.
	Success bool

	// Result is the raw "result" value, aliasing the decoded data; nil when
	// absent or null.
	Result json.RawMessage

	// ErrorCount is the number of errors[] entries, counting entries that
	// carry neither a code nor a message and entries beyond the retained ones.
	// Any entry is a refusal.
	ErrorCount int

	// Codes are the first 64 errors[].code values, in order. Entries without a
	// code are skipped.
	Codes []int

	// Messages are the first 64 errors[].message values, in order, each cut to
	// at most 1 KiB at a UTF-8 boundary. They are provider-supplied text that may
	// echo request content: expose them only through an explicit opt-in, never
	// in error strings.
	Messages []string

	// checked is Result as DecodeEnvelope set it, already checked strictly as
	// part of the envelope.
	checked json.RawMessage
}

// DecodeEnvelope strictly decodes a Cloudflare envelope: the input must be one
// JSON object without duplicate keys or trailing data, "success" must be a
// boolean, and "result" is kept raw. Other fields, including "messages", are
// ignored. Every error wraps ErrMalformed.
//
// "success" is read before "errors", so an envelope with success:false always
// decodes, as the refusal it is: its errors[] entries are read as DecodeRefusal
// reads them, keeping the codes and messages it can find and ignoring entries
// of any other shape. With success:true, "errors", when present and not null,
// must be an array of objects whose optional "code" is an integer and optional
// "message" is a string; any entry still makes the envelope a refusal.
func DecodeEnvelope(data []byte) (*Envelope, error) {
	if err := checkStrict(data); err != nil {
		return nil, err
	}
	d := decoder{members: make([]member, 0, envelopeMembers)}
	top, err := d.fields(data, field{key: "envelope"}, "success", "result", "errors")
	if err != nil {
		return nil, err
	}
	env := &Envelope{}
	sf := field{key: "success"}
	raw, ok := top.get(sf.key)
	if !ok {
		return nil, malformed(sf, "is required")
	}
	switch {
	case string(raw) == "true":
		env.Success = true
	case string(raw) != "false":
		return nil, malformed(sf, "must be a boolean")
	}
	if res, present := top.lookup(field{key: "result"}); present {
		env.Result, env.checked = res, res
	}
	errs, present := top.lookup(field{key: "errors"})
	switch {
	case !present:
	case env.Success:
		if err = env.strictErrors(&d, errs); err != nil {
			return nil, err
		}
	default:
		env.lenientErrors(errs)
	}
	return env, nil
}

// DecodeRefusal reads Cloudflare's error codes and messages from the body of a
// refused call, best effort: it never fails. A body that is a JSON object with an
// "errors" array yields the integer codes and string messages found in that
// array's entries, as DecodeEnvelope keeps them (the first 64 of each, messages
// cut to 1 KiB); "success" and every other field are ignored. A string entry is
// taken as a message. Anything else, such as an HTML error page, invalid UTF-8,
// an empty body or an entry of another shape, contributes nothing. When a key
// repeats, the last occurrence counts, as in encoding/json.
func DecodeRefusal(data []byte) (codes []int, messages []string) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, nil
	}
	var errs []byte
	r := readObject(data)
	for key, value, more := r.next(); more; key, value, more = r.next() {
		if keyIn(key, "errors") {
			errs = value
		}
	}
	var env Envelope
	env.lenientErrors(errs)
	return env.Codes, env.Messages
}

// DecodeResult strictly decodes the envelope's result, exactly like the
// package-level DecodeResult. A Result still as DecodeEnvelope set it was
// checked with the envelope, so its bytes are not walked a second time; a
// replaced Result is checked in full.
func (e *Envelope) DecodeResult(req *discern.Request) (*discern.Observation, error) {
	if req == nil || !sameSlice(e.Result, e.checked) {
		return DecodeResult(e.Result, req)
	}
	return decodeResult(e.Result, req)
}

// strictErrors reads the errors[] of a successful envelope into ErrorCount,
// Codes and Messages, rejecting entries that are not objects and codes or
// messages of the wrong type. Each entry reuses the member storage of the one
// before.
func (e *Envelope) strictErrors(d *decoder, raw json.RawMessage) error {
	items := readArray(raw)
	mark := len(d.members)
	for i := 0; ; i++ {
		item, more := items.next()
		if !more {
			break
		}
		e.ErrorCount++
		if err := e.strictError(d, item, i); err != nil {
			return err
		}
		d.members = d.members[:mark]
	}
	if !items.ok {
		return malformed(field{key: "errors"}, "must be an array")
	}
	return nil
}

// strictError reads errors[i]. Its field path is formatted only on failure.
func (e *Envelope) strictError(d *decoder, raw json.RawMessage, i int) error {
	obj, reason := d.parse(raw, math.MaxInt, []string{"code", "message"})
	if reason != "" {
		return malformed(errorField(i), reason)
	}
	if c, present := obj.lookup(field{key: "code"}); present {
		n, reason := parseInt(c, strconv.IntSize)
		if reason != "" {
			return malformed(errorField(i).member("code"), reason)
		}
		e.addCode(int(n))
	}
	if m, present := obj.lookup(field{key: "message"}); present {
		if firstByte(m) != '"' {
			return malformed(errorField(i).member("message"), "must be a string")
		}
		e.addMessage(m)
	}
	return nil
}

// errorField names errors[i].
func errorField(i int) field { return field{key: "errors[" + strconv.Itoa(i) + "]"} }

// lenientErrors reads whatever codes and messages raw, an errors[] value of any
// shape, holds, ignoring everything else. It never fails and stores nothing but
// what it keeps.
func (e *Envelope) lenientErrors(raw []byte) {
	items := readArray(raw)
	for item, more := items.next(); more; item, more = items.next() {
		e.ErrorCount++
		switch firstByte(item) {
		case '"':
			e.addMessage(item)
		case '{':
			var code, message []byte
			members := readObject(item)
			for key, value, more := members.next(); more; key, value, more = members.next() {
				switch {
				case keyIn(key, "code"):
					code = value
				case keyIn(key, "message"):
					message = value
				}
			}
			if n, reason := parseInt(code, strconv.IntSize); reason == "" {
				e.addCode(int(n))
			}
			if firstByte(message) == '"' {
				e.addMessage(message)
			}
		}
	}
}

// addCode keeps code unless maxRetainedErrors codes are already kept.
func (e *Envelope) addCode(code int) {
	if len(e.Codes) < maxRetainedErrors {
		e.Codes = append(e.Codes, code)
	}
}

// addMessage keeps the JSON string lit, decoded and cut to maxMessageBytes at a
// UTF-8 boundary, unless maxRetainedErrors messages are already kept. Messages
// past the limit are not even decoded.
func (e *Envelope) addMessage(lit []byte) {
	if len(e.Messages) >= maxRetainedErrors {
		return
	}
	text, ok := unquoteKey(lit)
	if !ok {
		return
	}
	e.keepMessage(text)
}

// keepMessage keeps text, cut to maxMessageBytes at a UTF-8 boundary. Callers
// check maxRetainedErrors first, so that no message past the limit is decoded.
func (e *Envelope) keepMessage(text []byte) {
	if len(text) > maxMessageBytes {
		cut := maxMessageBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	e.Messages = append(e.Messages, string(text))
}

// sameSlice reports whether a and b are the same window onto the same memory.
func sameSlice(a, b []byte) bool {
	return len(a) == len(b) && cap(a) == cap(b) && (len(a) == 0 || &a[0] == &b[0])
}
