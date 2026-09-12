package wa

import (
	"context"
	"fmt"
	"sync"
	"time"

	zchatv1 "github.com/zealish/zchat/packages/ipc/zchatv1"
)

// MetaSyncCompleted marks in the store that the one-time full sync finished, so
// later daemon restarts do not gate the UI again.
const MetaSyncCompleted = "full_sync_completed"

// syncSettleDelay is how long the tracker waits for another history-sync chunk
// before declaring the full sync finished. WhatsApp streams the initial history
// in bursts with no terminal marker, so quiescence is the only completion
// signal available.
const syncSettleDelay = 12 * time.Second

// syncHardDeadline caps the whole full sync. A phone that never sends the
// remaining chunks must not strand the user on the progress screen forever.
const syncHardDeadline = 3 * time.Minute

// expectedChunks is the assumed number of history-sync chunks used to turn
// chunk arrivals into a progress fraction. WhatsApp does not announce how many
// it will send, so the bar approaches but never reaches the history ceiling
// until the sync actually settles.
const expectedChunks = 8

// Progress fractions reserved for each stage of the full sync.
const (
	progressContacts   = 0.10
	progressHistory    = 0.85
	progressFinalizing = 0.95
)

// syncTracker drives the full-sync progress reported to the desktop client. It
// owns no whatsmeow state; the session feeds it lifecycle callbacks and it
// decides what the UI should show.
type syncTracker struct {
	session *Session

	mu        sync.Mutex
	active    bool
	stage     zchatv1.SyncStage
	chunks    int
	chats     int
	messages  int
	contacts  int
	settle    *time.Timer
	deadline  *time.Timer
	completed bool
}

func newSyncTracker(s *Session) *syncTracker {
	return &syncTracker{session: s, stage: zchatv1.SyncStage_SYNC_STAGE_IDLE}
}

// Load restores whether a full sync already completed in an earlier run.
func (t *syncTracker) Load(ctx context.Context) {
	value, err := t.session.store.Meta(ctx, MetaSyncCompleted)
	if err != nil {
		t.session.log.Warn().Err(err).Msg("read sync flag")
		return
	}
	t.mu.Lock()
	t.completed = value == "1"
	if t.completed {
		t.stage = zchatv1.SyncStage_SYNC_STAGE_DONE
	}
	t.mu.Unlock()
}

// State returns the current sync state for a client.
func (t *syncTracker) State() *zchatv1.SyncState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stateLocked()
}

func (t *syncTracker) stateLocked() *zchatv1.SyncState {
	state := &zchatv1.SyncState{
		Stage:          t.stage,
		ChatsSynced:    int32(t.chats),
		MessagesSynced: int32(t.messages),
		ContactsSynced: int32(t.contacts),
	}
	switch t.stage {
	case zchatv1.SyncStage_SYNC_STAGE_CONNECTING:
		state.Indeterminate = true
		state.Detail = "Connecting to WhatsApp"
	case zchatv1.SyncStage_SYNC_STAGE_CONTACTS:
		state.Progress = progressContacts
		state.Detail = fmt.Sprintf("Syncing %d contacts", t.contacts)
	case zchatv1.SyncStage_SYNC_STAGE_HISTORY:
		ratio := float64(t.chunks) / float64(expectedChunks)
		if ratio > 1 {
			ratio = 1
		}
		state.Progress = progressContacts + (progressHistory-progressContacts)*ratio
		state.Detail = fmt.Sprintf("Syncing %d chats, %d messages", t.chats, t.messages)
	case zchatv1.SyncStage_SYNC_STAGE_FINALIZING:
		state.Progress = progressFinalizing
		state.Detail = "Tidying up your conversations"
	case zchatv1.SyncStage_SYNC_STAGE_DONE:
		state.Progress = 1
		state.Detail = "Sync complete"
	default:
		state.Detail = "Up to date"
	}
	return state
}

