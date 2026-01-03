package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Writer struct {
	DB  *sql.DB
	Now func() time.Time
}

type EventPayload map[string]any

func (w Writer) Append(ctx context.Context, tx *sql.Tx, evtType, projectID, entityKind, entityID, actorID string, payload EventPayload) error {
	if w.Now == nil {
		w.Now = time.Now
	}
	ts := w.Now().UTC().Format(time.RFC3339)
	if payload == nil {
		payload = EventPayload{}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(ts,type,project_id,entity_kind,entity_id,actor_id,payload_json) VALUES (?,?,?,?,?,?,?)`,
		ts, evtType, nullable(projectID), entityKind, nullable(entityID), actorID, string(data))
	return err
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
