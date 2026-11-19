package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lsegal/glorp/core"
)

func TestServeSchedulesRoutesEachRequest(t *testing.T) {
	ui, _ := New("test")
	var got []core.ScheduleRequest
	ui.SetScheduleHandler(func(_ context.Context, request core.ScheduleRequest) (core.ScheduleResponse, error) {
		got = append(got, request)
		return core.ScheduleResponse{}, nil
	})
	for _, test := range []struct {
		method, path, body string
		status             int
		op, id             string
	}{
		{http.MethodGet, "/api/schedules", "", http.StatusOK, core.ScheduleOpList, ""},
		{http.MethodPost, "/api/schedules", `{"name":"n","repository":"o/r","prompt":"p","trigger":{"kind":"daily","time":"09:00"}}`, http.StatusCreated, core.ScheduleOpCreate, ""},
		{http.MethodPost, "/api/schedules/preview", `{"kind":"interval","every":"1h"}`, http.StatusOK, core.ScheduleOpPreview, ""},
		{http.MethodPatch, "/api/schedules/abc", `{"enabled":false}`, http.StatusOK, core.ScheduleOpUpdate, "abc"},
		{http.MethodDelete, "/api/schedules/abc", "", http.StatusNoContent, core.ScheduleOpDelete, "abc"},
		{http.MethodPost, "/api/schedules/abc/run", "", http.StatusOK, core.ScheduleOpRun, "abc"},
	} {
		got = nil
		recorder := httptest.NewRecorder()
		ui.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
		if recorder.Code != test.status {
			t.Errorf("%s %s = %d %s, want %d", test.method, test.path, recorder.Code, recorder.Body, test.status)
			continue
		}
		if len(got) != 1 || got[0].Op != test.op || got[0].ID != test.id {
			t.Errorf("%s %s sent %+v", test.method, test.path, got)
		}
	}
	if len(got) == 1 && got[0].Update.Enabled != nil && *got[0].Update.Enabled {
		t.Error("patch body not decoded")
	}
}

func TestServeSchedulesReportsNotReadyAndErrors(t *testing.T) {
	ui, _ := New("test")
	recorder := httptest.NewRecorder()
	ui.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a handler = %d", recorder.Code)
	}
	ui.SetScheduleHandler(func(_ context.Context, request core.ScheduleRequest) (core.ScheduleResponse, error) {
		if request.Op == core.ScheduleOpList {
			return core.ScheduleResponse{}, core.ErrNotReady
		}
		return core.ScheduleResponse{}, errorString(`schedule "x" not found`)
	})
	recorder = httptest.NewRecorder()
	ui.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("not ready = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	ui.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/schedules/x", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing schedule = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	ui.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(`{"bogus":1}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", recorder.Code)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
