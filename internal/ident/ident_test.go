package ident_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mrz1836/go-discern/internal/ident"
)

func TestVisible(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want bool
	}{
		{name: "empty", s: "", max: 0, want: true},
		{name: "lowest byte", s: "!", max: 1, want: true},
		{name: "highest byte", s: "~", max: 1, want: true},
		{name: "model", s: "jev-1.13.0", max: ident.MaxProviderID, want: true},
		{name: "path", s: "@cf/cloudflare/clef", max: ident.MaxProviderID, want: true},
		{name: "longest", s: strings.Repeat("r", ident.MaxProviderID), max: ident.MaxProviderID, want: true},
		{name: "too long", s: strings.Repeat("r", ident.MaxProviderID+1), max: ident.MaxProviderID},
		{name: "space", s: "a b", max: 8},
		{name: "line feed", s: "req-1\n", max: 8},
		{name: "nul", s: "a\x00", max: 8},
		{name: "delete", s: "a\x7f", max: 8},
		{name: "non-ASCII", s: "café", max: 8},
		{name: "zero-width space", s: "a\u200b", max: 8},
		{name: "invalid UTF-8", s: "a\xff", max: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ident.Visible(tt.s, tt.max))
		})
	}
}

func TestName(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want bool
	}{
		{name: "lower", s: "outage", max: 100, want: true},
		{name: "every class", s: "aZ09_.-", max: 100, want: true},
		{name: "single byte", s: "q", max: 1, want: true},
		{name: "longest", s: strings.Repeat("q", 100), max: 100, want: true},
		{name: "empty", s: "", max: 100},
		{name: "too long", s: strings.Repeat("q", 101), max: 100},
		{name: "space", s: "a b", max: 100},
		{name: "slash", s: "a/b", max: 100},
		{name: "colon", s: "a:b", max: 100},
		{name: "byte before digits", s: "a/", max: 100},
		{name: "byte after digits", s: "a:", max: 100},
		{name: "byte before upper", s: "a@", max: 100},
		{name: "byte after upper", s: "a[", max: 100},
		{name: "byte before lower", s: "a`", max: 100},
		{name: "byte after lower", s: "a{", max: 100},
		{name: "non-ASCII", s: "café", max: 100},
		{name: "control", s: "a\n", max: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ident.Name(tt.s, tt.max))
		})
	}
}
