package marketplace

import "testing"

func TestParseWBPrice(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want float64
	}{
		{name: "rubles with ordinary thousands separator", text: "5 690 ₽", want: 5690},
		{name: "rubles with nonbreaking thousands separator", text: "12\u00a0345 ₽", want: 12345},
		{name: "discounted price before old price", text: "5 690 ₽\n7 000 ₽", want: 5690},
		{name: "price without currency symbol", text: "890", want: 890},
		{name: "price with leading label", text: "Цена: 1 299 ₽", want: 1299},
		{name: "no digits", text: "Цена не указана", want: 0},
		{name: "zero price", text: "0 ₽", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseWBPrice(tc.text); got != tc.want {
				t.Errorf("parseWBPrice(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestExtractWBName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		title string
		nmID  string
		want  string
	}{
		{
			name:  "nmID suffix",
			title: "Кроссовки мужские 123456789 купить за 5 690 ₽ в интернет-магазине Wildberries",
			nmID:  "123456789",
			want:  "Кроссовки мужские",
		},
		{
			name:  "buy phrase fallback",
			title: "Сыворотка для лица купить за 899 ₽ в интернет-магазине Wildberries",
			nmID:  "987654321",
			want:  "Сыворотка для лица",
		},
		{
			name:  "trim whitespace",
			title: "  Чайник электрический 987654321 купить за 2 400 ₽",
			nmID:  "987654321",
			want:  "Чайник электрический",
		},
		{name: "no product marker", title: "Wildberries — интернет-магазин", nmID: "123", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractWBName(tc.title, tc.nmID); got != tc.want {
				t.Errorf("extractWBName(%q, %q) = %q, want %q", tc.title, tc.nmID, got, tc.want)
			}
		})
	}
}
