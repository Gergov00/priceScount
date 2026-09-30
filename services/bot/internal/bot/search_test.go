package bot

import "testing"

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		max         int
		want        string
	}{
		{name: "unicode runes plus ellipsis", input: "🙂🙂🙂🙂🙂", max: 3, want: "🙂🙂🙂…"},
		{name: "short text untouched", input: "товар", max: 10, want: "товар"},
		{name: "exact length untouched", input: "abc", max: 3, want: "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncate(tc.input, tc.max); got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
			}
		})
	}
}
