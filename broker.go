package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/3ggie-AB/Sailbit-backend-pos/cache"
	"github.com/3ggie-AB/Sailbit-backend-pos/config"
	"github.com/3ggie-AB/Sailbit-backend-pos/domain"
	"go.uber.org/zap"
)

// Client represents one SSE subscriber (one browser/terminal tab).
type Client struct {
	ID       string
	TenantID string
	OutletID string
	Events   chan []byte
	Done     chan struct{}
}

// Broker manages all SSE clients and routes events from Redis pub/sub.
type Broker struct {
	cfg    *config.SSEConfig
	cache  *cache.Client
	log    *zap.Logger

	mu      sync.RWMutex
	// channel key → set of clients
	channels map[string]map[string]*Client
}

func NewBroker(cfg *config.SSEConfig, cache *cache.Client, log *zap.Logger) *Broker {
	b := &Broker{
		cfg:      cfg,
		cache:    cache,
		log:      log,
		channels: make(map[string]map[string]*Client),
	}
	return b
}

// Subscribe registers a new SSE client and returns it.
func (b *Broker) Subscribe(tenantID, outletID, clientID string) *Client {
	ch := cache.SSEChannel(tenantID, outletID)

	client := &Client{
		ID:       clientID,
		TenantID: tenantID,
		OutletID: outletID,
		Events:   make(chan []byte, b.cfg.BufferSize),
		Done:     make(chan struct{}),
	}

	b.mu.Lock()
	if _, ok := b.channels[ch]; !ok {
		b.channels[ch] = make(map[string]*Client)
		// Start Redis subscriber for this channel
		go b.listenRedis(ch)
	}
	b.channels[ch][clientID] = client
	b.mu.Unlock()

	b.log.Debug("SSE client subscribed",
		zap.String("tenant", tenantID),
		zap.String("outlet", outletID),
		zap.String("client", clientID),
	)

	return client
}

// Unsubscribe removes a client.
func (b *Broker) Unsubscribe(tenantID, outletID, clientID string) {
	ch := cache.SSEChannel(tenantID, outletID)

	b.mu.Lock()
	defer b.mu.Unlock()

	if clients, ok := b.channels[ch]; ok {
		if client, ok := clients[clientID]; ok {
			close(client.Done)
			delete(clients, clientID)
		}
		if len(clients) == 0 {
			delete(b.channels, ch)
		}
	}
}

// Publish sends an event to all clients of a channel via Redis pub/sub.
// Redis handles fan-out across multiple server instances.
func (b *Broker) Publish(ctx context.Context, tenantID, outletID string, event *domain.SSEEvent) error {
	return b.cache.PublishSSE(ctx, tenantID, outletID, event)
}

// listenRedis subscribes to a Redis channel and fans out to local clients.
func (b *Broker) listenRedis(channel string) {
	ctx := context.Background()
	sub := b.cache.SubscribeSSE(ctx, extractTenantOutlet(channel))
	defer sub.Close()

	ch := sub.Channel()
	for msg := range ch {
		b.fanOut(channel, []byte(msg.Payload))
	}
}

func (b *Broker) fanOut(channel string, data []byte) {
	b.mu.RLock()
	clients := b.channels[channel]
	b.mu.RUnlock()

	for _, client := range clients {
		select {
		case client.Events <- data:
		default:
			// Buffer full — drop oldest, push newest
			select {
			case <-client.Events:
			default:
			}
			client.Events <- data
		}
	}
}

// ServeHTTP handles the SSE HTTP endpoint.
func (b *Broker) ServeHTTP(c *fiber.Ctx) error {
	tenantID := c.Locals("tenant_id").(string)
	outletID := c.Locals("outlet_id").(string)
	clientID := fmt.Sprintf("%s-%s-%d", tenantID, outletID, time.Now().UnixNano())

	client := b.Subscribe(tenantID, outletID, clientID)
	defer b.Unsubscribe(tenantID, outletID, clientID)

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no") // Disable nginx buffering

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		heartbeat := time.NewTicker(b.cfg.HeartbeatInterval)
		defer heartbeat.Stop()

		// Send connected event
		fmt.Fprintf(w, "event: connected\ndata: {\"client_id\":\"%s\"}\n\n", clientID)
		w.Flush()

		for {
			select {
			case data := <-client.Events:
				fmt.Fprintf(w, "data: %s\n\n", data)
				if err := w.Flush(); err != nil {
					return
				}

			case <-heartbeat.C:
				fmt.Fprintf(w, ": heartbeat\n\n")
				if err := w.Flush(); err != nil {
					return
				}

			case <-client.Done:
				return
			}
		}
	})

	return nil
}

func extractTenantOutlet(channel string) (string, string) {
	// channel format: pos:events:{tenant_id}:{outlet_id}
	parts := splitN(channel, ":", 4)
	if len(parts) < 4 {
		return "", ""
	}
	return parts[2], parts[3]
}

func splitN(s, sep string, n int) []string {
	var result []string
	for i := 0; i < n-1; i++ {
		idx := indexOf(s, sep)
		if idx < 0 {
			break
		}
		result = append(result, s[:idx])
		s = s[idx+len(sep):]
	}
	result = append(result, s)
	return result
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Helper to format SSE event with optional event type
func FormatEvent(eventType string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if eventType != "" {
		return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)), nil
	}
	return []byte(fmt.Sprintf("data: %s\n\n", data)), nil
}
