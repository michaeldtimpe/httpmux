package web

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
)

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth.ValidateRequest(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", nil)
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if !s.auth.Authenticate(username, password) {
		s.render(w, "login.html", map[string]any{"Error": "Invalid username or password"})
		return
	}

	s.auth.SetSession(w, username)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, "dashboard.html", map[string]any{
		"Targets": s.config.Targets,
	})
}

func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	target, ok := s.config.TargetByName(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if target.Terminal == nil || !target.Terminal.Enabled {
		http.Error(w, "terminal not enabled for this target", http.StatusForbidden)
		return
	}
	s.render(w, "terminal.html", map[string]any{
		"Target": target,
	})
}

func (s *Server) handleDesktopPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	target, ok := s.config.TargetByName(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if target.Desktop == nil || !target.Desktop.Enabled {
		http.Error(w, "desktop not enabled for this target", http.StatusForbidden)
		return
	}
	s.render(w, "desktop.html", map[string]any{
		"Target": target,
	})
}

type targetInfo struct {
	Name            string `json:"name"`
	Host            string `json:"host"`
	TerminalEnabled bool   `json:"terminal_enabled"`
	DesktopEnabled  bool   `json:"desktop_enabled"`
}

func (s *Server) handleListTargets(w http.ResponseWriter, r *http.Request) {
	targets := make([]targetInfo, 0, len(s.config.Targets))
	for _, t := range s.config.Targets {
		info := targetInfo{
			Name: t.Name,
			Host: t.Host,
		}
		if t.Terminal != nil {
			info.TerminalEnabled = t.Terminal.Enabled
		}
		if t.Desktop != nil {
			info.DesktopEnabled = t.Desktop.Enabled
		}
		targets = append(targets, info)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(targets)
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	target, ok := s.config.TargetByName(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if target.Terminal == nil || !target.Terminal.Enabled {
		http.Error(w, "terminal not enabled", http.StatusForbidden)
		return
	}

	ws, err := acceptWebSocket(w, r)
	if err != nil {
		return
	}
	defer ws.CloseNow()

	// Wait for initial resize message to get terminal dimensions
	cols, rows := 80, 24
	ctx := r.Context()
	_, msg, err := ws.Read(ctx)
	if err == nil && len(msg) > 1 && msg[0] == 0x01 {
		var resize struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		}
		if json.Unmarshal(msg[1:], &resize) == nil && resize.Cols > 0 && resize.Rows > 0 {
			cols = resize.Cols
			rows = resize.Rows
		}
	}

	ts, err := s.sessions.GetOrCreateTerminal(ctx, name, cols, rows)
	if err != nil {
		slog.Error("failed to create terminal session", "target", name, "error", err)
		ws.Close(websocket.StatusInternalError, "failed to create session")
		return
	}

	if err := ts.Attach(ctx, ws); err != nil {
		slog.Debug("terminal session detached", "target", name, "error", err)
	}
	ws.Close(websocket.StatusNormalClosure, "session ended")
}

func (s *Server) handleDesktopWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	target, ok := s.config.TargetByName(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if target.Desktop == nil || !target.Desktop.Enabled {
		http.Error(w, "desktop not enabled", http.StatusForbidden)
		return
	}

	ws, err := acceptWebSocket(w, r, "binary")
	if err != nil {
		return
	}
	defer ws.CloseNow()

	ctx := r.Context()
	vncSession, err := s.sessions.CreateVNC(ctx, name)
	if err != nil {
		slog.Error("failed to create VNC session", "target", name, "error", err)
		ws.Close(websocket.StatusInternalError, "failed to create VNC session")
		return
	}
	defer vncSession.Close()

	if err := vncSession.Bridge(ctx, ws); err != nil {
		slog.Debug("VNC session ended", "target", name, "error", err)
	}
	ws.Close(websocket.StatusNormalClosure, "session ended")
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions := s.sessions.List()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.sessions.RemoveTerminal(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template render error", "template", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
