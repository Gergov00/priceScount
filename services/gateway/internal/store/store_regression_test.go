//go:build integration && regression

package store

import "testing"

// F7: restoring a subscription with new thresholds must clear an old alert.
func TestCreateSubscriptionClearsStaleAlertRegression(t *testing.T) {
	st, _ := integrationStore(t)
	ctx := t.Context()
	user, product := seedProduct(t, st)
	sub, err := st.CreateSubscription(ctx, user, product, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAlertState(ctx, sub, "up"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSubscription(ctx, sub, user); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(ctx, user, product, 80, 150); err != nil {
		t.Fatal(err)
	}
	watchers, err := st.ProductSubscriptions(ctx, product)
	if err != nil {
		t.Fatal(err)
	}
	if len(watchers) != 1 || watchers[0].AlertState != "" {
		t.Fatalf("F7: restored subscription retains stale alert: %+v", watchers)
	}
}
