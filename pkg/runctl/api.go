package runctl

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Routes returns a chi.Router with all API routes mounted.
// Caller mounts it at any prefix: mainRouter.Mount("/api", ctrl.Routes())
func (this *Controller) Routes() chi.Router {
	r := chi.NewRouter()

	r.Get("/health", this.handleHealth)
	r.Get("/targets", this.handleListTargets)
	r.Get("/targets/{name}", this.handleGetTarget)
	r.Post("/targets/{name}/build", this.handleBuildTarget)
	r.Post("/targets/{name}/start", this.handleStartExec)
	r.Post("/targets/{name}/stop", this.handleStopExec)
	r.Post("/targets/{name}/restart", this.handleRestartTarget)
	r.Post("/targets/{name}/enable", this.handleEnableTarget)
	r.Post("/targets/{name}/disable", this.handleDisableTarget)
	r.Get("/targets/{name}/logs", this.handleGetLogs)

	return r
}

func (this *Controller) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (this *Controller) handleListTargets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, this.Status())
}

func (this *Controller) handleGetTarget(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	status, err := this.TargetStatus(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (this *Controller) handleBuildTarget(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := this.BuildTarget(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "building"})
}

func (this *Controller) handleStartExec(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := this.StartExec(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (this *Controller) handleStopExec(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := this.StopExec(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (this *Controller) handleRestartTarget(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := this.BuildTarget(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

func (this *Controller) handleEnableTarget(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := this.EnableTarget(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "enabled"})
}

func (this *Controller) handleDisableTarget(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := this.DisableTarget(name); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}

func (this *Controller) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "target not found")
		return
	}

	stage := r.URL.Query().Get("stage")
	if stage == "" {
		stage = "run"
	}
	if stage != "build" && stage != "run" {
		writeError(w, http.StatusBadRequest, "stage must be build or run")
		return
	}

	if t.tcfg.Logs == nil {
		writeError(w, http.StatusBadRequest, "no logs configured for this target")
		return
	}

	var path string
	switch stage {
	case "build":
		path = t.tcfg.Logs.Build
	case "run":
		path = t.tcfg.Logs.Run
	}
	if path == "" {
		writeError(w, http.StatusBadRequest, "no "+stage+" log configured for this target")
		return
	}

	// Check if using line-range mode (offset/limit params)
	if r.URL.Query().Has("offset") || r.URL.Query().Has("limit") {
		offset := 0
		limit := 500
		if s := r.URL.Query().Get("offset"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				offset = n
			}
		}
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				limit = n
			}
		}

		lines, totalLines, err := readLineRange(path, offset, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if lines == nil {
			lines = []string{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"lines":      lines,
			"totalLines": totalLines,
			"offset":     offset,
			"file":       path,
		})
		return
	}

	// Legacy tail mode (lines param)
	lines := 200
	if s := r.URL.Query().Get("lines"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			lines = n
		}
	}

	result, err := tailFile(path, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"lines": result,
		"file":  path,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
