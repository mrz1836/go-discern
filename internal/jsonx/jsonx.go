// Package jsonx holds JSON primitives that encoding/json does not offer: a
// duplicate-key check and a string unquoter, both decoding escapes exactly as
// encoding/json does and allocating as little as the input allows, and a scan
// for the end of a string literal.
//
// It imports nothing from this module, so every package in it, the root package
// included, can use it.
package jsonx

import (
	"bytes"
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// linearKeys is the largest object whose keys are compared pairwise. Larger
// objects are sorted once, so a check stays O(n log n).
const linearKeys = 16

// frame is one open object or array during a scan. For an object, start is
// where its keys begin in the key stack, and wantKey is set while the next
// string is a key.
type frame struct {
	start   int
	object  bool
	wantKey bool
}

// HasDuplicateKey reports whether any object in data, at any depth, has two keys
// that are equal once unescaped, so "a" and "\u0061" collide. It is
// DuplicateKeyEnd(data) >= 0.
//
// data must be valid JSON (json.Valid) and valid UTF-8 (utf8.Valid). For any
// other input the result is unspecified, but the call never panics.
func HasDuplicateKey(data []byte) bool { return DuplicateKeyEnd(data) >= 0 }

// DuplicateKeyEnd returns the offset just past the first object, in the order
// objects close, that has two keys equal once unescaped, or -1 when there is
// none. It has the same preconditions as HasDuplicateKey.
//
// A document nested at most 16 levels deep, with at most 32 keys open at once
// (the keys of every object not yet closed), is checked without allocating.
// With more open keys, one extra pass counts them and one int per key is
// allocated, once: memory grows with the keys, never with the bytes of the
// document, and keys with escapes are compared in place rather than decoded
// into copies.
func DuplicateKeyEnd(data []byte) int {
	var (
		frameBuf [16]frame
		keyBuf   [32]int
	)
	return scan(data, frameBuf[:0], keyBuf[:0])
}

// scan does the work of DuplicateKeyEnd. keys holds the offset of each open
// key's opening quote. The first time keys fills its capacity, it is grown to
// the exact peak that peakKeys reports, so a valid document never regrows it.
// The helpers take and return slices rather than sharing a walker struct, which
// keeps the caller's buffers on the stack.
func scan(data []byte, frames []frame, keys []int) int {
	grown := false
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '{':
			frames = append(frames, frame{start: len(keys), object: true, wantKey: true})
		case '[':
			frames = append(frames, frame{})
		case '}', ']':
			n := len(frames) - 1
			if n < 0 {
				return -1 // an unbalanced closer: not valid JSON
			}
			var dup bool
			if keys, dup = closeFrame(data, frames[n], keys); dup {
				return i + 1
			}
			frames = frames[:n]
		case ',':
			markKey(frames)
		case '"':
			end := StringEnd(data, i)
			if end < 0 {
				return -1 // an unterminated string: not valid JSON
			}
			if takeKey(frames) {
				keys, grown = pushKey(data, keys, i, grown)
			}
			i = end - 1
		}
		// Whitespace, colons and the bytes of numbers and literals need no
		// tracking.
	}
	return -1
}

// closeFrame ends the frame f. An object's keys are checked for a duplicate,
// reported by dup, and then dropped from the key stack.
func closeFrame(data []byte, f frame, keys []int) (rest []int, dup bool) {
	if !f.object {
		return keys, false
	}
	if duplicated(data, keys[f.start:]) {
		return keys, true
	}
	return keys[:f.start], false
}

// pushKey appends the key whose opening quote is at data[i]. The first time
// keys is full, it grows to the exact peak peakKeys reports, so that a valid
// document never grows it again; grown records that this happened.
func pushKey(data []byte, keys []int, i int, grown bool) ([]int, bool) {
	if len(keys) == cap(keys) && !grown {
		keys, grown = slices.Grow(keys, max(peakKeys(data)-len(keys), 1)), true
	}
	return append(keys, i), grown
}

// peakKeys returns the largest number of keys open at once while data is
// scanned: the keys of every object that has opened and not yet closed. It
// tracks the same frames as scan but stores no keys.
func peakKeys(data []byte) int {
	var frameBuf [16]frame
	frames := frameBuf[:0]
	open, peak := 0, 0
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '{':
			frames = append(frames, frame{start: open, object: true, wantKey: true})
		case '[':
			frames = append(frames, frame{})
		case '}', ']':
			n := len(frames) - 1
			if n < 0 {
				return peak
			}
			if frames[n].object {
				open = frames[n].start
			}
			frames = frames[:n]
		case ',':
			markKey(frames)
		case '"':
			end := StringEnd(data, i)
			if end < 0 {
				return peak
			}
			if takeKey(frames) {
				open++
				peak = max(peak, open)
			}
			i = end - 1
		}
	}
	return peak
}

// markKey records a comma: in an object, the next string is a key.
func markKey(frames []frame) {
	if n := len(frames) - 1; n >= 0 && frames[n].object {
		frames[n].wantKey = true
	}
}

// takeKey reports whether the string being scanned is a key, and if so marks
// the innermost object as no longer expecting one.
func takeKey(frames []frame) bool {
	n := len(frames) - 1
	if n < 0 || !frames[n].wantKey {
		return false
	}
	frames[n].wantKey = false
	return true
}

// duplicated reports whether two of keys, offsets of opening quotes in data,
// decode to the same string. It may reorder keys.
func duplicated(data []byte, keys []int) bool {
	if len(keys) <= linearKeys {
		for i := 1; i < len(keys); i++ {
			for j := range i {
				if compareKeys(data, keys[i], keys[j]) == 0 {
					return true
				}
			}
		}
		return false
	}
	slices.SortFunc(keys, func(a, b int) int { return compareKeys(data, a, b) })
	for i := 1; i < len(keys); i++ {
		if compareKeys(data, keys[i-1], keys[i]) == 0 {
			return true
		}
	}
	return false
}

