package testutil

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// FormatVerbs returns the fmt verbs a caller might apply to a value. Most of
// them bypass String and GoString and print fields by reflection, which
// dereferences pointers and reaches unexported fields.
func FormatVerbs() []string {
	return []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%t", "%d", "%b", "%o", "%e", "%c", "%U", "%p"}
}

// Shape is one way a caller might hold a value, named for test output.
type Shape struct {
	Name  string
	Value any
}

// exportedFields and unexportedFields hold a value and a pointer to it the way
// caller structs do. fmt reaches unexported fields only by reflection, never
// through String.
type exportedFields[T any] struct {
	V T
	P *T
}

type unexportedFields[T any] struct {
	v T
	p *T
}

// Shapes returns the ways a caller might hold p: the pointer, the value, a
// pointer to the pointer, exported and unexported struct fields, and slices
// and maps of pointers and values.
func Shapes[T any](p *T) []Shape {
	return []Shape{
		{Name: "pointer", Value: p},
		{Name: "value", Value: *p},
		{Name: "pointer to pointer", Value: &p},
		{Name: "exported fields", Value: exportedFields[T]{V: *p, P: p}},
		{Name: "unexported fields", Value: unexportedFields[T]{v: *p, p: p}},
		{Name: "pointer to unexported fields", Value: &unexportedFields[T]{v: *p, p: p}},
		{Name: "slice of pointers", Value: []*T{p}},
		{Name: "slice of values", Value: []T{*p}},
		{Name: "map of pointers", Value: map[string]*T{"k": p}},
		{Name: "map of values", Value: map[string]T{"k": *p}},
	}
}

// AssertRedacted fails tb when out contains any secret, in plain text or as hex
// in either case. msgAndArgs annotate a failure.
func AssertRedacted(tb testing.TB, out string, secrets []string, msgAndArgs ...any) bool {
	tb.Helper()
	ok := true
	for _, s := range secrets {
		h := hex.EncodeToString([]byte(s))
		for _, form := range []string{s, h, strings.ToUpper(h)} {
			ok = assert.NotContains(tb, out, form, msgAndArgs...) && ok
		}
	}
	return ok
}

// AssertNeverPrints formats v with every verb of FormatVerbs and fails tb when
// any output holds a secret, as AssertRedacted checks.
func AssertNeverPrints(tb testing.TB, v any, secrets ...string) bool {
	tb.Helper()
	ok := true
	for _, verb := range FormatVerbs() {
		ok = AssertRedacted(tb, fmt.Sprintf(verb, v), secrets, "verb %s on %T", verb, v) && ok
	}
	return ok
}

// AssertDiffRedacted compares a and b, each held in unexported struct fields,
// with assert.Equal, and fails tb unless that comparison fails without printing
// a secret. testify prints both values and their diff by reflection, so this
// covers what a failing assertion on such values would log.
func AssertDiffRedacted[T any](tb testing.TB, a, b *T, secrets ...string) bool {
	tb.Helper()
	rec := &messages{}
	assert.Equal(rec, unexportedFields[T]{v: *a, p: a}, unexportedFields[T]{v: *b, p: b})
	if !assert.NotEmpty(tb, rec.list, "the values must differ, so that testify prints them") {
		return false
	}
	ok := true
	for _, m := range rec.list {
		ok = AssertRedacted(tb, m, secrets) && ok
	}
	return ok
}

// messages is an assert.TestingT that records failure messages instead of
// failing.
type messages struct {
	list []string
}

// Errorf records the message.
func (m *messages) Errorf(format string, args ...any) {
	m.list = append(m.list, fmt.Sprintf(format, args...))
}
