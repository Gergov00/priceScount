package handler

import (
	"context"
	"errors"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type handlerStore struct {
	Store
	pending                 int
	pendingErr, errorLookup error
	lookup                  *store.LookupResult
	userID                  string
	userErr                 error
	productID, subID        string
	createdURL              string
	subErr                  error
}

func (s *handlerStore) PendingLookupCount(context.Context) (int, error) {
	return s.pending, s.pendingErr
}
func (s *handlerStore) CreateLookup(_ context.Context, _, u string) error {
	s.createdURL = u
	return nil
}
func (s *handlerStore) GetLookup(_ context.Context, _ string) (*store.LookupResult, error) {
	return s.lookup, s.errorLookup
}
func (s *handlerStore) UserIDByChatID(context.Context, int64) (string, error) {
	return s.userID, s.userErr
}
func (s *handlerStore) UpsertUser(context.Context, int64) (string, error) { return "user-1", nil }
func (s *handlerStore) EnsureProduct(_ context.Context, _ string, _ string, _ string, _ string) (string, error) {
	return "product-1", nil
}
func (s *handlerStore) CreateSubscription(_ context.Context, _ string, _ string, _ float64, _ float64) (string, error) {
	return "sub-1", nil
}
func (s *handlerStore) GetSubscription(_ context.Context, _, _ string) (*store.Subscription, error) {
	return nil, s.subErr
}

type publisherSpy struct {
	calls int
	queue string
	value any
	err   error
}

func (p *publisherSpy) Publish(_ context.Context, q string, v any) error {
	p.calls++
	p.queue = q
	p.value = v
	return p.err
}
func setupHandler(st *handlerStore, p *publisherSpy) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	New(st, p).Register(r)
	return r
}
func TestPostLookupValidatesAndQueuesNormalizedURL(t *testing.T) {
	st := &handlerStore{}
	p := &publisherSpy{}
	r := setupHandler(st, p)
	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(`{}`)))
	if bad.Code != 400 || p.calls != 0 {
		t.Fatalf("missing URL status=%d publishes=%d", bad.Code, p.calls)
	}
	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(`{"url":"https://www.wildberries.ru/catalog/123456/detail.aspx"}`)))
	if ok.Code != http.StatusAccepted || p.calls != 1 {
		t.Fatalf("lookup status=%d publishes=%d body=%s", ok.Code, p.calls, ok.Body)
	}
	task, yes := p.value.(contracts.LookupTask)
	if !yes || task.LookupID == "" || !strings.Contains(task.URL, "123456") || st.createdURL != task.URL {
		t.Fatalf("queued task=%#v stored URL=%q", p.value, st.createdURL)
	}
}
func TestPostLookupBackpressureDoesNotCreateOrPublish(t *testing.T) {
	st := &handlerStore{pending: 25}
	p := &publisherSpy{}
	r := setupHandler(st, p)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(`{"url":"https://www.wildberries.ru/catalog/123456/detail.aspx"}`)))
	if w.Code != http.StatusTooManyRequests || p.calls != 0 || st.createdURL != "" {
		t.Fatalf("status=%d publishes=%d url=%q", w.Code, p.calls, st.createdURL)
	}
}
func TestPostSubscriptionRejectsInvalidThresholdsBeforeWrites(t *testing.T) {
	st := &handlerStore{}
	p := &publisherSpy{}
	r := setupHandler(st, p)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/subscriptions", strings.NewReader(`{"chat_id":123,"lookup_id":"l","min_price":20,"max_price":10}`)))
	if w.Code != http.StatusBadRequest || p.calls != 0 {
		t.Fatalf("status=%d publishes=%d", w.Code, p.calls)
	}
}
func TestListSubscriptionsHidesMissingUserAsEmptyList(t *testing.T) {
	st := &handlerStore{userErr: store.ErrNotFound}
	p := &publisherSpy{}
	r := setupHandler(st, p)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/subscriptions?chat_id=12", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}
func TestAuthAndBodyLimitMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Auth("secret"), BodyLimit(4))
	r.POST("/", func(c *gin.Context) {
		b, err := io.ReadAll(c.Request.Body)
		if err != nil || len(b) > 4 {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusNoContent)
	})
	unauthorized := httptest.NewRecorder()
	r.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("ok")))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	tooLarge := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("123456"))
	req.Header.Set("X-Internal-Token", "secret")
	r.ServeHTTP(tooLarge, req)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status=%d", tooLarge.Code)
	}
}

func TestPatchSubscriptionHidesOwnershipFailure(t *testing.T) {
	st := &handlerStore{userID: "user-1", subErr: store.ErrNotFound}
	r := setupHandler(st, &publisherSpy{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/subscriptions/sub-1", strings.NewReader(`{"chat_id":123,"action":"pause"}`))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "subscription not found") {
		t.Fatalf("ownership failure status=%d body=%s", w.Code, w.Body)
	}
}
func TestPostSubscriptionHappyPathPublishesTrackRequest(t *testing.T) {
	st := &handlerStore{lookup: &store.LookupResult{URL: "https://www.wildberries.ru/catalog/123456/detail.aspx", Status: store.LookupDone, Name: "Lamp"}}
	p := &publisherSpy{}
	r := setupHandler(st, p)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/subscriptions", strings.NewReader(`{"chat_id":123,"lookup_id":"l","min_price":10,"max_price":20}`)))
	if w.Code != http.StatusOK || p.calls != 1 {
		t.Fatalf("status=%d publishes=%d body=%s", w.Code, p.calls, w.Body)
	}
	task, ok := p.value.(contracts.TrackRequest)
	if !ok || task.Action != "add" || task.ProductID != "product-1" || task.Platform != "wb" {
		t.Fatalf("track task=%#v", p.value)
	}
}
func TestGetLookupDistinguishesMissingAndInternalErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{{"missing", store.ErrNotFound, http.StatusNotFound}, {"database error", errors.New("db"), http.StatusInternalServerError}} {
		t.Run(tc.name, func(t *testing.T) {
			st := &handlerStore{errorLookup: tc.err}
			p := &publisherSpy{}
			r := setupHandler(st, p)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/lookup/id", nil))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
		})
	}
}
