package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/cron"
	"github.com/briggleman/kraken/internal/panel/store"
)

type scheduleRequest struct {
	Name    string `json:"name"`
	Action  string `json:"action"`
	Cron    string `json:"cron"`
	Command string `json:"command"`
	Enabled *bool  `json:"enabled"`
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sv, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if !s.authorizeServer(w, r.Context(), sv) {
		return
	}
	tasks, err := s.store.ListSchedulesByServer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list schedules")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": tasks})
}

// validateSchedule parses+validates a request and returns the resolved action,
// canonical cron text, and the next run time. enabled defaults to true.
func validateSchedule(req scheduleRequest) (store.ScheduleAction, string, time.Time, error) {
	action := store.ScheduleAction(req.Action)
	if !action.Valid() {
		return "", "", time.Time{}, errors.New("action must be one of restart|backup|command")
	}
	if action == store.ScheduleCommand && req.Command == "" {
		return "", "", time.Time{}, errors.New("command is required for the command action")
	}
	sched, err := cron.Parse(req.Cron)
	if err != nil {
		return "", "", time.Time{}, errors.New("invalid cron expression: " + err.Error())
	}
	return action, sched.String(), sched.Next(time.Now()), nil
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sv, err := s.store.GetServer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if !s.authorizeServer(w, r.Context(), sv) {
		return
	}
	var req scheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	action, cronText, next, err := validateSchedule(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	t := &store.ScheduledTask{
		ID:        uuid.NewString(),
		ServerID:  sv.ID,
		Name:      req.Name,
		Action:    action,
		Cron:      cronText,
		Command:   req.Command,
		Enabled:   enabled,
		CreatedAt: time.Now(),
	}
	if enabled && !next.IsZero() {
		t.NextRunAt = &next
	}
	if err := s.store.CreateSchedule(r.Context(), t); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create schedule")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := s.store.GetSchedule(r.Context(), chi.URLParam(r, "scheduleId"))
	if errors.Is(err, store.ErrNotFound) || (t != nil && t.ServerID != id) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get schedule")
		return
	}
	if sv, serr := s.store.GetServer(r.Context(), id); serr != nil || !s.mayAccessServer(r.Context(), sv) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	var req scheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	action, cronText, next, err := validateSchedule(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t.Name = req.Name
	t.Action = action
	t.Cron = cronText
	t.Command = req.Command
	if req.Enabled != nil {
		t.Enabled = *req.Enabled
	}
	if t.Enabled && !next.IsZero() {
		t.NextRunAt = &next
	} else {
		t.NextRunAt = nil
	}
	if err := s.store.UpdateSchedule(r.Context(), t); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update schedule")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := s.store.GetSchedule(r.Context(), chi.URLParam(r, "scheduleId"))
	if errors.Is(err, store.ErrNotFound) || (t != nil && t.ServerID != id) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get schedule")
		return
	}
	if sv, serr := s.store.GetServer(r.Context(), id); serr != nil || !s.mayAccessServer(r.Context(), sv) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	if err := s.store.DeleteSchedule(r.Context(), t.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete schedule")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
