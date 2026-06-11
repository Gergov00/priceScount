package handler

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/platform"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
)

// Store is the persistence interface required by Handler.
// Defined here, in the consumer, per Go convention.
type Store interface {
	CreateLookup(ctx context.Context, id, url string) error
	GetLookup(ctx context.Context, id string) (*store.LookupResult, error)
	PendingLookupCount(ctx context.Context) (int, error)
	UpsertUser(ctx context.Context, chatID int64) (string, error)
	UserIDByChatID(ctx context.Context, chatID int64) (string, error)
	EnsureProduct(ctx context.Context, candidateID, name, url, platform string) (string, error)
	CreateSubscription(ctx context.Context, userID, productID string, minPrice, maxPrice float64) (string, error)
	UserSubscriptions(ctx context.Context, userID string) ([]store.Subscription, error)
	GetSubscription(ctx context.Context, subID, userID string) (*store.Subscription, error)
	ProductSubCounts(ctx context.Context, productID string) (store.SubCounts, error)
	PauseSubscription(ctx context.Context, subID, userID string) error
	ResumeSubscription(ctx context.Context, subID, userID string) error
	UpdateThresholds(ctx context.Context, subID, userID string, minPrice, maxPrice float64) error
	DeleteSubscription(ctx context.Context, subID, userID string) error
	PriceHistory(ctx context.Context, productID string, limit int) ([]store.PricePoint, error)
}

// Publisher is the messaging interface required by Handler.
type Publisher interface {
	Publish(ctx context.Context, queue string, v any) error
}

type Handler struct {
	store Store
	mq    Publisher
}

func New(st Store, mq Publisher) *Handler {
	return &Handler{store: st, mq: mq}
}

func (h *Handler) Register(r *gin.Engine) {
	r.POST("/lookup", h.postLookup)
	r.GET("/lookup/:id", h.getLookup)

	subs := r.Group("/subscriptions")
	subs.POST("", h.postSubscription)
	subs.GET("", h.listSubscriptions)
	subs.PATCH("/:id", h.patchSubscription)
	subs.DELETE("/:id", h.deleteSubscription)
	subs.GET("/:id/history", h.getHistory)
	subs.POST("/:id/check", h.postCheck)
}

// ─── POST /lookup ─────────────────────────────────────────────────────────────

type postLookupRequest struct {
	URL string `json:"url" binding:"required"`
}

// maxPendingLookups bounds the lookup queue: each lookup costs a headless-Chrome
// page load (~10-40s), so an unbounded queue is a trivial DoS vector.
const maxPendingLookups = 25

