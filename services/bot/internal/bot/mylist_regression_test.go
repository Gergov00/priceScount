package bot

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
)

// Regression for PROJECT_REVIEW.md F10: every page fits Telegram's text limit.
func TestBuildMyListPagesStayWithinTelegramMessageLimit(t *testing.T) {
	subs := make([]gateway.Subscription, 100)
	for i := range subs {
		subs[i] = gateway.Subscription{ID: "sub", ProductName: strings.Repeat("товар🙂", 40), ProductURL: "https://example.test/product/123456789"}
	}
	for page := 1; ; page++ {
		text, _, actual := buildMyListPage(subs, page)
		if actual != page {
			t.Fatalf("page %d was clamped to %d", page, actual)
		}
		if units := utf16Units(text); units > 4096 {
			t.Fatalf("mylist page has %d UTF-16 units, exceeds Telegram limit", units)
		}
		_, keyboard, _ := buildMyListPage(subs, page)
		if len(keyboard.InlineKeyboard) == 0 {
			break
		}
		last := keyboard.InlineKeyboard[len(keyboard.InlineKeyboard)-1]
		hasNext := false
		for _, button := range last {
			if button.CallbackData != nil && *button.CallbackData == fmt.Sprintf("page:%d", page+1) {
				hasNext = true
			}
		}
		if !hasNext {
			break
		}
	}
}
