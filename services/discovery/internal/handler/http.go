package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/Gergov00/pricescount/services/discovery/internal/agent"
	"github.com/Gergov00/pricescount/services/discovery/internal/publisher"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

// Handler wires the HTTP layer to the agent and publisher.
type Handler struct {
	agent     *agent.Agent
	publisher *publisher.Publisher
}

func New(ag *agent.Agent, pub *publisher.Publisher) *Handler {
	return &Handler{agent: ag, publisher: pub}
}

type discoverRequest struct {
	ProductName string `json:"product_name"`
	Locale      string `json:"locale"` // "ru", "us", "all" — default "all"
}

type discoveredItem struct {
	URL    string `json:"url"`
	Source string `json:"source"`
	Title  string `json:"title"`
	Price  string `json:"price,omitempty"`
}

type discoverResponse struct {
	ProductID string            `json:"product_id"`
	Items     []discoveredItem  `json:"items"`
	Count     int               `json:"count"`
}

// Discover handles POST /discover.
func (h *Handler) Discover(w http.ResponseWriter, r *http.Request) {
	var req discoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ProductName = strings.TrimSpace(req.ProductName)
	if req.ProductName == "" || len(req.ProductName) > 200 {
		writeErr(w, http.StatusBadRequest, "product_name must be 1–200 characters")
		return
	}

	productID := uuid.New().String()
	slog.Info("discovery request received", "product_id", productID, "product", req.ProductName)

	results, err := h.agent.Discover(r.Context(), req.ProductName, req.Locale)
	if err != nil {
		slog.Error("agent discovery failed", "product_id", productID, "error", err)
		writeErr(w, http.StatusInternalServerError, "discovery failed")
		return
	}

	now := time.Now().UTC()
	msgs := make([]contracts.DiscoveredURL, len(results))
	items := make([]discoveredItem, len(results))
	for i, res := range results {
		msgs[i] = contracts.DiscoveredURL{
			ProductID:    productID,
			ProductName:  req.ProductName,
			URL:          res.URL,
			Source:       res.Source,
			DiscoveredAt: now,
		}
		items[i] = discoveredItem{
			URL:    res.URL,
			Source: res.Source,
			Title:  res.Title,
			Price:  res.Price,
		}
	}

	if len(msgs) > 0 {
		if err := h.publisher.PublishDiscoveredURLs(r.Context(), msgs); err != nil {
			slog.Error("publish to queue failed", "product_id", productID, "error", err)
			writeErr(w, http.StatusInternalServerError, "failed to queue discovered URLs")
			return
		}
	}

	slog.Info("discovery complete", "product_id", productID, "count", len(results))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(discoverResponse{
		ProductID: productID,
		Items:     items,
		Count:     len(items),
	})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
