package vnc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mtimpe/httpmux/internal/config"
	internalssh "github.com/mtimpe/httpmux/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type Session struct {
	ID         string
	TargetName string
	sshClient  *gossh.Client
	vncConn    net.Conn
	done       chan struct{}
	closeOnce  sync.Once
}

func New(ctx context.Context, pool *internalssh.BastionPool, target config.Target) (*Session, error) {
	if target.Desktop == nil || !target.Desktop.Enabled {
		return nil, fmt.Errorf("desktop not enabled for target %q", target.Name)
	}

	sshClient, err := pool.DialTarget(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("dial target %s: %w", target.Name, err)
	}

	remoteAddr := fmt.Sprintf("localhost:%d", target.Desktop.VNCPort)
	vncConn, err := internalssh.DialRemote(ctx, sshClient, remoteAddr)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("dial VNC on %s at %s: %w", target.Name, remoteAddr, err)
	}

	return &Session{
		ID:         fmt.Sprintf("vnc-%s-%d", target.Name, time.Now().UnixNano()),
		TargetName: target.Name,
		sshClient:  sshClient,
		vncConn:    vncConn,
		done:       make(chan struct{}),
	}, nil
}

func (s *Session) Bridge(ctx context.Context, ws *websocket.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)

	// VNC -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := s.vncConn.Read(buf)
			if n > 0 {
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

	// WebSocket -> VNC
	go func() {
		for {
			_, msg, err := ws.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			if _, err := s.vncConn.Write(msg); err != nil {
				errCh <- err
				return
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

func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.vncConn != nil {
			s.vncConn.Close()
		}
		if s.sshClient != nil {
			err = s.sshClient.Close()
		}
		slog.Info("closed VNC session", "target", s.TargetName, "session_id", s.ID)
	})
	return err
}
