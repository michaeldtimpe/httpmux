package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mtimpe/httpmux/internal/config"
	internalssh "github.com/mtimpe/httpmux/internal/ssh"
	"github.com/mtimpe/httpmux/internal/terminal"
	"github.com/mtimpe/httpmux/internal/vnc"
)

type SessionType int

const (
	TypeTerminal SessionType = iota
	TypeVNC
)

type SessionInfo struct {
	ID         string      `json:"id"`
	Type       SessionType `json:"type"`
	TargetName string      `json:"target_name"`
	CreatedAt  time.Time   `json:"created_at"`
}

type Manager struct {
	mu              sync.RWMutex
	termSessions    map[string]*terminal.Session // keyed by target name
	pool            *internalssh.BastionPool
	config          *config.Config
	terminalGrace   time.Duration
}

func NewManager(pool *internalssh.BastionPool, cfg *config.Config) *Manager {
	return &Manager{
		termSessions:  make(map[string]*terminal.Session),
		pool:          pool,
		config:        cfg,
		terminalGrace: 5 * time.Minute,
	}
}

func (m *Manager) GetOrCreateTerminal(ctx context.Context, targetName string, cols, rows int) (*terminal.Session, error) {
	m.mu.Lock()
	if ts, ok := m.termSessions[targetName]; ok && !ts.IsClosed() {
		m.mu.Unlock()
		return ts, nil
	}
	m.mu.Unlock()

	target, ok := m.config.TargetByName(targetName)
	if !ok {
		return nil, fmt.Errorf("target %q not found", targetName)
	}
	if target.Terminal == nil || !target.Terminal.Enabled {
		return nil, fmt.Errorf("terminal not enabled for target %q", targetName)
	}

	ts, err := terminal.New(ctx, m.pool, *target, target.Terminal.DefaultSession, cols, rows)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if existing, ok := m.termSessions[targetName]; ok && !existing.IsClosed() {
		m.mu.Unlock()
		ts.Close()
		return existing, nil
	}
	m.termSessions[targetName] = ts
	m.mu.Unlock()

	slog.Info("created terminal session", "target", targetName, "session_id", ts.ID)
	return ts, nil
}

func (m *Manager) CreateVNC(ctx context.Context, targetName string) (*vnc.Session, error) {
	target, ok := m.config.TargetByName(targetName)
	if !ok {
		return nil, fmt.Errorf("target %q not found", targetName)
	}
	if target.Desktop == nil || !target.Desktop.Enabled {
		return nil, fmt.Errorf("desktop not enabled for target %q", targetName)
	}

	vs, err := vnc.New(ctx, m.pool, *target)
	if err != nil {
		return nil, err
	}

	slog.Info("created VNC session", "target", targetName, "session_id", vs.ID)
	return vs, nil
}

func (m *Manager) RemoveTerminal(targetName string) {
	m.mu.Lock()
	ts, ok := m.termSessions[targetName]
	if ok {
		delete(m.termSessions, targetName)
	}
	m.mu.Unlock()
	if ok && ts != nil {
		ts.Close()
		slog.Info("removed terminal session", "target", targetName)
	}
}

func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sessions []SessionInfo
	for _, ts := range m.termSessions {
		if ts.IsClosed() {
			continue
		}
		sessions = append(sessions, SessionInfo{
			ID:         ts.ID,
			Type:       TypeTerminal,
			TargetName: ts.TargetName,
			CreatedAt:  ts.LastActive(),
		})
	}
	return sessions
}

func (m *Manager) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.cleanup()
			}
		}
	}()
}

func (m *Manager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, ts := range m.termSessions {
		if ts.IsClosed() {
			delete(m.termSessions, name)
			slog.Info("cleaned up closed terminal session", "target", name)
			continue
		}
		if time.Since(ts.LastActive()) > m.terminalGrace {
			ts.Close()
			delete(m.termSessions, name)
			slog.Info("cleaned up idle terminal session", "target", name)
		}
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, ts := range m.termSessions {
		ts.Close()
		delete(m.termSessions, name)
	}
}
