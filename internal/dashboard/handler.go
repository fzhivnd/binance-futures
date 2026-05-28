package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	q       *Queries
	static  http.Handler
}

func NewHandler(pool *pgxpool.Pool, staticFS http.FileSystem) *Handler {
	return &Handler{
		q:      NewQueries(pool),
		static: http.FileServer(staticFS),
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/trades", h.trades)
	mux.HandleFunc("/api/summary", h.summary)
	mux.HandleFunc("/api/stats", h.stats)
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