// compareKeys orders the decoded contents of the JSON strings whose opening
// quotes are at data[a] and data[b], byte by byte, as bytes.Compare would order
// the decoded strings. Identical raw bytes are skipped in bulk; escapes are
// decoded as they are reached, without copying either string.
func compareKeys(data []byte, a, b int) int {
	i, j := a+1, b+1
	for i < len(data) && j < len(data) {
		x := data[i]
		if x != data[j] || x == '"' || x == '\\' {
			break
		}
		i, j = i+1, j+1
	}
	x, y := cursor{i: i}, cursor{i: j}
	for {
		cx, okx := x.next(data)
		cy, oky := y.next(data)
		switch {
		case !okx || !oky:
			return boolOrder(okx) - boolOrder(oky) // the shorter string sorts first
		case cx != cy:
			return int(cx) - int(cy)
		}
	}
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cursor yields the decoded bytes of one JSON string from data[i] up to its
// closing quote. A decoded escape is held in buf until all its bytes are read.
// Raw bytes are yielded as they are, which matches encoding/json for valid
// UTF-8.
type cursor struct {
	i    int
	buf  [utf8.UTFMax]byte
	k, n int
}

// next returns the next decoded byte, or false at the closing quote. An
// invalid escape, or the end of data, also ends the string; neither occurs for
// the keys of a valid document.
func (c *cursor) next(data []byte) (byte, bool) {
	if c.k < c.n {
		b := c.buf[c.k]
		c.k++
		return b, true
	}
	if c.i < len(data) && data[c.i] != '"' {
		if b := data[c.i]; b != '\\' {
			c.i++
			return b, true
		}
		if r, next := unescape(data, c.i); next >= 0 {
			c.i = next
			c.n, c.k = utf8.EncodeRune(c.buf[:], r), 1
			return c.buf[0], true
		}
	}
	return 0, false
}

// AppendUnquoted appends the content of the JSON string literal s, quotes
// included, to dst, decoded exactly as encoding/json decodes it: escapes are
// resolved, a valid surrogate pair becomes one rune, and a lone surrogate or a
// byte that is not valid UTF-8 becomes U+FFFD. It reports false, returning dst
// unchanged, when s is not a valid JSON string literal. It allocates only when
// dst lacks capacity.
func AppendUnquoted(dst, s []byte) ([]byte, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return dst, false
	}
	out, end := len(dst), len(s)-1
	for i := 1; i < end; {
		switch c := s[i]; {
		case c == '\\':
			r, next := unescape(s[:end], i)
			if next < 0 {
				return dst[:out], false
			}
			dst, i = utf8.AppendRune(dst, r), next
		case c == '"', c < ' ':
			return dst[:out], false
		case c < utf8.RuneSelf:
			// Copy a run of plain ASCII at once.
			j := i + 1
			for j < end && s[j] >= ' ' && s[j] < utf8.RuneSelf && s[j] != '"' && s[j] != '\\' {
				j++
			}
			dst, i = append(dst, s[i:j]...), j
		default:
			r, size := utf8.DecodeRune(s[i:end])
			if r == utf8.RuneError && size == 1 {
				dst = utf8.AppendRune(dst, utf8.RuneError)
			} else {
				dst = append(dst, s[i:i+size]...)
			}
			i += size
		}
	}
	return dst, true
}

// unescape decodes the escape sequence at s[i], a backslash, and returns its
// rune and the index just past it, or a negative index for an invalid escape.
// \ud83d\ude00 is one rune; a surrogate not paired that way is U+FFFD, and the
// escape after it is decoded on its own, as encoding/json does.
func unescape(s []byte, i int) (rune, int) {
	if i+1 >= len(s) {
		return 0, -1
	}
	switch c := s[i+1]; c {
	case '"', '\\', '/':
		return rune(c), i + 2
	case 'b':
		return '\b', i + 2
	case 'f':
		return '\f', i + 2
	case 'n':
		return '\n', i + 2
	case 'r':
		return '\r', i + 2
	case 't':
		return '\t', i + 2
	case 'u':
		r := hex4(s, i)
		if r < 0 {
			return 0, -1
		}
		if !utf16.IsSurrogate(r) {
			return r, i + 6
		}
		if pair := utf16.DecodeRune(r, hex4(s, i+6)); pair != utf8.RuneError {
			return pair, i + 12
		}
		return utf8.RuneError, i + 6
	}
	return 0, -1
}

// hex4 returns the value of the \uXXXX escape at s[i], or -1.
func hex4(s []byte, i int) rune {
	if i+6 > len(s) || s[i] != '\\' || s[i+1] != 'u' {
		return -1
	}
	var r rune
	for _, c := range s[i+2 : i+6] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c -= 'a' - 10
		case c >= 'A' && c <= 'F':
			c -= 'A' - 10
		default:
			return -1
		}
		r = r<<4 | rune(c)
	}
	return r
}

// StringEnd returns the index just past the JSON string whose opening quote is
// at data[i], or -1 if it is not terminated. It jumps from quote to quote; a
// quote ends the string unless an odd run of backslashes escapes it. It checks
// nothing else, so data[i:] should come from a valid document.
func StringEnd(data []byte, i int) int {
	for j := i + 1; ; {
		k := bytes.IndexByte(data[j:], '"')
		if k < 0 {
			return -1
		}
		q := j + k
		run := 0
		for b := q - 1; b > i && data[b] == '\\'; b-- {
			run++
		}
		if run%2 == 0 {
			return q + 1
		}
		j = q + 1
	}
}
