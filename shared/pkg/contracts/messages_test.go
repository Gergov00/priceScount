package contracts

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestScraperTaskJSON(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		value     ScraperTask
		wantForce bool
		wantChat  bool
	}{
		{
			name: "force request carries requester",
			value: ScraperTask{
				TaskID: "task-1", ProductID: "product-1", URL: "https://www.wildberries.ru/catalog/123/detail.aspx",
				Platform: "wb", ScheduledAt: when, Force: true, ChatID: 456,
			},
			wantForce: true, wantChat: true,
		},
		{
			name: "zero force fields omitted",
			value: ScraperTask{
				TaskID: "task-2", ProductID: "product-2", URL: "https://www.wildberries.ru/catalog/124/detail.aspx",
				Platform: "wb", ScheduledAt: when,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			assertJSONField(t, data, "force", tc.wantForce, tc.value.Force)
			assertJSONField(t, data, "chat_id", tc.wantChat, tc.value.ChatID)
		})
	}
}

func TestPriceResultJSON(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		value     PriceResult
		wantForce bool
		wantChat  bool
	}{
		{
			name: "force result carries requester",
			value: PriceResult{
				TaskID: "task-1", ProductID: "product-1", URL: "https://www.wildberries.ru/catalog/123/detail.aspx",
				Price: 5690, Currency: "RUB", ScrapedAt: when, Success: true, Force: true, ChatID: 456,
			},
			wantForce: true, wantChat: true,
		},
		{
			name: "zero force fields omitted",
			value: PriceResult{
				TaskID: "task-2", LookupID: "lookup-2", URL: "https://www.wildberries.ru/catalog/124/detail.aspx",
				Price: 1299, Currency: "RUB", ScrapedAt: when, Success: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			assertJSONField(t, data, "force", tc.wantForce, tc.value.Force)
			assertJSONField(t, data, "chat_id", tc.wantChat, tc.value.ChatID)
		})
	}
}

func TestTrackRequestJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		value    TrackRequest
		wantChat bool
	}{
		{name: "force request carries chat ID", value: TrackRequest{Action: "force", ProductID: "product-1", ChatID: 456}, wantChat: true},
		{name: "zero chat ID omitted", value: TrackRequest{Action: "pause", ProductID: "product-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			assertJSONField(t, data, "chat_id", tc.wantChat, tc.value.ChatID)
		})
	}
}

func assertJSONField(t *testing.T, data []byte, key string, wantPresent bool, wantValue any) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", data, err)
	}
	raw, present := fields[key]
	if present != wantPresent {
		t.Fatalf("JSON field %q present = %v, want %v; JSON: %s", key, present, wantPresent, data)
	}
	if !present {
		return
	}
	var got any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", raw, err)
	}
	wantJSON, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("json.Marshal(%v) error = %v", wantValue, err)
	}
	var want any
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", wantJSON, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JSON field %q = %v, want %v", key, got, wantValue)
	}
}
