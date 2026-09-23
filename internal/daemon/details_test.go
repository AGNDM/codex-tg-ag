package daemon

import (
	"encoding/json"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestSnapshotForPanelTurnFallsBackToStoredThreadRaw(t *testing.T) {
	t.Parallel()

	thread := model.Thread{
		ID:  "thread-details-fallback",
		Raw: json.RawMessage(`{"thread":{"id":"thread-details-fallback","turns":[{"id":"turn-old","status":"completed","items":[{"id":"agent-old","type":"agentMessage","text":"old final","phase":"final_answer"}]}]}}`),
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:           model.Thread{ID: thread.ID},
		LatestTurnID:     "turn-new",
		LatestTurnStatus: "completed",
	}
	panel := &model.ThreadPanel{CurrentTurnID: "turn-old"}

	got, ok := snapshotForPanelTurn(thread, snapshot, panel)
	if !ok || got == nil {
		t.Fatal("snapshotForPanelTurn did not use stored thread raw fallback")
	}
	if got.LatestTurnID != "turn-old" || got.LatestFinalText != "old final" {
		t.Fatalf("fallback snapshot = %#v, want old turn final", got)
	}
}
