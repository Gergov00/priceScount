package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gergov00/pricescount/services/gateway/internal/store"
	"github.com/gin-gonic/gin"
)

type handlerStore struct {
	Store
	pending                 int
	pendingErr, errorLookup error
	lookup                  *store.LookupResult
	userID                  string
	userErr                 error
	createdURL              string
	created                 bool
}

func (s *handlerStore) PendingLookupCount(context.Context) (int, error) {
	return s.pending, s.pendingErr
}
func (s *handlerStore) CreateLookup(_ context.Context, _, u string) error {
	s.createdURL = u
	s.created = true
	return nil
}
func (s *handlerStore) GetLookup(context.Context, string) (*store.LookupResult, error) {
	return s.lookup, s.errorLookup
}
func (s *handlerStore) UserIDByChatID(context.Context, int64) (string, error) {
	return s.userID, s.userErr
}
func (s *handlerStore) UpsertUser(context.Context, int64) (string, error) { return "user-1", nil }
func (s *handlerStore) EnsureProduct(context.Context, string, string, string, string) (string, error) {
	return "product-1", nil
}
func (s *handlerStore) CreateSubscription(context.Context, string, string, float64, float64) (string, error) {
	return "sub-1", nil
}
func setupHandler(st *handlerStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	New(st).Register(r)
	return r
}
func TestPostLookupQueuesThroughTransactionalStore(t *testing.T) {
	st := &handlerStore{}
	r := setupHandler(st)
	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(`{}`)))
	if bad.Code != 400 || st.created {
		t.Fatalf("missing URL status=%d created=%v", bad.Code, st.created)
	}
	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(`{"url":"https://www.wildberries.ru/catalog/123456/detail.aspx"}`)))
	if ok.Code != http.StatusAccepted || !st.created || !strings.Contains(st.createdURL, "123456") {
		t.Fatalf("lookup status=%d created=%v url=%q body=%s", ok.Code, st.created, st.createdURL, ok.Body)
	}
}
func TestPostLookupBackpressureDoesNotCreate(t *testing.T) {
	st := &handlerStore{pending: 25}
	r := setupHandler(st)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/lookup", strings.NewReader(`{"url":"https://www.wildberries.ru/catalog/123456/detail.aspx"}`)))
	if w.Code != http.StatusTooManyRequests || st.created {
		t.Fatalf("status=%d created=%v", w.Code, st.created)
	}
}
func TestPostSubscriptionRejectsInvalidThresholdsBeforeWrites(t *testing.T) {
	st := &handlerStore{}
	r := setupHandler(st)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/subscriptions", strings.NewReader(`{"chat_id":123,"lookup_id":"l","min_price":20,"max_price":10}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}
func TestListSubscriptionsHidesMissingUser(t *testing.T) {
	st := &handlerStore{userErr: store.ErrNotFound}
	r := setupHandler(st)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/subscriptions?chat_id=12", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}
func TestAuthAndBodyLimit(t *testing.T) {
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
		t.Fatalf("unauthorized=%d", unauthorized.Code)
	}
	large := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("123456"))
	req.Header.Set("X-Internal-Token", "secret")
	r.ServeHTTP(large, req)
	if large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large=%d", large.Code)
	}
}
func TestGetLookupDistinguishesMissingAndInternalErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{{"missing", store.ErrNotFound, 404}, {"database", errors.New("db"), 500}} {
		t.Run(tc.name, func(t *testing.T) {
			r := setupHandler(&handlerStore{errorLookup: tc.err})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/lookup/id", nil))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
		})
	}
}
