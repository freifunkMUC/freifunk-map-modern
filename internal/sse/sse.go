package sse

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// Hub manages Server-Sent Event connections.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

const maxSSEClients = 1000

func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan []byte]struct{}),
	}
}

// Subscribe returns a channel for receiving SSE data, or nil if the limit is reached.
func (h *Hub) Subscribe() chan []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= maxSSEClients {
		return nil
	}
	ch := make(chan []byte, 16)
	h.clients[ch] = struct{}{}
	return ch
}

func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// Broadcast sends an SSE update to all connected clients.
// Accepts any JSON-marshalable value (typically *store.SSEUpdate).
func (h *Hub) Broadcast(update interface{}) {
	data, err := json.Marshal(update)
	if err != nil {
		log.Printf("SSE marshal error: %v", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for ch := range h.clients {
		select {
		case ch <- data:
		default:
			// Client too slow, skip
		}
	}
}

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// HandleSSE returns an http.HandlerFunc for SSE streaming.
func HandleSSE(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Disable write deadline for SSE (long-lived connection)
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})

		ch := hub.Subscribe()
		if ch == nil {
			http.Error(w, "Too many SSE clients", http.StatusServiceUnavailable)
			return
		}
		log.Printf("SSE client connected from %s (%d total)", r.RemoteAddr, hub.ClientCount())
		defer func() {
			hub.Unsubscribe(ch)
			log.Printf("SSE client disconnected from %s (%d total)", r.RemoteAddr, hub.ClientCount())
		}()

		fmt.Fprintf(w, ": connected\n\n")
		flusher.Flush()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case data, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}
