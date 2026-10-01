package platform

import "testing"

func TestDetect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{name: "product URL", url: "https://www.wildberries.ru/catalog/123456/detail.aspx", want: "wb"},
		{name: "case-insensitive host", url: "HTTPS://WILDBERRIES.RU/catalog/123456/detail.aspx", want: "wb"},
		{name: "other marketplace", url: "https://www.ozon.ru/product/item-123", wantErr: true},
		{name: "not a URL", url: "not a marketplace URL", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Detect(tc.url)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Detect(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("Detect(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestNormalizeWB(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "product URL with query",
			url:  "https://www.wildberries.ru/catalog/123456/detail.aspx?size=987&source=search",
			want: "https://www.wildberries.ru/catalog/123456/detail.aspx",
		},
		{
			name: "product URL without www",
			url:  "https://wildberries.ru/catalog/123456/",
			want: "https://www.wildberries.ru/catalog/123456/detail.aspx",
		},
		{name: "category URL has no product ID", url: "https://www.wildberries.ru/catalog/", wantErr: true},
		{name: "other marketplace URL", url: "https://www.ozon.ru/product/123456", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeWB(tc.url)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeWB(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("NormalizeWB(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
