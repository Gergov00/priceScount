package bot

import (
	"fmt"
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

func TestBuildMyListWithNoSubscriptionsShowsCount(t *testing.T) {
	text, keyboard := buildMyList(nil)
	if text != "📋 Твои товары (0):\n" || len(keyboard.InlineKeyboard) != 0 {
		t.Fatalf("empty list = %q, %#v", text, keyboard)
	}
}
