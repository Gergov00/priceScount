package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/Gergov00/pricescount/shared/pkg/platform"
	"github.com/Gergov00/pricescount/services/gateway/internal/store"
)

type Handler struct {
	store *store.Store
	mq    *broker.Connection
}

func New(st *store.Store, mq *broker.Connection) *Handler {
	return &Handler{store: st, mq: mq}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /lookup", h.postLookup)
	mux.HandleFunc("GET /lookup/{id}", h.getLookup)
	mux.HandleFunc("POST /subscriptions", h.postSubscription)
	mux.HandleFunc("GET /subscriptions", h.listSubscriptions)
	mux.HandleFunc("PATCH /subscriptions/{id}", h.patchSubscription)
	mux.HandleFunc("DELETE /subscriptions/{id}", h.deleteSubscription)
	mux.HandleFunc("GET /subscriptions/{id}/history", h.getHistory)
	mux.HandleFunc("POST /subscriptions/{id}/check", h.postCheck)
}

// ─── POST /lookup ─────────────────────────────────────────────────────────────

type postLookupRequest struct {
	URL string `json:"url"`
}

func (h *Handler) postLookup(w http.ResponseWriter, r *http.Request) {
	var req postLookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	plat, err := platform.Detect(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	normalURL, err := normalizeURL(plat, req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot parse product url")
		return
	}

	lookupID := uuid.New().String()
	if err := h.store.CreateLookup(r.Context(), lookupID, normalURL); err != nil {
		slog.Error("create lookup", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	task := contracts.LookupTask{
		TaskID:   uuid.New().String(),
		LookupID: lookupID,
		URL:      normalURL,
		Platform: plat,
	}
	if err := h.mq.Publish(r.Context(), broker.QueueLookupTasks, task); err != nil {
		slog.Error("publish lookup task", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"lookup_id": lookupID})
}

// ─── GET /lookup/{id} ─────────────────────────────────────────────────────────

func (h *Handler) getLookup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := h.store.GetLookup(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "lookup not found")
		return
	}

	switch result.Status {
	case store.LookupPending:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
	case store.LookupDone:
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "done",
			"name":   result.Name,
			"price":  result.Price,
			"url":    result.URL,
		})
	case store.LookupFailed:
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "failed",
			"error":  result.Error,
		})
	}
}

// ─── POST /subscriptions ──────────────────────────────────────────────────────

type postSubscriptionRequest struct {
	ChatID   int64   `json:"chat_id"`
	LookupID string  `json:"lookup_id"`
	MinPrice float64 `json:"min_price"`
	MaxPrice float64 `json:"max_price"`
}

