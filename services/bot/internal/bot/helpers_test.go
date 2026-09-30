package bot

import "testing"

func TestParsePriceAcceptsDecimalCommaAndSpaces(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        float64
	}{
		{name: "decimal comma and grouped spaces", input: "1 234,50", want: 1234.5},
		{name: "decimal point", input: "1234.5", want: 1234.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePrice(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("parsePrice(%q) = %v, %v; want %v", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestParsePriceRejectsInvalidOrNonPositiveValues(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{name: "empty", input: ""},
		{name: "zero", input: "0"},
		{name: "negative", input: "-2"},
		{name: "not numeric", input: "abc"},
		{name: "multiple separators", input: "1,2,3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parsePrice(tc.input); err == nil || got != 0 {
				t.Fatalf("parsePrice(%q) = %v, %v; want error and zero", tc.input, got, err)
			}
		})
	}
}
