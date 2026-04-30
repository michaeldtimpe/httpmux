package web

import (
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
)

func acceptWebSocket(w http.ResponseWriter, r *http.Request, subprotocols ...string) (*websocket.Conn, error) {
	opts := &websocket.AcceptOptions{
		Subprotocols: subprotocols,
	}
	ws, err := websocket.Accept(w, r, opts)
	if err != nil {
		slog.Error("websocket accept failed", "error", err, "remote", r.RemoteAddr)
		return nil, err
	}
	return ws, nil
}