func (h *Handler) postSubscription(w http.ResponseWriter, r *http.Request) {
	var req postSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ChatID == 0 || req.LookupID == "" {
		writeError(w, http.StatusBadRequest, "chat_id and lookup_id are required")
		return
	}
	if req.MaxPrice <= req.MinPrice {
		writeError(w, http.StatusBadRequest, "max_price must be greater than min_price")
		return
	}

	lookup, err := h.store.GetLookup(r.Context(), req.LookupID)
	if err != nil || lookup.Status != store.LookupDone {
		writeError(w, http.StatusBadRequest, "lookup not found or not completed")
		return
	}

	userID, err := h.store.UpsertUser(r.Context(), req.ChatID)
	if err != nil {
		slog.Error("upsert user", "chat_id", req.ChatID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	plat, _ := platform.Detect(lookup.URL)

	// Reuse existing product if same URL already tracked, otherwise create new.
	productID, err := h.store.ProductIDByURL(r.Context(), lookup.URL)
	if err != nil {
		productID = uuid.New().String()
	}

	if err := h.store.UpsertProduct(r.Context(), productID, lookup.Name, lookup.URL, plat); err != nil {
		slog.Error("upsert product", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	subID, err := h.store.CreateSubscription(r.Context(), userID, productID, req.MinPrice, req.MaxPrice)
	if err != nil {
		slog.Error("create subscription", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	track := contracts.TrackRequest{
		Action:        "add",
		ProductID:     productID,
		URL:           lookup.URL,
		Platform:      plat,
		IntervalHours: 1,
	}
	if err := h.mq.Publish(r.Context(), broker.QueueTrackRequests, track); err != nil {
		slog.Error("publish track request", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"subscription_id": subID,
		"product_id":      productID,
	})
}

// ─── GET /subscriptions ───────────────────────────────────────────────────────

func (h *Handler) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	chatID, err := parseChatID(r.URL.Query().Get("chat_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid chat_id")
		return
	}

	userID, err := h.store.UpsertUser(r.Context(), chatID)
	if err != nil {
		slog.Error("upsert user", "chat_id", chatID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	subs, err := h.store.UserSubscriptions(r.Context(), userID)
	if err != nil {
		slog.Error("list subscriptions", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if subs == nil {
		subs = []store.Subscription{}
	}
	writeJSON(w, http.StatusOK, subs)
}

// ─── PATCH /subscriptions/{id} ────────────────────────────────────────────────

type patchSubscriptionRequest struct {
	ChatID   int64    `json:"chat_id"`
	Action   string   `json:"action"` // pause | resume | edit
	MinPrice *float64 `json:"min_price,omitempty"`
	MaxPrice *float64 `json:"max_price,omitempty"`
}

func (h *Handler) patchSubscription(w http.ResponseWriter, r *http.Request) {
	subID := r.PathValue("id")

	var req patchSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ChatID == 0 {
		writeError(w, http.StatusBadRequest, "chat_id is required")
		return
	}

	userID, err := h.store.UpsertUser(r.Context(), req.ChatID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	sub, err := h.store.GetSubscription(r.Context(), subID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}

	var trackAction string
	switch req.Action {
	case "pause":
		if err := h.store.PauseSubscription(r.Context(), subID, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		trackAction = "pause"
	case "resume":
		if err := h.store.ResumeSubscription(r.Context(), subID, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		trackAction = "resume"
	case "edit":
		if req.MinPrice == nil || req.MaxPrice == nil {
			writeError(w, http.StatusBadRequest, "min_price and max_price are required for edit")
			return
		}
		if *req.MaxPrice <= *req.MinPrice {
			writeError(w, http.StatusBadRequest, "max_price must be greater than min_price")
			return
		}
		if err := h.store.UpdateThresholds(r.Context(), subID, userID, *req.MinPrice, *req.MaxPrice); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "action must be pause, resume, or edit")
		return
	}

	if trackAction != "" {
		plat, _ := platform.Detect(sub.ProductURL)
		track := contracts.TrackRequest{
			Action:    trackAction,
			ProductID: sub.ProductID,
			URL:       sub.ProductURL,
			Platform:  plat,
		}
		if err := h.mq.Publish(r.Context(), broker.QueueTrackRequests, track); err != nil {
			slog.Error("publish track request", "action", trackAction, "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── DELETE /subscriptions/{id} ───────────────────────────────────────────────

type chatIDRequest struct {
	ChatID int64 `json:"chat_id"`
}

func (h *Handler) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	subID := r.PathValue("id")

	var req chatIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	userID, err := h.store.UpsertUser(r.Context(), req.ChatID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	sub, err := h.store.GetSubscription(r.Context(), subID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}

	if err := h.store.DeleteSubscription(r.Context(), subID, userID); err != nil {
		slog.Error("delete subscription", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	plat, _ := platform.Detect(sub.ProductURL)
	track := contracts.TrackRequest{
		Action:    "delete",
		ProductID: sub.ProductID,
		URL:       sub.ProductURL,
		Platform:  plat,
	}
	if err := h.mq.Publish(r.Context(), broker.QueueTrackRequests, track); err != nil {
		slog.Error("publish track delete", "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── GET /subscriptions/{id}/history ─────────────────────────────────────────

func (h *Handler) getHistory(w http.ResponseWriter, r *http.Request) {
	subID := r.PathValue("id")

	chatID, err := parseChatID(r.URL.Query().Get("chat_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid chat_id")
		return
	}

	userID, err := h.store.UpsertUser(r.Context(), chatID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	sub, err := h.store.GetSubscription(r.Context(), subID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}

	points, err := h.store.PriceHistory(r.Context(), sub.ProductID, 50)
	if err != nil {
		slog.Error("price history", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if points == nil {
		points = []store.PricePoint{}
	}
	writeJSON(w, http.StatusOK, points)
}

// ─── POST /subscriptions/{id}/check ──────────────────────────────────────────

func (h *Handler) postCheck(w http.ResponseWriter, r *http.Request) {
	subID := r.PathValue("id")

	var req chatIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	userID, err := h.store.UpsertUser(r.Context(), req.ChatID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	sub, err := h.store.GetSubscription(r.Context(), subID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}

	plat, _ := platform.Detect(sub.ProductURL)
	track := contracts.TrackRequest{
		Action:    "force",
		ProductID: sub.ProductID,
		URL:       sub.ProductURL,
		Platform:  plat,
	}
	if err := h.mq.Publish(r.Context(), broker.QueueTrackRequests, track); err != nil {
		slog.Error("publish force check", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scheduled"})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func normalizeURL(plat, rawURL string) (string, error) {
	if plat == "wb" {
		return platform.NormalizeWB(rawURL)
	}
	return "", fmt.Errorf("unsupported platform: %s", plat)
}

func parseChatID(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty chat_id")
	}
	return strconv.ParseInt(s, 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
