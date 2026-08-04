package inboxv2

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Writer is the single place a notification becomes rows.
//
// Producers used to call queries.CreateInboxItem directly. They go through here
// so the legacy inbox_item row and the v2 group/event are written in one
// transaction — the dual-write contract from v2.1 §9 — without each of the
// seven call sites having to open and commit its own.
type Writer struct {
	tx       TxStarter
	queries  *db.Queries
	recorder *Recorder
}

func NewWriter(tx TxStarter, queries *db.Queries) *Writer {
	return &Writer{tx: tx, queries: queries, recorder: NewRecorder(NewStore(queries, tx))}
}

// NewWriterWithSwitch is for tests that drive the switch directly instead of
// through the process environment.
func NewWriterWithSwitch(tx TxStarter, queries *db.Queries, enabled func() bool) *Writer {
	return &Writer{tx: tx, queries: queries, recorder: NewRecorderWithSwitch(NewStore(queries, tx), enabled)}
}

// CreateInboxItem writes one notification.
//
// With the dual-write switch off it is exactly the old call: no transaction, no
// v2 table touched. Shipping the producer wiring is therefore not the same
// event as changing behaviour — the switch is.
//
// Agent recipients never get a v2 group. Agents receive work through the run
// queue; the inbox_item rows written for them today are dead rows nothing
// reads, and reproducing them in v2 would create groups no inbox will render.
// This is the "member only" guard the design calls for at the delivery seam.
func (w *Writer) CreateInboxItem(ctx context.Context, p db.CreateInboxItemParams, d Delivery) (db.InboxItem, error) {
	if !w.recorder.Enabled() || p.RecipientType != "member" {
		return w.queries.CreateInboxItem(ctx, p)
	}

	tx, err := w.tx.Begin(ctx)
	if err != nil {
		return db.InboxItem{}, err
	}
	defer tx.Rollback(ctx)

	item, err := w.queries.WithTx(tx).CreateInboxItem(ctx, p)
	if err != nil {
		return db.InboxItem{}, err
	}
	if err := w.recorder.RecordInTx(ctx, tx,
		util.UUIDToString(p.WorkspaceID), util.UUIDToString(p.RecipientID),
		d, time.Now().UTC()); err != nil {
		return db.InboxItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.InboxItem{}, err
	}
	return item, nil
}
