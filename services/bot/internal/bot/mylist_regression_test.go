//go:build regression

package bot

import (
	"strings"
	"testing"

	"github.com/Gergov00/pricescount/services/bot/internal/gateway"
)

// Regression for PROJECT_REVIEW.md F10: generated /mylist text must fit the
// Telegram sendMessage limit to preserve access to the list and its controls.
func TestBuildMyListStaysWithinTelegramMessageLimit(t *testing.T) {
	subs := make([]gateway.Subscription, 100)
	for i := range subs {
		subs[i] = gateway.Subscription{ID: "sub", ProductName: strings.Repeat("товар", 10), ProductURL: "https://example.test/product/123456789"}
	}
	text, _ := buildMyList(subs)
	if len([]rune(text)) > 4096 {
		t.Fatalf("mylist has %d characters, exceeds Telegram limit 4096", len([]rune(text)))
	}
}
