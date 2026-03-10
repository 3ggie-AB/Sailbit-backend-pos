package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

type sseClient struct {
	id       string
	tenantID string
	outletID string
	events   chan []byte
	done     chan struct{}
}

type SSEBroker struct {
	cache *Cache
	cfg   *SSEConfig

	mu       sync.RWMutex
	channels map[string]map[string]*sseClient // channel → clientID → client
}

func newSSEBroker(cache *Cache, cfg *SSEConfig) *SSEBroker {
	return &SSEBroker{
		cache:    cache,
		cfg:      cfg,
		channels: make(map[string]map[string]*sseClient),
	}
}

func (b *SSEBroker) subscribe(tenantID, outletID string) *sseClient {
	ch := sseChannel(tenantID, outletID)
	clientID := fmt.Sprintf("%s-%s-%d", tenantID[:8], outletID[:8], time.Now().UnixNano())

	client := &sseClient{
		id:       clientID,
		tenantID: tenantID,
		outletID: outletID,
		events:   make(chan []byte, b.cfg.BufferSize),
		done:     make(chan struct{}),
	}

	b.mu.Lock()
	if _, ok := b.channels[ch]; !ok {
		b.channels[ch] = make(map[string]*sseClient)
		go b.listenRedis(ch, tenantID, outletID)
	}
	b.channels[ch][clientID] = client
	b.mu.Unlock()

	return client
}

func (b *SSEBroker) unsubscribe(client *sseClient) {
	ch := sseChannel(client.tenantID, client.outletID)

	b.mu.Lock()
	defer b.mu.Unlock()

	if clients, ok := b.channels[ch]; ok {
		if c, ok := clients[client.id]; ok {
			close(c.done)
			delete(clients, client.id)
		}
		if len(clients) == 0 {
			delete(b.channels, ch)
		}
	}
}

func (b *SSEBroker) publish(ctx context.Context, tenantID, outletID string, event *SSEEvent) error {
	return b.cache.PublishSSE(ctx, tenantID, outletID, event)
}

// listenRedis subscribes to Redis channel and fans out to local SSE clients.
func (b *SSEBroker) listenRedis(ch, tenantID, outletID string) {
	ctx := context.Background()
	sub := b.cache.SubscribeSSE(ctx, tenantID, outletID)
	defer sub.Close()

	for msg := range sub.Channel() {
		payload := []byte(msg.Payload)

		b.mu.RLock()
		clients := b.channels[ch]
		b.mu.RUnlock()

		for _, client := range clients {
			select {
			case client.events <- payload:
			default:
				// Buffer full: drop oldest, push newest
				select {
				case <-client.events:
				default:
				}
				client.events <- payload
			}
		}
	}
}

// ServeSSE is the Fiber handler for GET /api/v1/events
func (b *SSEBroker) ServeSSE(c *fiber.Ctx) error {
	tenantID := c.Locals("tenant_id").(string)
	outletID := c.Locals("outlet_id").(string)

	client := b.subscribe(tenantID, outletID)

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		// Send connected confirmation
		fmt.Fprintf(w, "event: connected\ndata: {\"client_id\":\"%s\"}\n\n", client.id)
		w.Flush()

		heartbeat := time.NewTicker(b.cfg.HeartbeatInterval)
		defer heartbeat.Stop()
		defer b.unsubscribe(client)

		for {
			select {
			case data := <-client.events:
				fmt.Fprintf(w, "data: %s\n\n", data)
				if err := w.Flush(); err != nil {
					return
				}

			case <-heartbeat.C:
				// Keeps connection alive through proxies
				fmt.Fprintf(w, ": heartbeat\n\n")
				if err := w.Flush(); err != nil {
					return
				}

			case <-client.done:
				return
			}
		}
	})

	return nil
}

// formatSSE builds a raw SSE payload string.
func formatSSE(eventType string, payload any) []byte {
	data, _ := json.Marshal(payload)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data))
}

// Suppress unused import
var _ = fasthttp.StatusOK