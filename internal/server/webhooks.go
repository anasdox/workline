package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"workline/internal/config"
	"workline/internal/domain"
	"workline/internal/engine"
)

const (
	defaultWebhookInterval = 2 * time.Second
	defaultWebhookTimeout  = 5 * time.Second
	defaultWebhookBatch    = 100
)

type webhookDispatcher struct {
	engine   engine.Engine
	project  string
	webhooks []config.WebhookConfig
	client   *http.Client
	mu       sync.Mutex
	cursors  map[int]int64
}

func startWebhookDispatcher(e engine.Engine) {
	if e.Config == nil || len(e.Config.Webhooks) == 0 {
		return
	}
	projectID := e.Config.Project.ID
	if strings.TrimSpace(projectID) == "" {
		return
	}
	d := &webhookDispatcher{
		engine:   e,
		project:  projectID,
		webhooks: e.Config.Webhooks,
		client:   &http.Client{Timeout: defaultWebhookTimeout},
		cursors:  make(map[int]int64),
	}
	go d.run()
}

func (d *webhookDispatcher) run() {
	ticker := time.NewTicker(defaultWebhookInterval)
	defer ticker.Stop()
	for {
		d.dispatchAll()
		<-ticker.C
	}
}

func (d *webhookDispatcher) dispatchAll() {
	for i, hook := range d.webhooks {
		if hook.Enabled != nil && !*hook.Enabled {
			continue
		}
		if strings.TrimSpace(hook.URL) == "" {
			continue
		}
		d.dispatchWebhook(i, hook)
	}
}

func (d *webhookDispatcher) dispatchWebhook(idx int, hook config.WebhookConfig) {
	ctx := context.Background()
	cursor := d.cursorFor(idx, hook)
	events, err := d.engine.Repo.EventsAfter(ctx, defaultWebhookBatch, cursor, d.project)
	if err != nil {
		log.Printf("webhook: fetch events failed: %v", err)
		return
	}
	if len(events) == 0 {
		return
	}
	filter := newEventFilter(hook.Events)
	for _, evt := range events {
		if !filter.match(evt.Type) {
			d.setCursor(idx, evt.ID)
			continue
		}
		if err := d.postEvent(ctx, hook, evt); err != nil {
			log.Printf("webhook: deliver to %s failed: %v", hook.URL, err)
			return
		}
		d.setCursor(idx, evt.ID)
	}
}

func (d *webhookDispatcher) cursorFor(idx int, hook config.WebhookConfig) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.cursors[idx]; ok {
		return cur
	}
	ctx := context.Background()
	cur, err := d.engine.Repo.LatestEventID(ctx, d.project)
	if err != nil {
		log.Printf("webhook: init cursor failed: %v", err)
		cur = 0
	}
	d.cursors[idx] = cur
	return cur
}

func (d *webhookDispatcher) setCursor(idx int, value int64) {
	d.mu.Lock()
	d.cursors[idx] = value
	d.mu.Unlock()
}

type webhookEvent struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	ProjectID  string          `json:"project_id"`
	EntityKind string          `json:"entity_kind"`
	EntityID   string          `json:"entity_id,omitempty"`
	ActorID    string          `json:"actor_id"`
	TS         string          `json:"ts"`
	Payload    json.RawMessage `json:"payload"`
	PayloadRaw string          `json:"payload_raw,omitempty"`
}

func (d *webhookDispatcher) postEvent(ctx context.Context, hook config.WebhookConfig, evt domain.Event) error {
	payload := json.RawMessage([]byte("{}"))
	var raw string
	if evt.Payload != "" {
		if json.Valid([]byte(evt.Payload)) {
			payload = json.RawMessage([]byte(evt.Payload))
		} else {
			raw = evt.Payload
		}
	}
	body := webhookEvent{
		ID:         evt.ID,
		Type:       evt.Type,
		ProjectID:  evt.ProjectID,
		EntityKind: evt.EntityKind,
		EntityID:   evt.EntityID,
		ActorID:    evt.ActorID,
		TS:         evt.TS,
		Payload:    payload,
		PayloadRaw: raw,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	timeout := defaultWebhookTimeout
	if hook.TimeoutSeconds > 0 {
		timeout = time.Duration(hook.TimeoutSeconds) * time.Second
	}
	client := d.client
	if timeout != d.client.Timeout {
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workline-Event", evt.Type)
	req.Header.Set("X-Workline-Delivery", fmt.Sprintf("%d", evt.ID))
	req.Header.Set("X-Workline-Project", d.project)
	if strings.TrimSpace(hook.Secret) != "" {
		req.Header.Set("X-Workline-Secret", hook.Secret)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("status %d: %s", res.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return nil
}

type eventFilter struct {
	all bool
	set map[string]struct{}
}

func newEventFilter(events []string) eventFilter {
	if len(events) == 0 {
		return eventFilter{all: true}
	}
	set := make(map[string]struct{}, len(events))
	for _, evt := range events {
		key := strings.TrimSpace(evt)
		if key == "" {
			continue
		}
		set[key] = struct{}{}
	}
	if len(set) == 0 {
		return eventFilter{all: true}
	}
	return eventFilter{set: set}
}

func (f eventFilter) match(evt string) bool {
	if f.all {
		return true
	}
	_, ok := f.set[evt]
	return ok
}
