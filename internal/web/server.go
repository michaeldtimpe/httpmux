package web

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/mtimpe/httpmux/internal/auth"
	"github.com/mtimpe/httpmux/internal/config"
	"github.com/mtimpe/httpmux/internal/session"
)

//go:embed all:templates
var templateFS embed.FS

//go:embed all:static
var staticFS embed.FS

type Server struct {
	config   *config.Config
	auth     *auth.Authenticator
	sessions *session.Manager
	mux      *http.ServeMux
	tmpl     *template.Template
	server   *http.Server
}

func New(cfg *config.Config, authenticator *auth.Authenticator, sessions *session.Manager) *Server {
	s := &Server{
		config:   cfg,
		auth:     authenticator,
		sessions: sessions,
		mux:      http.NewServeMux(),
	}

	s.tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	staticRoot, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticRoot))

	s.mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
	s.mux.HandleFunc("GET /login", s.handleLoginPage)
	s.mux.HandleFunc("POST /login", s.handleLoginSubmit)
	s.mux.HandleFunc("POST /logout", s.handleLogout)

	protected := s.auth.Middleware(http.HandlerFunc(s.handleDashboard))
	s.mux.Handle("GET /{$}", protected)

	s.mux.Handle("GET /terminal/{name}", s.auth.Middleware(http.HandlerFunc(s.handleTerminalPage)))
	s.mux.Handle("GET /desktop/{name}", s.auth.Middleware(http.HandlerFunc(s.handleDesktopPage)))

	s.mux.Handle("GET /api/targets", s.auth.Middleware(http.HandlerFunc(s.handleListTargets)))
	s.mux.Handle("GET /api/sessions", s.auth.Middleware(http.HandlerFunc(s.handleListSessions)))
	s.mux.Handle("DELETE /api/sessions/{id}", s.auth.Middleware(http.HandlerFunc(s.handleDeleteSession)))

	s.mux.Handle("GET /ws/terminal/{name}", s.auth.Middleware(http.HandlerFunc(s.handleTerminalWS)))
	s.mux.Handle("GET /ws/desktop/{name}", s.auth.Middleware(http.HandlerFunc(s.handleDesktopWS)))
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	s.server = &http.Server{
		Addr:    s.config.Server.Listen,
		Handler: s.mux,
		// No ReadTimeout/WriteTimeout — WebSocket connections are long-lived.
		// Individual HTTP handlers set their own deadlines.
		IdleTimeout: 120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.server.Shutdown(shutdownCtx)
	}()

	if s.config.Server.TLS != nil {
		return s.server.ListenAndServeTLS(s.config.Server.TLS.Cert, s.config.Server.TLS.Key)
	}
	return s.server.ListenAndServe()
}
