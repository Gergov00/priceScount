package bot

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
)

func TestBuildMyListIncludesSubscriptionControlsAndStatus(t *testing.T) {
	min, max := 100.0, 200.0
	subs := []gateway.Subscription{
		{ID: "a1", ProductName: "Кофемолка", ProductURL: "https://example.test/a", MinPrice: &min, MaxPrice: &max},
		{ID: "b2", ProductName: "На паузе", Paused: true},
	}
	text, keyboard := buildMyList(subs)
	for _, expected := range []string{"Твои товары (2)", "Кофемолка", "100 — 200 ₽", "https://example.test/a", "На паузе ⏸"} {
		t.Run(expected, func(t *testing.T) {
			if !strings.Contains(text, expected) {
				t.Errorf("list text %q missing %q", text, expected)
			}
		})
	}
	if len(keyboard.InlineKeyboard) != 6 {
		t.Fatalf("keyboard rows = %d, want 6", len(keyboard.InlineKeyboard))
	}
	wantCallbacks := [][]string{{"edit_sub:a1", "pause_sub:a1"}, {"history_sub:a1", "check_sub:a1"}, {"del_sub:a1"}, {"edit_sub:b2", "resume_sub:b2"}, {"history_sub:b2", "check_sub:b2"}, {"del_sub:b2"}}
	for row, want := range wantCallbacks {
		t.Run(fmt.Sprintf("row_%d", row), func(t *testing.T) {
			if len(keyboard.InlineKeyboard[row]) != len(want) {
				t.Fatalf("row %d buttons = %d, want %d", row, len(keyboard.InlineKeyboard[row]), len(want))
			}
			for col, callback := range want {
				got := keyboard.InlineKeyboard[row][col].CallbackData
				if got == nil || *got != callback {
					t.Errorf("callback [%d][%d] = %v, want %q", row, col, got, callback)
				}
			}
		})
	}
}

func TestMyListUnicodePagesKeepEveryProductAccessible(t *testing.T) {
	subs := make([]gateway.Subscription, 37)
	for i := range subs {
		subs[i] = gateway.Subscription{ID: fmt.Sprintf("sub-%02d", i), ProductName: fmt.Sprintf("товар-%02d-%s", i, strings.Repeat("🙂", 120)), ProductURL: "https://example.test/" + strings.Repeat("путь🙂", 1200)}
	}
	seen := make(map[int]bool)
	for page := 1; ; page++ {
		text, keyboard, actual := buildMyListPage(subs, page)
		if actual != page {
			t.Fatalf("page %d clamped to %d unexpectedly", page, actual)
		}
		if units := utf16Units(text); units > telegramTextLimit {
			t.Fatalf("page %d text uses %d UTF-16 units", page, units)
		}
		for i := range subs {
			marker := fmt.Sprintf("\n%d. товар-%02d-", i+1, i)
			if strings.Contains(text, marker) {
				if seen[i] {
					t.Fatalf("product %d appeared on multiple pages", i)
				}
				seen[i] = true
			}
		}
		visible := make(map[string]bool)
		for i := range subs {
			if strings.Contains(text, fmt.Sprintf("\n%d. товар-%02d-", i+1, i)) {
				visible[subs[i].ID] = true
			}
		}
		for _, row := range keyboard.InlineKeyboard {
			for _, button := range row {
				if button.CallbackData == nil || strings.HasPrefix(*button.CallbackData, "page:") {
					continue
				}
				id := strings.SplitN(*button.CallbackData, ":", 2)[1]
				if !visible[id] {
					t.Fatalf("page %d has control for off-page product %s", page, id)
				}
			}
		}
		if page == 1 && utf16Units(truncateUTF16(subs[0].ProductName, 200)) > 200 {
			t.Fatal("product name exceeds 200 UTF-16 units")
		}
		lastRow := keyboard.InlineKeyboard[len(keyboard.InlineKeyboard)-1]
		var next string
		for _, button := range lastRow {
			if button.CallbackData != nil && strings.HasPrefix(*button.CallbackData, "page:") {
				n, _ := strconv.Atoi(strings.TrimPrefix(*button.CallbackData, "page:"))
				if n > page {
					next = *button.CallbackData
				}
			}
		}
		if next == "" {
			break
		}
	}
	if len(seen) != len(subs) {
		t.Fatalf("rendered %d of %d products", len(seen), len(subs))
	}
}

func TestMyListPageClampsAfterDeletingLastItem(t *testing.T) {
	subs := make([]gateway.Subscription, 11)
	for i := range subs {
		subs[i] = gateway.Subscription{ID: fmt.Sprintf("sub-%d", i), ProductName: fmt.Sprintf("item-%d", i)}
	}
	_, _, page := buildMyListPage(subs, 2)
	if page != 2 {
		t.Fatalf("last page = %d, want 2", page)
	}
	subs = subs[:len(subs)-1]
	_, keyboard, page := buildMyListPage(subs, 2)
	if page != 1 {
		t.Fatalf("page after deleting last item = %d, want 1", page)
	}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData != nil && *button.CallbackData == "page:2" {
				t.Fatal("last page navigation remained after clamp")
			}
		}
	}
}

func TestBuildMyListWithNoSubscriptionsShowsCount(t *testing.T) {
	text, keyboard := buildMyList(nil)
	if text != "📋 Твои товары (0) — страница 1:\n" || len(keyboard.InlineKeyboard) != 0 {
		t.Fatalf("empty list = %q, %#v", text, keyboard)
	}
}
