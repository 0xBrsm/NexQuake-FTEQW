package main

import (
	"net/http"

	"github.com/0xBrsm/NexQuake/nexus/trunk"
	"github.com/0xBrsm/NexQuake/nexus/trunk/websocket"
)

// setupWebSocket mounts WebSocket on the shared /connect route.
// WebSocket rides on Nexus's existing TCP HTTP listener, so there is no second
// listener and no setup error to surface.
func setupWebSocket(app *nexusApp, mux *http.ServeMux) {
	mux.HandleFunc("GET /connect", app.handleWebSocket)
}

func (app *nexusApp) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	app.trunkSession(w, r, trunk.TransportWebSocket, func() (trunk.Transport, error) {
		c, err := websocket.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return nil, err
		}
		return websocket.New(c), nil
	})
}
