package utils

import (
	"testing"
)

func TestDifference(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want []string
	}{
		{
			name: "b empty returns all of a",
			a:    []string{"base", "web", "sale"},
			b:    []string{},
			want: []string{"base", "web", "sale"},
		},
		{
			name: "a and b identical returns empty",
			a:    []string{"base", "web"},
			b:    []string{"base", "web"},
			want: nil,
		},
		{
			name: "b is strict subset of a returns delta",
			a:    []string{"base", "web", "sale"},
			b:    []string{"base", "web"},
			want: []string{"sale"},
		},
		{
			name: "a empty returns empty",
			a:    []string{},
			b:    []string{"base"},
			want: nil,
		},
		{
			name: "b has elements not in a returns only unmatched elements of a",
			a:    []string{"base", "web"},
			b:    []string{"base", "crm"},
			want: []string{"web"},
		},
		{
			name: "duplicates in a are preserved as-is",
			a:    []string{"base", "base", "web"},
			b:    []string{"base"},
			want: []string{"web"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Difference(tc.a, tc.b)
			if len(got) != len(tc.want) {
				t.Fatalf("Difference(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Difference(%v, %v)[%d] = %q, want %q", tc.a, tc.b, i, got[i], tc.want[i])
				}
			}
		})
	}
}
