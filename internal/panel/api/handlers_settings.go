package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// settingsResponse is the payload for the server Settings tab: the spec's grouped
// schema plus the server's current values, and the spec's launch variables with
// this server's resolved values.
type settingsResponse struct {
	Groups []spec.SettingGroup `json:"groups"`
	Values map[string]string   `json:"values"`
	// Variables are the spec's launch variables (rendered into the startup
	// command / container env at start). Editable only while the server is
	// stopped; edits apply on the next start.
	Variables []variableView `json:"variables"`
}

// variableView is a launch variable surfaced on the Settings tab: the spec's
// declaration plus this server's current value.
type variableView struct {
	Key          string `json:"key"`
	Label        string `json:"label,omitempty"`
	Value        string `json:"value"`
	Rules        string `json:"rules,omitempty"`
	UserEditable bool   `json:"user_editable"`
}

// variableViews pairs each spec variable with the server's stored value
// (falling back to the spec default for variables added after deploy).
func variableViews(sp *spec.Spec, sv *store.Server) []variableView {
	out := make([]variableView, 0, len(sp.Variables))
	for _, v := range sp.Variables {
		val, ok := sv.Vars[v.Key]
		if !ok {
			val = v.Default
		}
		out = append(out, variableView{Key: v.Key, Label: v.Label, Value: val, Rules: v.Rules, UserEditable: v.UserEditable})
	}
	return out
}

func (s *Server) handleGetServerSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, sp, ok := s.loadServerAndSpec(ctx, w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	// Ensure values include any settings added to the spec since creation.
	values := sp.ResolveSettings(sv.Settings)
	// Specs without a settings block have a nil Groups slice, which would
	// serialize as JSON null; the UI expects an array.
	groups := sp.Settings.Groups
	if groups == nil {
		groups = []spec.SettingGroup{}
	}
	writeJSON(w, http.StatusOK, settingsResponse{Groups: groups, Values: values, Variables: variableViews(sp, sv)})
}

type updateSettingsRequest struct {
	Values map[string]string `json:"values"`
	// Variables are launch-variable edits. Only accepted while the server is
	// stopped — they render into the startup command, so a running container
	// would silently keep the old values until its next start.
	Variables map[string]string `json:"variables,omitempty"`
}

func (s *Server) handleUpdateServerSettings(w http.ResponseWriter, r *http.Request) {
	var req updateSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	sv, sp, ok := s.loadServerAndSpec(ctx, w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	// Launch-variable edits: stopped servers only. The next start re-renders
	// the startup command and container env from the stored vars, so no
	// reinstall is needed (variables referenced by the install script only
	// take effect after a reinstall).
	if len(req.Variables) > 0 {
		switch sv.State {
		case store.StateRunning, store.StateStarting, store.StateStopping:
			writeError(w, http.StatusConflict, "stop the server to edit launch variables; they apply on the next start")
			return
		}
		if err := sp.ValidateVarOverrides(req.Variables); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		editable := make(map[string]bool, len(sp.Variables))
		for _, v := range sp.Variables {
			if v.UserEditable {
				editable[v.Key] = true
			}
		}
		if sv.Vars == nil {
			sv.Vars = map[string]string{}
		}
		for k, v := range req.Variables {
			if !editable[k] {
				writeError(w, http.StatusBadRequest, "variable "+k+" is not editable")
				return
			}
			sv.Vars[k] = v
		}
	}

	// Validate each supplied value against its field; ignore unknown keys.
	fieldByKey := map[string]spec.SettingField{}
	for _, g := range sp.Settings.Groups {
		for _, f := range g.Fields {
			fieldByKey[f.Key] = f
		}
	}
	merged := sp.ResolveSettings(sv.Settings)
	for k, v := range req.Values {
		f, known := fieldByKey[k]
		if !known {
			continue
		}
		if f.ReadOnly {
			if v != merged[k] {
				writeError(w, http.StatusBadRequest, "setting "+k+" is read-only")
				return
			}
			continue // unchanged read-only value: accept as a no-op
		}
		if err := spec.ValidateFieldValue(f, v); err != nil {
			writeError(w, http.StatusBadRequest, "setting "+k+": "+err.Error())
			return
		}
		merged[k] = v
	}
	sv.Settings = merged
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save settings")
		return
	}

	// Render config files and push them to the Agent.
	applied, err := s.applyConfig(ctx, sv, sp)
	if err != nil {
		writeError(w, http.StatusBadGateway, "settings saved but config apply failed: "+err.Error())
		return
	}

	restartNeeded := sv.State == store.StateRunning && applied
	writeJSON(w, http.StatusOK, map[string]any{
		"values":         sv.Settings,
		"variables":      variableViews(sp, sv),
		"applied":        applied,
		"restart_needed": restartNeeded,
	})
}

// applyConfig renders the spec's config files from the server's settings and
// sends them to the hosting Agent. Returns whether any files were applied.
func (s *Server) applyConfig(ctx context.Context, sv *store.Server, sp *spec.Spec) (bool, error) {
	if len(sp.ConfigFiles) == 0 {
		return false, nil
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		return false, err
	}
	client, err := s.nodes.Client(node.Address)
	if err != nil {
		return false, err
	}
	rctx := spec.RenderContext{Settings: sv.Settings, Vars: sv.Vars, Ports: sv.Ports}
	files := make([]*agentpb.RenderedFile, 0, len(sp.ConfigFiles))
	for _, cf := range sp.ConfigFiles {
		content, rerr := spec.RenderConfig(cf, rctx)
		if rerr != nil {
			return false, rerr
		}
		files = append(files, &agentpb.RenderedFile{Path: cf.Path, Content: content})
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := client.ApplyConfig(cctx, &agentpb.ApplyConfigRequest{ServerId: sv.ID, Files: files}); err != nil {
		return false, err
	}
	return true, nil
}

// loadServerAndSpec loads a server and its spec, writing the appropriate error
// response and returning ok=false on failure.
func (s *Server) loadServerAndSpec(ctx context.Context, w http.ResponseWriter, id string) (*store.Server, *spec.Spec, bool) {
	sv, err := s.store.GetServer(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return nil, nil, false
	}
	if !s.authorizeServer(w, ctx, sv) {
		return nil, nil, false
	}
	sp, err := s.store.GetSpec(ctx, sv.SpecID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load spec")
		return nil, nil, false
	}
	return sv, sp, true
}
