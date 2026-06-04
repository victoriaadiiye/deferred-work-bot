package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/slack-go/slack"
)

type HealthDeps struct {
	Store        *Store
	Worker       *Worker
	TriggerToken string
	Slack        SlackAPI
	Metrics      *AppMetrics
}

type HealthServer struct{ deps HealthDeps }

func NewHealthServer(d HealthDeps) *HealthServer { return &HealthServer{deps: d} }

func (h *HealthServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.dashboard)
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/metrics", h.metrics)
	mux.HandleFunc("/trigger", h.trigger)
	mux.HandleFunc("/file-now", h.fileNow)
	return mux
}

func (h *HealthServer) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, h.handler())
}

func (h *HealthServer) health(w http.ResponseWriter, r *http.Request) {
	if h.deps.Store == nil || h.deps.Store.db.Ping() != nil {
		http.Error(w, "db unreachable", 503)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok\n"))
}

func (h *HealthServer) metrics(w http.ResponseWriter, r *http.Request) {
	statuses := []string{"collecting", "proposing", "proposed", "ticketed", "commented_on_existing", "cancelled", "archived"}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP queue_depth Worker queue depth\nqueue_depth %d\n", h.deps.Worker.QueueDepth())
	for _, st := range statuses {
		n := 0
		row := h.deps.Store.db.QueryRow(`SELECT COUNT(*) FROM items WHERE status = ?`, st)
		row.Scan(&n)
		fmt.Fprintf(w, `items_by_status{status=%q} %d`+"\n", st, n)
	}
	if h.deps.Metrics != nil {
		h.deps.Metrics.WriteMetrics(w)
	}
}

func (h *HealthServer) trigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	if h.deps.TriggerToken != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+h.deps.TriggerToken {
			http.Error(w, "unauthorized", 401)
			return
		}
	}
	itemID, err := strconv.ParseInt(r.URL.Query().Get("item_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad item_id", 400)
		return
	}
	action := r.URL.Query().Get("action")
	switch action {
	case "propose":
		h.deps.Worker.Submit(ProposeJob{ItemID: itemID})
	case "reminder":
		h.deps.Worker.Submit(ReminderJob{ItemID: itemID})
	case "archive":
		it, err := h.deps.Store.GetItemByID(itemID)
		if err != nil {
			http.Error(w, "item not found", 404)
			return
		}
		if !isTerminal(it.Status) {
			h.deps.Store.UpdateItemStatus(it.ID, "archived")
			h.deps.Store.LogEvent(&it.ID, "archive", `{"via":"trigger"}`)
			// Best effort: react on original message; ignore Slack errors.
			if h.deps.Slack != nil {
				h.deps.Slack.AddReaction("wastebasket", slack.ItemRef{Channel: it.SlackChannel, Timestamp: it.SlackTS})
			}
		}
	default:
		http.Error(w, "bad action", 400)
		return
	}
	w.WriteHeader(202)
}

// fileNow advances a collecting item straight to proposal, mirroring the
// Slack "@bot file now" command. Form POST from the dashboard; no auth,
// same trust level as the dashboard itself.
func (h *HealthServer) fileNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	itemID, err := strconv.ParseInt(r.FormValue("item_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad item_id", 400)
		return
	}
	it, err := h.deps.Store.GetItemByID(itemID)
	if err != nil {
		http.Error(w, "item not found", 404)
		return
	}
	if it.Status == "collecting" {
		h.deps.Store.UpdateItemStatus(it.ID, "proposing")
		h.deps.Store.LogEvent(&it.ID, "advanced", `{"reason":"file_now","via":"dashboard"}`)
		h.deps.Worker.Submit(ProposeJob{ItemID: it.ID})
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