func (t *syncTracker) publishLocked() {
	t.session.pub.Publish(&zchatv1.Event{Payload: &zchatv1.Event_SyncState{SyncState: t.stateLocked()}})
}

// Begin arms the full sync. It is a no-op once a sync has completed, so
// reconnects on an already-synced device never block the UI again.
func (t *syncTracker) Begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.completed || t.active {
		return
	}
	t.active = true
	t.stage = zchatv1.SyncStage_SYNC_STAGE_CONNECTING
	t.deadline = time.AfterFunc(syncHardDeadline, func() { t.finish("sync deadline reached") })
	t.publishLocked()
}

// Connected moves an armed sync past the connection stage.
func (t *syncTracker) Connected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active || t.stage != zchatv1.SyncStage_SYNC_STAGE_CONNECTING {
		return
	}
	t.stage = zchatv1.SyncStage_SYNC_STAGE_CONTACTS
	t.publishLocked()
	t.armSettleLocked()
}

// Contacts records how many contacts the app-state sync produced.
func (t *syncTracker) Contacts(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return
	}
	t.contacts = n
	if t.stage == zchatv1.SyncStage_SYNC_STAGE_CONNECTING || t.stage == zchatv1.SyncStage_SYNC_STAGE_CONTACTS {
		t.stage = zchatv1.SyncStage_SYNC_STAGE_CONTACTS
		t.publishLocked()
	}
	t.armSettleLocked()
}

// Chunk records one processed history-sync batch.
func (t *syncTracker) Chunk(chats, messages int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return
	}
	t.chunks++
	t.chats += chats
	t.messages += messages
	t.stage = zchatv1.SyncStage_SYNC_STAGE_HISTORY
	t.publishLocked()
	t.armSettleLocked()
}

// armSettleLocked restarts the quiet-period timer that ends the sync.
func (t *syncTracker) armSettleLocked() {
	if t.settle != nil {
		t.settle.Stop()
	}
	t.settle = time.AfterFunc(syncSettleDelay, func() { t.finish("history sync settled") })
}

// finish closes the sync, running the final name backfill before the UI is
// released so the chat list never renders bare phone numbers.
func (t *syncTracker) finish(reason string) {
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return
	}
	t.active = false
	t.stage = zchatv1.SyncStage_SYNC_STAGE_FINALIZING
	if t.settle != nil {
		t.settle.Stop()
	}
	if t.deadline != nil {
		t.deadline.Stop()
	}
	t.publishLocked()
	t.mu.Unlock()

	ctx := context.Background()
	t.session.log.Info().Str("reason", reason).Msg("finishing full sync")
	t.session.reconcileChats(ctx)

	if err := t.session.store.SetMeta(ctx, MetaSyncCompleted, "1"); err != nil {
		t.session.log.Warn().Err(err).Msg("persist sync flag")
	}

	t.mu.Lock()
	t.completed = true
	t.stage = zchatv1.SyncStage_SYNC_STAGE_DONE
	t.publishLocked()
	t.mu.Unlock()
}

// Reset clears the completion flag so the next pairing syncs from scratch.
func (t *syncTracker) Reset(ctx context.Context) {
	t.mu.Lock()
	t.active = false
	t.completed = false
	t.stage = zchatv1.SyncStage_SYNC_STAGE_IDLE
	t.chunks, t.chats, t.messages, t.contacts = 0, 0, 0, 0
	if t.settle != nil {
		t.settle.Stop()
	}
	if t.deadline != nil {
		t.deadline.Stop()
	}
	// Clients gate their main view on this stage, so an aborted sync must be
	// announced or a paired-out client stays stuck on the progress screen.
	t.publishLocked()
	t.mu.Unlock()

	if err := t.session.store.SetMeta(ctx, MetaSyncCompleted, ""); err != nil {
		t.session.log.Warn().Err(err).Msg("clear sync flag")
	}
}

// SyncState exposes the current full-sync progress to the gRPC layer.
func (s *Session) SyncState() *zchatv1.SyncState { return s.fullSync.State() }
