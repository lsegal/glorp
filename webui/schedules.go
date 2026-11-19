package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/lsegal/glorp/core"
)

// serveSchedules backs the Schedules modal and the schedule API (issue #848):
//
//	GET    /api/schedules            list schedules
//	POST   /api/schedules            create a schedule
//	POST   /api/schedules/preview    the next fires of a trigger
//	PATCH  /api/schedules/{id}       change a schedule
//	DELETE /api/schedules/{id}       delete a schedule
//	POST   /api/schedules/{id}/run   start a run now
func (ui *Server) serveSchedules(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/schedules"), "/")
	parts := []string{}
	if rest != "" {
		parts = strings.Split(rest, "/")
	}
	var request core.ScheduleRequest
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		request.Op = core.ScheduleOpList
	case len(parts) == 0 && r.Method == http.MethodPost:
		request.Op = core.ScheduleOpCreate
		if !decodeScheduleBody(w, r, &request.Schedule) {
			return
		}
	case len(parts) == 1 && parts[0] == "preview" && r.Method == http.MethodPost:
		request.Op = core.ScheduleOpPreview
		if !decodeScheduleBody(w, r, &request.Trigger) {
			return
		}
	case len(parts) == 1 && r.Method == http.MethodPatch:
		request.Op, request.ID = core.ScheduleOpUpdate, parts[0]
		if !decodeScheduleBody(w, r, &request.Update) {
			return
		}
	case len(parts) == 1 && r.Method == http.MethodDelete:
		request.Op, request.ID = core.ScheduleOpDelete, parts[0]
	case len(parts) == 2 && parts[1] == "run" && r.Method == http.MethodPost:
		request.Op, request.ID = core.ScheduleOpRun, parts[0]
	case len(parts) <= 2:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	default:
		http.NotFound(w, r)
		return
	}
	ui.mu.RLock()
	handler := ui.schedules
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "schedules unavailable", http.StatusServiceUnavailable)
		return
	}
	response, err := handler(r.Context(), request)
	if err != nil {
		if errors.Is(err, core.ErrNotReady) {
			http.Error(w, "schedules unavailable", http.StatusServiceUnavailable)
			return
		}
		status := http.StatusBadRequest
		if strings.HasSuffix(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	if request.Op == core.ScheduleOpDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Op == core.ScheduleOpList && response.Schedules == nil {
		response.Schedules = []core.ScheduleView{}
	}
	w.Header().Set("Content-Type", "application/json")
	if request.Op == core.ScheduleOpCreate {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(response)
}

func decodeScheduleBody(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		http.Error(w, "invalid schedule request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
