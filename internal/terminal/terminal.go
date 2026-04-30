package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mtimpe/httpmux/internal/config"
	internalssh "github.com/mtimpe/httpmux/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const (
	msgTypeData   byte = 0x00
	msgTypeResize byte = 0x01
)

type resizeMsg struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type Session struct {
	ID         string
	TargetName string
	TmuxName   string

	sshClient  *gossh.Client
	pty        *internalssh.PTYSession
	cols, rows int

	mu         sync.Mutex
	closed     bool
	done       chan struct{}
	lastActive time.Time
}

func New(ctx context.Context, pool *internalssh.BastionPool, target config.Target, sessionName string, cols, rows int) (*Session, error) {
	sshClient, err := pool.DialTarget(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("dial target %s: %w", target.Name, err)
	}

	cmd := fmt.Sprintf("tmux new-session -A -s %s", sessionName)

	pty, err := internalssh.OpenPTY(sshClient, cols, rows, cmd)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("open pty on %s: %w", target.Name, err)
	}

	s := &Session{
		ID:         fmt.Sprintf("%s-%d", target.Name, time.Now().UnixNano()),
		TargetName: target.Name,
		TmuxName:   sessionName,
		sshClient:  sshClient,
		pty:        pty,
		cols:       cols,
		rows:       rows,
		done:       make(chan struct{}),
		lastActive: time.Now(),
	}

	go func() {
		s.pty.Session.Wait()
		s.mu.Lock()
		if !s.closed {
			s.closed = true
			close(s.done)
		}
		s.mu.Unlock()
	}()

	return s, nil
}

func (s *Session) Attach(ctx context.Context, ws *websocket.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)

	// PTY stdout -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := s.pty.Stdout.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.lastActive = time.Now()
				s.mu.Unlock()

				if wErr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); wErr != nil {
					errCh <- wErr
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errCh <- err
				} else {
					errCh <- nil
				}
				return
			}
		}
	}()

	// WebSocket -> PTY stdin (with type prefix protocol)
	go func() {
		for {
			_, msg, err := ws.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			if len(msg) == 0 {
				continue
			}

			s.mu.Lock()
			s.lastActive = time.Now()
			s.mu.Unlock()

			switch msg[0] {
			case msgTypeData:
				if _, err := s.pty.Stdin.Write(msg[1:]); err != nil {
					errCh <- err
					return
				}
			case msgTypeResize:
				var r resizeMsg
				if err := json.Unmarshal(msg[1:], &r); err != nil {
					slog.Warn("invalid resize message", "error", err)
					continue
				}
				if err := s.Resize(r.Cols, r.Rows); err != nil {
					slog.Warn("resize failed", "error", err)
				}
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols = cols
	s.rows = rows
	return internalssh.ResizePTY(s.pty.Session, cols, rows)
}

func (s *Session) LastActive() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActive
}

func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)
	s.pty.Session.Close()
	return s.sshClient.Close()
}