func (h *Handler) postLookup(c *gin.Context) {
	var req postLookupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}

	pending, err := h.store.PendingLookupCount(c.Request.Context())
	if err != nil {
		slog.Error("pending lookup count", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if pending >= maxPendingLookups {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many lookups in progress, try again later"})
		return
	}

	plat, err := platform.Detect(req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	normalURL, err := normalizeURL(plat, req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot parse product url"})
		return
	}

	lookupID := uuid.New().String()
	if err := h.store.CreateLookup(c.Request.Context(), lookupID, normalURL); err != nil {
		slog.Error("create lookup", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	task := contracts.LookupTask{
		TaskID:   uuid.New().String(),
		LookupID: lookupID,
		URL:      normalURL,
		Platform: plat,
	}
	if err := h.mq.Publish(c.Request.Context(), broker.QueueLookupTasks, task); err != nil {
		slog.Error("publish lookup task", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"lookup_id": lookupID})
}

// ─── GET /lookup/:id ─────────────────────────────────────────────────────────

func (h *Handler) getLookup(c *gin.Context) {
	result, err := h.store.GetLookup(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "lookup not found"})
		return
	}
	if err != nil {
		slog.Error("get lookup", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	switch result.Status {
	case store.LookupPending:
		c.JSON(http.StatusAccepted, gin.H{"status": "pending"})
	case store.LookupDone:
		c.JSON(http.StatusOK, gin.H{
			"status": "done",
			"name":   result.Name,
			"price":  result.Price,
			"url":    result.URL,
		})
	case store.LookupFailed:
		c.JSON(http.StatusOK, gin.H{"status": "failed", "error": result.Error})
	}
}

// ─── POST /subscriptions ─────────────────────────────────────────────────────

type postSubscriptionRequest struct {
	ChatID   int64   `json:"chat_id"  binding:"required"`
	LookupID string  `json:"lookup_id" binding:"required"`
	MinPrice float64 `json:"min_price"`
	MaxPrice float64 `json:"max_price"`
}

func (h *Handler) postSubscription(c *gin.Context) {
	var req postSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id and lookup_id are required"})
		return
	}
	if req.MinPrice < 0 || req.MaxPrice <= req.MinPrice {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prices must be non-negative and max_price greater than min_price"})
		return
	}

	ctx := c.Request.Context()

	lookup, err := h.store.GetLookup(ctx, req.LookupID)
	if err != nil || lookup.Status != store.LookupDone {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lookup not found or not completed"})
		return
	}

	userID, err := h.store.UpsertUser(ctx, req.ChatID)
	if err != nil {
		slog.Error("upsert user", "chat_id", req.ChatID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	plat, _ := platform.Detect(lookup.URL)

	productID, err := h.store.EnsureProduct(ctx, uuid.New().String(), lookup.Name, lookup.URL, plat)
	if err != nil {
		slog.Error("ensure product", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	subID, err := h.store.CreateSubscription(ctx, userID, productID, req.MinPrice, req.MaxPrice)
	if err != nil {
		slog.Error("create subscription", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// The subscription is saved, but without this message the scheduler never
	// monitors it. Surface the failure so the client retries (the create is
	// an idempotent upsert).
	if err := h.mq.Publish(ctx, broker.QueueTrackRequests, contracts.TrackRequest{
		Action:        "add",
		ProductID:     productID,
		URL:           lookup.URL,
		Platform:      plat,
		IntervalHours: 1,
	}); err != nil {
		slog.Error("publish track request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"subscription_id": subID, "product_id": productID})
}

// ─── GET /subscriptions ───────────────────────────────────────────────────────

func (h *Handler) listSubscriptions(c *gin.Context) {
	chatID, err := strconv.ParseInt(c.Query("chat_id"), 10, 64)
	if err != nil || chatID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid chat_id"})
		return
	}

	ctx := c.Request.Context()

	userID, err := h.store.UserIDByChatID(ctx, chatID)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusOK, []store.Subscription{})
		return
	}
	if err != nil {
		slog.Error("user by chat_id", "chat_id", chatID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	subs, err := h.store.UserSubscriptions(ctx, userID)
	if err != nil {
		slog.Error("list subscriptions", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if subs == nil {
		subs = []store.Subscription{}
	}
	c.JSON(http.StatusOK, subs)
}

// ─── PATCH /subscriptions/:id ─────────────────────────────────────────────────

type patchSubscriptionRequest struct {
	ChatID   int64    `json:"chat_id" binding:"required"`
	Action   string   `json:"action"  binding:"required"`
	MinPrice *float64 `json:"min_price"`
	MaxPrice *float64 `json:"max_price"`
}

func (h *Handler) patchSubscription(c *gin.Context) {
	subID := c.Param("id")

	var req patchSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id and action are required"})
		return
	}

	ctx := c.Request.Context()

	userID, ok := h.userIDOr404(c, req.ChatID)
	if !ok {
		return
	}

	sub, err := h.store.GetSubscription(ctx, subID, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}

	var trackAction string
	switch req.Action {
	case "pause":
		if err := h.store.PauseSubscription(ctx, subID, userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		// The scheduler entry is shared by everyone tracking this URL —
		// stop monitoring only when no one is watching anymore.
		watching, err := h.productStillWatched(ctx, sub.ProductID)
		if err != nil {
			slog.Error("product sub counts", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		if !watching {
			trackAction = "pause"
		}
	case "resume":
		if err := h.store.ResumeSubscription(ctx, subID, userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		trackAction = "resume"
	case "edit":
		if req.MinPrice == nil || req.MaxPrice == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "min_price and max_price are required for edit"})
			return
		}
		if *req.MinPrice < 0 || *req.MaxPrice <= *req.MinPrice {
			c.JSON(http.StatusBadRequest, gin.H{"error": "prices must be non-negative and max_price greater than min_price"})
			return
		}
		if err := h.store.UpdateThresholds(ctx, subID, userID, *req.MinPrice, *req.MaxPrice); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be pause, resume, or edit"})
		return
	}

	if trackAction != "" {
		plat, _ := platform.Detect(sub.ProductURL)
		if err := h.mq.Publish(ctx, broker.QueueTrackRequests, contracts.TrackRequest{
			Action:    trackAction,
			ProductID: sub.ProductID,
			URL:       sub.ProductURL,
			Platform:  plat,
		}); err != nil {
			slog.Error("publish track request", "action", trackAction, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
	}

	c.Status(http.StatusNoContent)
}

// ─── DELETE /subscriptions/:id ────────────────────────────────────────────────

type chatIDBody struct {
	ChatID int64 `json:"chat_id" binding:"required"`
}

func (h *Handler) deleteSubscription(c *gin.Context) {
	subID := c.Param("id")

	var req chatIDBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id is required"})
		return
	}

	ctx := c.Request.Context()

	userID, ok := h.userIDOr404(c, req.ChatID)
	if !ok {
		return
	}

	sub, err := h.store.GetSubscription(ctx, subID, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}

	if err := h.store.DeleteSubscription(ctx, subID, userID); err != nil {
		slog.Error("delete subscription", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Other users may still track the same URL — only drop the scheduler
	// entry when no active subscriptions remain at all, and merely pause it
	// when the rest are paused.
	counts, err := h.store.ProductSubCounts(ctx, sub.ProductID)
	if err != nil {
		slog.Error("product sub counts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	trackAction := ""
	switch {
	case counts.Active == 0:
		trackAction = "delete"
	case counts.Watching == 0:
		trackAction = "pause"
	}

	if trackAction != "" {
		plat, _ := platform.Detect(sub.ProductURL)
		if err := h.mq.Publish(ctx, broker.QueueTrackRequests, contracts.TrackRequest{
			Action:    trackAction,
			ProductID: sub.ProductID,
			URL:       sub.ProductURL,
			Platform:  plat,
		}); err != nil {
			slog.Error("publish track request", "action", trackAction, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
	}

	c.Status(http.StatusNoContent)
}

// ─── GET /subscriptions/:id/history ──────────────────────────────────────────

func (h *Handler) getHistory(c *gin.Context) {
	subID := c.Param("id")

	chatID, err := strconv.ParseInt(c.Query("chat_id"), 10, 64)
	if err != nil || chatID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid chat_id"})
		return
	}

	ctx := c.Request.Context()

	userID, ok := h.userIDOr404(c, chatID)
	if !ok {
		return
	}

	sub, err := h.store.GetSubscription(ctx, subID, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}

	points, err := h.store.PriceHistory(ctx, sub.ProductID, 50)
	if err != nil {
		slog.Error("price history", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if points == nil {
		points = []store.PricePoint{}
	}
	c.JSON(http.StatusOK, points)
}

// ─── POST /subscriptions/:id/check ───────────────────────────────────────────

func (h *Handler) postCheck(c *gin.Context) {
	subID := c.Param("id")

	var req chatIDBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id is required"})
		return
	}

	ctx := c.Request.Context()

	userID, ok := h.userIDOr404(c, req.ChatID)
	if !ok {
		return
	}

	sub, err := h.store.GetSubscription(ctx, subID, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}

	plat, _ := platform.Detect(sub.ProductURL)
	if err := h.mq.Publish(ctx, broker.QueueTrackRequests, contracts.TrackRequest{
		Action:    "force",
		ProductID: sub.ProductID,
		URL:       sub.ProductURL,
		Platform:  plat,
		ChatID:    req.ChatID, // only the requester gets the status message
	}); err != nil {
		slog.Error("publish force check", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"status": "scheduled"})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// userIDOr404 resolves chat_id to an existing user. On failure it writes the
// response and returns ok=false. A missing user reads as "subscription not
// found" so chat_id probing cannot distinguish users from subscriptions.
func (h *Handler) userIDOr404(c *gin.Context, chatID int64) (string, bool) {
	userID, err := h.store.UserIDByChatID(c.Request.Context(), chatID)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return "", false
	}
	if err != nil {
		slog.Error("user by chat_id", "chat_id", chatID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return "", false
	}
	return userID, true
}

// productStillWatched reports whether the product has at least one active,
// non-paused subscription left.
func (h *Handler) productStillWatched(ctx context.Context, productID string) (bool, error) {
	counts, err := h.store.ProductSubCounts(ctx, productID)
	if err != nil {
		return false, err
	}
	return counts.Watching > 0, nil
}

func normalizeURL(plat, rawURL string) (string, error) {
	if plat == "wb" {
		return platform.NormalizeWB(rawURL)
	}
	return "", fmt.Errorf("unsupported platform: %s", plat)
}

// ─── middleware ──────────────────────────────────────────────────────────────

// Auth rejects requests that do not carry the shared internal token in the
// X-Internal-Token header. The Gateway is an internal API for the Bot only.
func Auth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := c.GetHeader("X-Internal-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// BodyLimit caps request body size.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// RequestLogger logs every request via slog.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(),
		)
	}
}
