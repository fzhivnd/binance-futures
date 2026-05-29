package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	q      *Queries
	sq     *ScoringQueries
	static http.Handler
}

func NewHandler(pool *pgxpool.Pool, staticFS http.FileSystem) *Handler {
	return &Handler{
		q:      NewQueries(pool),
		sq:     NewScoringQueries(pool),
		static: http.FileServer(staticFS),
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/trades", h.trades)
	mux.HandleFunc("/api/summary", h.summary)
	mux.HandleFunc("/api/stats", h.stats)

	mux.HandleFunc("/api/scoring/configs", h.scoringConfigs)
	mux.HandleFunc("/api/scoring/configs/active", h.scoringActive)
	mux.HandleFunc("/api/scoring/configs/", h.scoringConfigByID)

	mux.Handle("/", h.static)
}

func (h *Handler) trades(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	from := time.Now().AddDate(0, -1, 0)
	to := time.Now().Add(24 * time.Hour)
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t.Add(24 * time.Hour)
		}
	}

	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	var isPaper *bool
	if v := q.Get("paper"); v != "" {
		b := v == "true" || v == "1"
		isPaper = &b
	}

	var result *string
	if v := q.Get("result"); v != "" {
		result = &v
	}

	trades, err := h.q.ListTrades(r.Context(), from, to, isPaper, result, limit)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if trades == nil {
		trades = []TradeRow{}
	}
	writeJSON(w, trades)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	limit := 90
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			limit = n
		}
	}

	var isPaper *bool
	if v := q.Get("paper"); v != "" {
		b := v == "true" || v == "1"
		isPaper = &b
	}

	summaries, err := h.q.ListSummaries(r.Context(), isPaper, limit)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if summaries == nil {
		summaries = []SummaryRow{}
	}
	writeJSON(w, summaries)
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var isPaper *bool
	if v := r.URL.Query().Get("paper"); v != "" {
		b := v == "true" || v == "1"
		isPaper = &b
	}

	stats, err := h.q.GetStats(r.Context(), isPaper)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if stats == nil {
		stats = []StatsRow{}
	}
	writeJSON(w, stats)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encode error: "+err.Error(), http.StatusInternalServerError)
	}
}

// GET /api/scoring/configs        — list all
// POST /api/scoring/configs       — create/clone from active
func (h *Handler) scoringConfigs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := h.sq.ListConfigs(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []ScoringConfigRow{}
		}
		writeJSON(w, rows)

	case http.MethodPost:
		var body struct {
			Name  string `json:"name"`
			Notes string `json:"notes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		id, err := h.sq.CreateConfig(r.Context(), body.Name, body.Notes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]int{"id": id})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /api/scoring/configs/active
func (h *Handler) scoringActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := h.sq.GetActiveConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cfg == nil {
		http.Error(w, "no active config", http.StatusNotFound)
		return
	}
	writeJSON(w, cfg)
}

// /api/scoring/configs/{id}[/action]
func (h *Handler) scoringConfigByID(w http.ResponseWriter, r *http.Request) {
	// strip prefix and split path segments
	path := strings.TrimPrefix(r.URL.Path, "/api/scoring/configs/")
	parts := strings.SplitN(path, "/", 2)

	id, err := strconv.Atoi(parts[0])
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// sub-action: /api/scoring/configs/{id}/activate|weights|thresholds|candle-weights|confidence-tiers|params
	if len(parts) == 2 {
		switch parts[1] {
		case "activate":
			if r.Method != http.MethodPut {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			changedBy := r.URL.Query().Get("by")
			if err := h.sq.ActivateConfig(r.Context(), id, changedBy); err != nil {
				if strings.Contains(err.Error(), "incomplete") {
					http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case "weights":
			if r.Method != http.MethodPut {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var body []ScoringWeightRow
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if err := h.sq.UpdateWeights(r.Context(), id, body); err != nil {
				code := http.StatusInternalServerError
				if strings.Contains(err.Error(), "cannot edit active") {
					code = http.StatusConflict
				}
				http.Error(w, err.Error(), code)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case "thresholds":
			if r.Method != http.MethodPut {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var body []ScoringThresholdRow
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if err := h.sq.UpdateThresholds(r.Context(), id, body); err != nil {
				code := http.StatusInternalServerError
				if strings.Contains(err.Error(), "cannot edit active") {
					code = http.StatusConflict
				}
				http.Error(w, err.Error(), code)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case "candle-weights":
			if r.Method != http.MethodPut {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var body []CandleWeightRow
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if err := h.sq.UpdateCandleWeights(r.Context(), id, body); err != nil {
				code := http.StatusInternalServerError
				if strings.Contains(err.Error(), "cannot edit active") {
					code = http.StatusConflict
				}
				http.Error(w, err.Error(), code)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case "confidence-tiers":
			if r.Method != http.MethodPut {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var body []ConfidenceTierRow
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if err := h.sq.UpdateConfidenceTiers(r.Context(), id, body); err != nil {
				code := http.StatusInternalServerError
				if strings.Contains(err.Error(), "cannot edit active") {
					code = http.StatusConflict
				}
				http.Error(w, err.Error(), code)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case "params":
			if r.Method != http.MethodPut {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var body BotParamsRow
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if err := h.sq.UpdateBotParams(r.Context(), id, body); err != nil {
				code := http.StatusInternalServerError
				if strings.Contains(err.Error(), "cannot edit active") {
					code = http.StatusConflict
				}
				http.Error(w, err.Error(), code)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "unknown action", http.StatusNotFound)
		}
		return
	}

	// /api/scoring/configs/{id}
	switch r.Method {
	case http.MethodGet:
		cfg, err := h.sq.GetConfig(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, cfg)

	case http.MethodDelete:
		if err := h.sq.DeleteConfig(r.Context(), id); err != nil {
			code := http.StatusInternalServerError
			if strings.Contains(err.Error(), "cannot delete active") {
				code = http.StatusConflict
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
