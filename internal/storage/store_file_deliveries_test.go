package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestClaimFileDeliveriesFreezesTurnAndDeduplicates(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	origin := model.TelegramTurnOrigin{ThreadID: "thread-1", TurnID: "turn-1", ChatID: 42, TopicID: 9, DeliveryNonce: "nonce-1"}
	if err := store.PutTelegramTurnOrigin(ctx, origin); err != nil {
		t.Fatal(err)
	}
	requests := []model.FileDelivery{{DirectiveIndex: 0, FilePath: "reports/a.pdf", Caption: "A"}}

	claimed, err := store.ClaimFileDeliveries(ctx, "thread-1", "turn-1", "final-fp", requests)
	if err != nil || len(claimed) != 1 || claimed[0].Status != model.FileDeliverySending {
		t.Fatalf("claimed = %#v, err = %v", claimed, err)
	}
	claimed, err = store.ClaimFileDeliveries(ctx, "thread-1", "turn-1", "final-fp", requests)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("replay claimed = %#v, err = %v", claimed, err)
	}
	if _, err := store.ClaimFileDeliveries(ctx, "thread-1", "turn-1", "changed-final", append(requests, model.FileDelivery{DirectiveIndex: 1, FilePath: "b.pdf"})); err == nil {
		t.Fatal("ClaimFileDeliveries accepted a changed final for a frozen turn")
	}
}

func TestRecoverSendingFileDeliveriesMarksUnknown(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: "thread-1", TurnID: "turn-1", ChatID: 42, DeliveryNonce: "nonce-1"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimFileDeliveries(ctx, "thread-1", "turn-1", "final-fp", []model.FileDelivery{{DirectiveIndex: 0, FilePath: "a.pdf"}})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %#v, err = %v", claimed, err)
	}
	if err := store.RecoverSendingFileDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListFileDeliveries(ctx, "thread-1", "turn-1")
	if err != nil || len(got) != 1 || got[0].Status != model.FileDeliveryUnknown {
		t.Fatalf("deliveries = %#v, err = %v", got, err)
	}
}

func TestPutTelegramTurnOriginKeepsFirstDestinationAndNonce(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: "thread-1", TurnID: "turn-1", ChatID: 42, TopicID: 9, DeliveryNonce: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: "thread-1", TurnID: "turn-1", ChatID: 99, TopicID: 10, DeliveryNonce: "second"}); err != nil {
		t.Fatal(err)
	}
	origin, err := store.GetTelegramTurnOrigin(ctx, "thread-1", "turn-1")
	if err != nil || origin == nil || origin.ChatID != 42 || origin.TopicID != 9 || origin.DeliveryNonce != "first" {
		t.Fatalf("origin = %#v, err = %v", origin, err)
	}
}
