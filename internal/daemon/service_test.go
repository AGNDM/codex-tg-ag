package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/leadpolicy"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func operatorPrompt(message string) string {
	const marker = "\n\n[Codex Telegram file delivery]\n"
	if index := strings.Index(message, marker); index >= 0 {
		message = message[:index]
	}
	return strings.TrimSpace(message)
}

func TestHandleDocumentSavesSafeProjectRelativePath(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	thread := model.Thread{ID: "document-thread", CWD: project, Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, thread.ID, model.BindingModeBound); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	_, err := service.HandleDocument(ctx, 123456789, 0, 123456789, `..\..\report.txt`, []byte("private body"), "summarize", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one", stub.turnStartCalls)
	}
	prompt := stub.turnStartCalls[0].message
	if !strings.Contains(prompt, "summarize\n\nAttached document: \".codex-tg/uploads/") || strings.Contains(prompt, "..") {
		t.Fatalf("prompt = %q", prompt)
	}
	matches, err := filepath.Glob(filepath.Join(project, ".codex-tg", "uploads", "*-report.txt"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("uploaded files = %#v, err = %v", matches, err)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil || string(body) != "private body" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
}

func TestHandleDocumentRejectsInvalidCWDBeforeAppServer(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{ID: "document-thread", CWD: "relative/project", Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, thread.ID, model.BindingModeBound); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	if _, err := service.HandleDocument(ctx, 123456789, 0, 123456789, "report.txt", []byte("body"), "inspect", 0); err == nil {
		t.Fatal("HandleDocument accepted a relative Project cwd")
	}
	if len(stub.turnStartCalls) != 0 || len(stub.turnSteerCalls) != 0 {
		t.Fatalf("unexpected App Server calls: start=%#v steer=%#v", stub.turnStartCalls, stub.turnSteerCalls)
	}
}

func TestHandleDocumentRejectsUploadSymlinkOutsideProject(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, ".codex-tg")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	thread := model.Thread{ID: "document-thread", CWD: project, Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, thread.ID, model.BindingModeBound); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	if _, err := service.HandleDocument(ctx, 123456789, 0, 123456789, "report.txt", []byte("body"), "inspect", 0); err == nil {
		t.Fatal("HandleDocument followed an upload symlink outside the Project")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside entries = %#v, err = %v", entries, err)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want none", stub.turnStartCalls)
	}
}

func TestResolveRoutePrecedenceExplicitThenReplyThenBinding(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	if err := service.store.SetBinding(ctx, 123456789, 0, "binding-thread", model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}
	if err := service.store.PutMessageRoute(ctx, model.MessageRoute{
		ChatID:    123456789,
		TopicID:   0,
		MessageID: 99,
		ThreadID:  "reply-thread",
		TurnID:    "reply-turn",
		CreatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("PutMessageRoute failed: %v", err)
	}

	explicit, err := service.resolveRoute(ctx, 123456789, 0, "explicit-thread", 99)
	if err != nil {
		t.Fatalf("resolveRoute(explicit) failed: %v", err)
	}
	if explicit.ThreadID != "explicit-thread" || explicit.Source != model.RouteSourceExplicit {
		t.Fatalf("explicit route = %#v, want explicit-thread / explicit", explicit)
	}

	reply, err := service.resolveRoute(ctx, 123456789, 0, "", 99)
	if err != nil {
		t.Fatalf("resolveRoute(reply) failed: %v", err)
	}
	if reply.ThreadID != "reply-thread" || reply.Source != model.RouteSourceReply {
		t.Fatalf("reply route = %#v, want reply-thread / reply", reply)
	}
	if reply.TurnID != "reply-turn" || reply.RequestID != "" {
		t.Fatalf("reply route turn/request = %#v, want reply-turn without request", reply)
	}

	binding, err := service.resolveRoute(ctx, 123456789, 0, "", 0)
	if err != nil {
		t.Fatalf("resolveRoute(binding) failed: %v", err)
	}
	if binding.ThreadID != "binding-thread" || binding.Source != model.RouteSourceBinding {
		t.Fatalf("binding route = %#v, want binding-thread / binding", binding)
	}
}

func TestResolveRouteExtractsPlanRequestIDOnlyFromPlanRequestEvent(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	if err := service.store.PutMessageRoute(ctx, model.MessageRoute{
		ChatID:    123456789,
		TopicID:   0,
		MessageID: 100,
		ThreadID:  "plan-thread",
		TurnID:    "plan-turn",
		EventID:   "plan_request:request-plan-1",
		CreatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("PutMessageRoute(plan request) failed: %v", err)
	}
	if err := service.store.PutMessageRoute(ctx, model.MessageRoute{
		ChatID:    123456789,
		TopicID:   0,
		MessageID: 101,
		ThreadID:  "synthetic-thread",
		TurnID:    "synthetic-turn",
		EventID:   "synthetic-plan-fp",
		CreatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("PutMessageRoute(synthetic) failed: %v", err)
	}

	real, err := service.resolveRoute(ctx, 123456789, 0, "", 100)
	if err != nil {
		t.Fatalf("resolveRoute(real plan request) failed: %v", err)
	}
	if real.ThreadID != "plan-thread" || real.TurnID != "plan-turn" || real.RequestID != "request-plan-1" {
		t.Fatalf("real plan route = %#v, want thread/turn/request", real)
	}

	synthetic, err := service.resolveRoute(ctx, 123456789, 0, "", 101)
	if err != nil {
		t.Fatalf("resolveRoute(synthetic plan) failed: %v", err)
	}
	if synthetic.ThreadID != "synthetic-thread" || synthetic.TurnID != "synthetic-turn" || synthetic.RequestID != "" {
		t.Fatalf("synthetic plan route = %#v, want thread/turn without request", synthetic)
	}
}

func TestCurrentBackgroundTargetDefaultsMovesAndDisables(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	target, err := service.currentBackgroundTarget(ctx)
	if err != nil {
		t.Fatalf("currentBackgroundTarget(default) failed: %v", err)
	}
	if target == nil || target.ChatID != 123456789 || target.TopicID != 0 || !target.Enabled {
		t.Fatalf("default background target = %#v, want enabled DM target for allowed user", target)
	}

	if err := service.store.SetGlobalObserverTarget(ctx, -1001234567890, 7, true); err != nil {
		t.Fatalf("SetGlobalObserverTarget(enable moved target) failed: %v", err)
	}
	target, err = service.currentBackgroundTarget(ctx)
	if err != nil {
		t.Fatalf("currentBackgroundTarget(moved) failed: %v", err)
	}
	if target == nil || target.ChatID != -1001234567890 || target.TopicID != 7 || !target.Enabled {
		t.Fatalf("moved global target = %#v, want -1001234567890:7 enabled", target)
	}

	if err := service.store.SetGlobalObserverTarget(ctx, -1001234567890, 7, false); err != nil {
		t.Fatalf("SetGlobalObserverTarget(disable) failed: %v", err)
	}
	target, err = service.currentBackgroundTarget(ctx)
	if err != nil {
		t.Fatalf("currentBackgroundTarget(disabled) failed: %v", err)
	}
	if target != nil {
		t.Fatalf("disabled global target = %#v, want nil", target)
	}
}

func TestObserveCommandsMoveAndDisableGlobalTarget(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	response, err := service.handleCommand(ctx, 42, 9, "/observe all", 0)
	if err != nil {
		t.Fatalf("handleCommand(/observe all) failed: %v", err)
	}
	if response == nil {
		t.Fatal("handleCommand(/observe all) returned nil response")
	}

	target, configured, err := service.store.GetGlobalObserverTarget(ctx)
	if err != nil {
		t.Fatalf("GetGlobalObserverTarget(after /observe all) failed: %v", err)
	}
	if !configured || target == nil {
		t.Fatalf("global target after /observe all = %#v configured=%t, want configured target", target, configured)
	}
	if target.ChatID != 42 || target.TopicID != 9 {
		t.Fatalf("global target after /observe all = %#v, want 42:9", target)
	}
	sinceUnix, ok, err := service.store.GetGlobalObserverSinceUnix(ctx)
	if err != nil {
		t.Fatalf("GetGlobalObserverSinceUnix(after /observe all) failed: %v", err)
	}
	if !ok || sinceUnix <= 0 {
		t.Fatalf("GetGlobalObserverSinceUnix(after /observe all) = %d ok=%t, want positive value", sinceUnix, ok)
	}

	response, err = service.handleCommand(ctx, 42, 9, "/observe off", 0)
	if err != nil {
		t.Fatalf("handleCommand(/observe off) failed: %v", err)
	}
	if response == nil {
		t.Fatal("handleCommand(/observe off) returned nil response")
	}

	target, configured, err = service.store.GetGlobalObserverTarget(ctx)
	if err != nil {
		t.Fatalf("GetGlobalObserverTarget(after /observe off) failed: %v", err)
	}
	if !configured {
		t.Fatal("GetGlobalObserverTarget(after /observe off) should remain configured")
	}
	if target != nil {
		t.Fatalf("global target after /observe off = %#v, want nil", target)
	}
}

func TestBindingAndGlobalObserverCanCoexistAtServiceLevel(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	if err := service.store.SetGlobalObserverTarget(ctx, 123456789, 0, true); err != nil {
		t.Fatalf("SetGlobalObserverTarget failed: %v", err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, "thread-1", model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}

	target, configured, err := service.store.GetGlobalObserverTarget(ctx)
	if err != nil {
		t.Fatalf("GetGlobalObserverTarget failed: %v", err)
	}
	if !configured || target == nil || target.ChatID != 123456789 {
		t.Fatalf("global target = %#v configured=%t, want enabled target for bound chat", target, configured)
	}

	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "thread-1" {
		t.Fatalf("binding = %#v, want thread-1", binding)
	}
}

func TestResolveArmedSteerReturnsActiveStateAndExpires(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	if err := service.armSteer(ctx, 123456789, 0, "steer-thread", "turn-9", 77); err != nil {
		t.Fatalf("armSteer failed: %v", err)
	}
	state, err := service.resolveArmedSteer(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("resolveArmedSteer(active) failed: %v", err)
	}
	if state == nil || state.ThreadID != "steer-thread" || state.TurnID != "turn-9" || state.PanelID != 77 {
		t.Fatalf("active steer state = %#v, want steer-thread/turn-9/panel 77", state)
	}

	if err := service.store.ArmSteerState(ctx, model.SteerState{
		ChatKey:   model.ChatKey(123456789, 0),
		ChatID:    123456789,
		TopicID:   0,
		ThreadID:  "expired-thread",
		TurnID:    "turn-old",
		PanelID:   88,
		ExpiresAt: model.TimeString(time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)),
		CreatedAt: model.NowString(),
		UpdatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("ArmSteerState(expired) failed: %v", err)
	}

	state, err = service.resolveArmedSteer(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("resolveArmedSteer(expired) failed: %v", err)
	}
	if state != nil {
		t.Fatalf("expired steer state = %#v, want nil", state)
	}
	loaded, err := service.store.GetSteerState(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetSteerState(after expired resolve) failed: %v", err)
	}
	if loaded != nil {
		t.Fatalf("stored steer state after expired resolve = %#v, want nil", loaded)
	}
}

func TestArmSteerCallbackUsesEnglishStatus(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	route := model.CallbackRoute{
		Token: "steer-english", Action: "arm_steer", ThreadID: "steer-thread", TurnID: "turn-9",
		Status: model.CallbackStatusActive, CreatedAt: model.NowString(),
	}
	if err := service.store.PutCallbackRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.CreateThreadPanel(ctx, model.ThreadPanel{
		ChatID: 123456789, TopicID: 0, ThreadID: route.ThreadID, SummaryMessageID: 42,
		CurrentTurnID: route.TurnID, Status: "inProgress",
	}); err != nil {
		t.Fatal(err)
	}

	response, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, route.Token)
	if err != nil {
		t.Fatalf("HandleCallback(arm_steer) failed: %v", err)
	}
	if response == nil || response.CallbackText != "Your next message will steer this turn." {
		t.Fatalf("response = %#v", response)
	}
}

func TestStaleSteerAndStopButtonsDoNotTargetCurrentTurn(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{ID: "button-thread", CWD: "/Users/example/project", Status: "active", ActiveTurnID: "turn-current"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.CreateThreadPanel(ctx, model.ThreadPanel{
		ChatID: 123456789, TopicID: 0, ThreadID: thread.ID, SummaryMessageID: 42,
		CurrentTurnID: thread.ActiveTurnID, Status: "inProgress",
	}); err != nil {
		t.Fatal(err)
	}
	route := model.CallbackRoute{
		Token: "stale-steer", Action: "arm_steer", ThreadID: thread.ID, TurnID: "turn-old",
		Status: model.CallbackStatusActive, CreatedAt: model.NowString(),
	}
	if err := service.store.PutCallbackRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleCallback(ctx, 123456789, 0, 41, 123456789, route.Token)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "stale") {
		t.Fatalf("stale Steer response = %#v, err = %v", response, err)
	}
	state, err := service.store.GetSteerState(ctx, 123456789, 0)
	if err != nil || state != nil {
		t.Fatalf("steer state = %#v, err = %v; want none", state, err)
	}

	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true
	response, err = service.interruptTurn(ctx, 123456789, 0, thread.ID, "turn-old")
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "stale") {
		t.Fatalf("stale Stop response = %#v, err = %v", response, err)
	}
	if len(stub.turnInterruptCalls) != 0 {
		t.Fatalf("turnInterruptCalls = %#v, want none", stub.turnInterruptCalls)
	}
}

func TestTrackedThreadsSkipsIdleRecentHistoryWithoutBindingsOrPanels(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Unix()

	idle := model.Thread{
		ID:          "idle-thread",
		Title:       "Idle history",
		ProjectName: "Codex",
		UpdatedAt:   now - 600,
		Status:      "idle",
	}
	active := model.Thread{
		ID:           "active-thread",
		Title:        "Active now",
		ProjectName:  "Codex",
		UpdatedAt:    now,
		Status:       "idle",
		ActiveTurnID: "turn-1",
	}
	if err := service.store.UpsertThread(ctx, idle); err != nil {
		t.Fatalf("UpsertThread(idle) failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, active); err != nil {
		t.Fatalf("UpsertThread(active) failed: %v", err)
	}

	tracked := service.trackedThreads(ctx, 10)
	ids := map[string]bool{}
	for _, thread := range tracked {
		ids[thread.ID] = true
	}

	if ids[idle.ID] {
		t.Fatalf("tracked threads unexpectedly include stale idle history: %#v", tracked)
	}
	if !ids[active.ID] {
		t.Fatalf("tracked threads do not include active thread: %#v", tracked)
	}
}

func TestThreadsCommandHidesInternalSubAgentThreads(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	visible := model.Thread{
		ID:            "visible-thread",
		Title:         "Visible work",
		ProjectName:   "codex-tg",
		DirectoryName: "codex-tg",
		UpdatedAt:     10,
		LastPreview:   "normal user request",
		Raw:           json.RawMessage(`{"id":"visible-thread","preview":"normal user request"}`),
	}
	internal := model.Thread{
		ID:            "01900000-0000-7000-8000-000000000014",
		Title:         "01900000-0000-7000-8000-000000000014",
		ProjectName:   "memories",
		DirectoryName: "memories",
		UpdatedAt:     20,
		Raw:           json.RawMessage(`{"thread":{"id":"01900000-0000-7000-8000-000000000014","ephemeral":true,"source":{"subAgent":"memory_consolidation"}}}`),
	}
	if err := service.store.UpsertThread(ctx, visible); err != nil {
		t.Fatalf("UpsertThread(visible) failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, internal); err != nil {
		t.Fatalf("UpsertThread(internal) failed: %v", err)
	}

	response, err := service.handleCommand(ctx, 123456789, 0, "/threads 8", 0)
	if err != nil {
		t.Fatalf("handleCommand(/threads) failed: %v", err)
	}
	if response == nil {
		t.Fatal("handleCommand(/threads) returned nil response")
	}
	if !strings.Contains(response.Text, "Visible work") {
		t.Fatalf("/threads text missing visible thread:\n%s", response.Text)
	}
	if strings.Contains(response.Text, "memories") || strings.Contains(response.Text, internal.ID) {
		t.Fatalf("/threads text contains internal thread:\n%s", response.Text)
	}
	if len(response.Buttons) != 1 || len(response.Buttons[0]) != 1 || response.Buttons[0][0].Text != "Open 1" {
		t.Fatalf("/threads buttons = %#v, want one Open 1 button", response.Buttons)
	}
}

func TestProjectsCommandShowsProjectButtonsGroupedByCWD(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	threads := []model.Thread{
		{
			ID:            "workspace-a-1",
			Title:         "A one",
			CWD:           "/Users/example/work/a/codex-tg",
			ProjectName:   "codex-tg",
			DirectoryName: "codex-tg",
			UpdatedAt:     30,
			Raw:           json.RawMessage(`{"id":"workspace-a-1"}`),
		},
		{
			ID:            "workspace-a-2",
			Title:         "A two",
			CWD:           "/Users/example/work/a/codex-tg",
			ProjectName:   "codex-tg",
			DirectoryName: "codex-tg",
			UpdatedAt:     40,
			Raw:           json.RawMessage(`{"id":"workspace-a-2"}`),
		},
		{
			ID:            "workspace-b-1",
			Title:         "B one",
			CWD:           "/Users/example/work/b/codex-tg",
			ProjectName:   "codex-tg",
			DirectoryName: "codex-tg",
			UpdatedAt:     20,
			Raw:           json.RawMessage(`{"id":"workspace-b-1"}`),
		},
	}
	for _, thread := range threads {
		if err := service.store.UpsertThread(ctx, thread); err != nil {
			t.Fatalf("UpsertThread(%s) failed: %v", thread.ID, err)
		}
	}

	response, err := service.handleCommand(ctx, 123456789, 0, "/projects", 0)
	if err != nil {
		t.Fatalf("handleCommand(/projects) failed: %v", err)
	}
	if response == nil {
		t.Fatal("handleCommand(/projects) returned nil response")
	}
	if !strings.Contains(response.Text, "/Users/example/work/a/codex-tg") || !strings.Contains(response.Text, "/Users/example/work/b/codex-tg") {
		t.Fatalf("/projects text missing grouped cwd entries:\n%s", response.Text)
	}
	if strings.Contains(response.Text, "key:") {
		t.Fatalf("/projects text renders internal project key:\n%s", response.Text)
	}
	if !strings.Contains(response.Text, "last thread: A two") || !strings.Contains(response.Text, "last thread: B one") {
		t.Fatalf("/projects text missing latest thread labels:\n%s", response.Text)
	}
	if got := countButtonsContaining(response.Buttons, "codex-tg"); got != 2 {
		t.Fatalf("/projects buttons = %#v, want two named project workspace buttons", response.Buttons)
	}
}

func TestIsCodexChatsCWDMatchesGenericMacAndWindowsPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cwd  string
		want bool
	}{
		{cwd: "/Users/alice/Documents/Codex", want: true},
		{cwd: "/Users/alice/Documents/Codex/2026-04-29/new-chat", want: true},
		{cwd: `C:\Users\you\Documents\Codex`, want: true},
		{cwd: `C:\Users\you\Documents\Codex\2026-04-28\tool-call`, want: true},
		{cwd: "/Users/alice/Library/CloudStorage/OneDrive-Personal/Programming/AI/Codex", want: false},
		{cwd: "/Users/alice/Documents/Codexology", want: false},
		{cwd: `D:\Users\bob\Documents\Codex`, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.cwd, func(t *testing.T) {
			if got := isCodexChatsCWD(tc.cwd); got != tc.want {
				t.Fatalf("isCodexChatsCWD(%q) = %v, want %v", tc.cwd, got, tc.want)
			}
		})
	}
}

func TestProjectsCommandShowsChatsSectionAndSortsByRecency(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	threads := []model.Thread{
		{
			ID:            "old-project-thread",
			Title:         "Old project",
			CWD:           "/Users/example/work/old-project",
			ProjectName:   "old-project",
			DirectoryName: "old-project",
			UpdatedAt:     10,
			Raw:           json.RawMessage(`{"id":"old-project-thread"}`),
		},
		{
			ID:            "new-project-thread",
			Title:         "New project",
			CWD:           "/Users/example/work/new-project",
			ProjectName:   "new-project",
			DirectoryName: "new-project",
			UpdatedAt:     50,
			Raw:           json.RawMessage(`{"id":"new-project-thread"}`),
		},
		{
			ID:            "older-chat-thread",
			Title:         "Older Chat",
			CWD:           "/Users/example/Documents/Codex/2026-04-28/tool-call",
			ProjectName:   "tool-call",
			DirectoryName: "tool-call",
			UpdatedAt:     20,
			Raw:           json.RawMessage(`{"id":"older-chat-thread"}`),
		},
		{
			ID:            "newer-chat-thread",
			Title:         "Newer Chat",
			CWD:           "/Users/example/Documents/Codex/2026-04-29/new-chat",
			ProjectName:   "new-chat",
			DirectoryName: "new-chat",
			UpdatedAt:     60,
			Raw:           json.RawMessage(`{"id":"newer-chat-thread"}`),
		},
		{
			ID:            "windows-chat-thread",
			Title:         "Windows Chat",
			CWD:           `C:\Users\you\Documents\Codex\2026-04-30\win-chat`,
			ProjectName:   "win-chat",
			DirectoryName: "win-chat",
			UpdatedAt:     30,
			Raw:           json.RawMessage(`{"id":"windows-chat-thread"}`),
		},
	}
	for _, thread := range threads {
		if err := service.store.UpsertThread(ctx, thread); err != nil {
			t.Fatalf("UpsertThread(%s) failed: %v", thread.ID, err)
		}
	}

	response, err := service.handleCommand(ctx, 123456789, 0, "/projects", 0)
	if err != nil {
		t.Fatalf("handleCommand(/projects) failed: %v", err)
	}
	if response == nil {
		t.Fatal("handleCommand(/projects) returned nil response")
	}
	for _, needle := range []string{"Projects", "Project page 1/1", "Latest Chats: showing 3 of 3", "Open Chats", "Newer Chat", "Windows Chat", "Older Chat"} {
		if !strings.Contains(response.Text, needle) {
			t.Fatalf("/projects text missing %q:\n%s", needle, response.Text)
		}
	}
	requireTextOrder(t, response.Text, "new-project", "old-project")
	requireTextOrder(t, response.Text, "Newer Chat", "Windows Chat")
	requireTextOrder(t, response.Text, "Windows Chat", "Older Chat")
	if strings.Contains(response.Text, "cwd: /Users/example/Documents/Codex") || strings.Contains(response.Text, `cwd: C:\Users\you\Documents\Codex`) {
		t.Fatalf("/projects text renders chat cwd as project cwd:\n%s", response.Text)
	}
	if strings.Contains(response.Text, "key:") {
		t.Fatalf("/projects text renders internal project key:\n%s", response.Text)
	}
	if !strings.Contains(response.Text, "last thread: New project") || !strings.Contains(response.Text, "last thread: Old project") {
		t.Fatalf("/projects text missing latest project thread labels:\n%s", response.Text)
	}
	for _, label := range []string{"1. new-project", "2. old-project", "Chat 1. Newer Chat", "Chat 2. Windows Chat", "Chat 3. Older Chat"} {
		if callbackTokenForButton(response.Buttons, label) == "" {
			t.Fatalf("/projects buttons = %#v, want named button %q", response.Buttons, label)
		}
	}
	if callbackTokenForButton(response.Buttons, "Open Chats") == "" {
		t.Fatalf("/projects buttons = %#v, want Open Chats", response.Buttons)
	}
}

func TestProjectsPaginationUsesPreviewLimitsAndKeepsLatestChats(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	service.cfg.ProjectsProjectPreviewLimit = 2
	service.cfg.ProjectsChatPreviewLimit = 1
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		thread := model.Thread{
			ID:            fmt.Sprintf("project-%d-thread", i),
			Title:         fmt.Sprintf("Project %d thread", i),
			CWD:           fmt.Sprintf("/Users/example/work/project-%d", i),
			ProjectName:   fmt.Sprintf("project-%d", i),
			DirectoryName: fmt.Sprintf("project-%d", i),
			UpdatedAt:     int64(i * 10),
			Raw:           json.RawMessage(fmt.Sprintf(`{"id":"project-%d-thread"}`, i)),
		}
		if err := service.store.UpsertThread(ctx, thread); err != nil {
			t.Fatalf("UpsertThread(%s) failed: %v", thread.ID, err)
		}
	}
	for i := 1; i <= 2; i++ {
		thread := model.Thread{
			ID:            fmt.Sprintf("chat-%d-thread", i),
			Title:         fmt.Sprintf("Chat %d", i),
			CWD:           fmt.Sprintf("/Users/example/Documents/Codex/2026-04-2%d/chat-%d", i, i),
			ProjectName:   fmt.Sprintf("chat-%d", i),
			DirectoryName: fmt.Sprintf("chat-%d", i),
			UpdatedAt:     int64(100 + i),
			Raw:           json.RawMessage(fmt.Sprintf(`{"id":"chat-%d-thread"}`, i)),
		}
		if err := service.store.UpsertThread(ctx, thread); err != nil {
			t.Fatalf("UpsertThread(%s) failed: %v", thread.ID, err)
		}
	}

	page1, err := service.handleCommand(ctx, 123456789, 0, "/projects", 0)
	if err != nil {
		t.Fatalf("handleCommand(/projects) failed: %v", err)
	}
	if !strings.Contains(page1.Text, "Project page 1/3") || !strings.Contains(page1.Text, "project-5") || !strings.Contains(page1.Text, "project-4") || strings.Contains(page1.Text, "project-3") {
		t.Fatalf("page1 text =\n%s\nwant first two recent projects only", page1.Text)
	}
	if !strings.Contains(page1.Text, "Latest Chats: showing 1 of 2") || !strings.Contains(page1.Text, "Chat 2") || strings.Contains(page1.Text, "Chat 1") {
		t.Fatalf("page1 text =\n%s\nwant latest one chat preview", page1.Text)
	}
	for _, label := range []string{"1. project-5", "2. project-4", "Chat 1. Chat 2"} {
		if callbackTokenForButton(page1.Buttons, label) == "" {
			t.Fatalf("page1 buttons = %#v, want named button %q", page1.Buttons, label)
		}
	}
	nextToken := callbackTokenForButton(page1.Buttons, ">")
	if nextToken == "" {
		t.Fatalf("page1 buttons = %#v, want next button", page1.Buttons)
	}
	page2, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, nextToken)
	if err != nil {
		t.Fatalf("HandleCallback(next projects page) failed: %v", err)
	}
	if !strings.Contains(page2.Text, "Project page 2/3") || !strings.Contains(page2.Text, "project-3") || !strings.Contains(page2.Text, "project-2") || strings.Contains(page2.Text, "project-5") {
		t.Fatalf("page2 text =\n%s\nwant second page projects only", page2.Text)
	}
	if !strings.Contains(page2.Text, "Chat 2") || strings.Contains(page2.Text, "Chat 1") {
		t.Fatalf("page2 text =\n%s\nwant same latest chat preview", page2.Text)
	}
	for _, label := range []string{"3. project-3", "4. project-2", "Chat 1. Chat 2"} {
		if callbackTokenForButton(page2.Buttons, label) == "" {
			t.Fatalf("page2 buttons = %#v, want named button %q", page2.Buttons, label)
		}
	}
}

func TestOpenChatsPaginatesAndChatSelectionBindsThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	service.cfg.ChatsPageSize = 3
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		thread := model.Thread{
			ID:            fmt.Sprintf("chat-%d-thread", i),
			Title:         fmt.Sprintf("Chat %d", i),
			CWD:           fmt.Sprintf("/Users/example/Documents/Codex/2026-04-2%d/chat-%d", i, i),
			ProjectName:   fmt.Sprintf("chat-%d", i),
			DirectoryName: fmt.Sprintf("chat-%d", i),
			UpdatedAt:     int64(i * 10),
			Raw:           json.RawMessage(fmt.Sprintf(`{"id":"chat-%d-thread"}`, i)),
		}
		if err := service.store.UpsertThread(ctx, thread); err != nil {
			t.Fatalf("UpsertThread(%s) failed: %v", thread.ID, err)
		}
	}

	projects, err := service.handleCommand(ctx, 123456789, 0, "/projects", 0)
	if err != nil {
		t.Fatalf("handleCommand(/projects) failed: %v", err)
	}
	openChats := callbackTokenForButton(projects.Buttons, "Open Chats")
	if openChats == "" {
		t.Fatalf("/projects buttons = %#v, want Open Chats", projects.Buttons)
	}
	chats, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, openChats)
	if err != nil {
		t.Fatalf("HandleCallback(Open Chats) failed: %v", err)
	}
	if !strings.Contains(chats.Text, "Chats") || !strings.Contains(chats.Text, "Page 1/2") || !strings.Contains(chats.Text, "Chat 5") || strings.Contains(chats.Text, "Chat 2") {
		t.Fatalf("chats text =\n%s\nwant first page of recent chats", chats.Text)
	}
	if strings.Contains(chats.Text, "New thread") || callbackTokenForButton(chats.Buttons, "New thread") != "" {
		t.Fatalf("chats response = %#v, want no New thread action", chats)
	}
	chatToken := callbackTokenForButton(chats.Buttons, "Chat 1. Chat 5")
	if chatToken == "" {
		t.Fatalf("chats buttons = %#v, want first chat button", chats.Buttons)
	}
	opened, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, chatToken)
	if err != nil {
		t.Fatalf("HandleCallback(Chat 1. Chat 5) failed: %v", err)
	}
	if opened == nil || opened.ThreadID != "chat-5-thread" {
		t.Fatalf("opened = %#v, want newest chat thread", opened)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "chat-5-thread" {
		t.Fatalf("binding = %#v, want selected chat binding", binding)
	}
}

func TestProjectsCloseDeletesMenuMessage(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()

	response, err := service.closeProjectsMenu(ctx, 123456789, 0, 42)
	if err != nil {
		t.Fatalf("closeProjectsMenu failed: %v", err)
	}
	if response == nil || response.CallbackText != "Closed." || response.Text != "" {
		t.Fatalf("response = %#v, want callback-only closed response", response)
	}
	if len(sender.deletes) != 1 || sender.deletes[0].messageID != 42 {
		t.Fatalf("deletes = %#v, want deleted menu message 42", sender.deletes)
	}
}

func TestProjectOpenShowsNewThreadMenu(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:            "project-menu-thread",
		Title:         "Menu thread",
		CWD:           "/Users/example/project",
		ProjectName:   "project",
		DirectoryName: "project",
		UpdatedAt:     10,
		Raw:           json.RawMessage(`{"id":"project-menu-thread"}`),
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}

	projects, err := service.handleCommand(ctx, 123456789, 0, "/projects", 0)
	if err != nil {
		t.Fatalf("handleCommand(/projects) failed: %v", err)
	}
	token := callbackTokenForButton(projects.Buttons, "1. project")
	if token == "" {
		t.Fatalf("/projects buttons = %#v, want project button", projects.Buttons)
	}

	menu, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, token)
	if err != nil {
		t.Fatalf("HandleCallback(project_open) failed: %v", err)
	}
	if menu == nil || !strings.Contains(menu.Text, "Project") || !strings.Contains(menu.Text, "/Users/example/project") {
		t.Fatalf("project menu = %#v, want project cwd", menu)
	}
	for _, label := range []string{"New thread", "Threads", "Bind latest"} {
		if callbackTokenForButton(menu.Buttons, label) == "" {
			t.Fatalf("project menu buttons = %#v, want %q", menu.Buttons, label)
		}
	}
}

func TestProjectNewThreadArmsThenPlainTextCreatesThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()
	project := model.Thread{
		ID:            "project-source-thread",
		Title:         "Source",
		CWD:           "/Users/example/project",
		ProjectName:   "project",
		DirectoryName: "project",
		UpdatedAt:     10,
		Raw:           json.RawMessage(`{"id":"project-source-thread"}`),
	}
	if err := service.store.UpsertThread(ctx, project); err != nil {
		t.Fatalf("UpsertThread(project) failed: %v", err)
	}
	stub := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "new-thread-id", "cwd": "/Users/example/project", "title": "New thread"}},
		threadReads: map[string]map[string]any{
			"new-thread-id": {
				"thread": map[string]any{
					"id":     "new-thread-id",
					"title":  "New thread",
					"cwd":    "/Users/example/project",
					"status": "active",
					"turns": []any{map[string]any{
						"id":     "started-turn",
						"status": "inProgress",
						"items":  []any{map[string]any{"id": "user-item", "type": "userMessage", "content": []any{map[string]any{"text": "first prompt"}}}},
					}},
				},
			},
		},
	}
	service.live = stub
	service.liveConnected = true

	menu := openOnlyProjectMenu(t, service, ctx)
	armToken := callbackTokenForButton(menu.Buttons, "New thread")
	if armToken == "" {
		t.Fatalf("project menu buttons = %#v, want New thread", menu.Buttons)
	}
	armed, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, armToken)
	if err != nil {
		t.Fatalf("HandleCallback(New thread) failed: %v", err)
	}
	if armed == nil || !strings.Contains(armed.Text, "Send the first prompt") {
		t.Fatalf("armed response = %#v, want prompt instruction", armed)
	}

	response, err := service.handlePlainText(ctx, 123456789, 0, "first prompt", 0)
	if err != nil {
		t.Fatalf("handlePlainText(first prompt) failed: %v", err)
	}
	if response == nil || response.ThreadID != "new-thread-id" || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want new thread/turn", response)
	}
	if len(stub.threadStartCalls) != 1 || stub.threadStartCalls[0] != "/Users/example/project" {
		t.Fatalf("threadStartCalls = %#v, want project cwd", stub.threadStartCalls)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one turn start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.threadID != "new-thread-id" || operatorPrompt(got.message) != "first prompt" || got.cwd != "/Users/example/project" {
		t.Fatalf("turnStartCall = %#v, want new thread first prompt in project cwd", got)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "new-thread-id" {
		t.Fatalf("binding = %#v, want new thread binding", binding)
	}
}

func TestProjectNewThreadRejectsThreadStartWithoutID(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.UpsertThread(ctx, model.Thread{
		ID:            "project-source-thread",
		Title:         "Source",
		CWD:           "/Users/example/project",
		ProjectName:   "project",
		DirectoryName: "project",
		UpdatedAt:     10,
		Raw:           json.RawMessage(`{"id":"project-source-thread"}`),
	}); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"cwd": "/Users/example/project"}}}
	service.live = stub
	service.liveConnected = true

	menu := openOnlyProjectMenu(t, service, ctx)
	_, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, callbackTokenForButton(menu.Buttons, "New thread"))
	if err != nil {
		t.Fatalf("HandleCallback(New thread) failed: %v", err)
	}
	response, err := service.handlePlainText(ctx, 123456789, 0, "first prompt", 0)
	if err != nil {
		t.Fatalf("handlePlainText failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "could not create thread") {
		t.Fatalf("response = %#v, want missing thread id error", response)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no turn start without thread id", stub.turnStartCalls)
	}
}

func TestProjectNewThreadTurnStartFailureSavesThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.UpsertThread(ctx, model.Thread{
		ID:            "project-source-thread",
		Title:         "Source",
		CWD:           "/Users/example/project",
		ProjectName:   "project",
		DirectoryName: "project",
		UpdatedAt:     10,
		Raw:           json.RawMessage(`{"id":"project-source-thread"}`),
	}); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "new-thread-id", "cwd": "/Users/example/project"}},
		turnStartErr:      errors.New("turn start failed"),
	}
	service.live = stub
	service.liveConnected = true

	menu := openOnlyProjectMenu(t, service, ctx)
	_, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, callbackTokenForButton(menu.Buttons, "New thread"))
	if err != nil {
		t.Fatalf("HandleCallback(New thread) failed: %v", err)
	}
	response, err := service.handlePlainText(ctx, 123456789, 0, "first prompt", 0)
	if err != nil {
		t.Fatalf("handlePlainText failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "Created thread") || !strings.Contains(response.Text, "could not start first turn") {
		t.Fatalf("response = %#v, want recoverable turn start failure", response)
	}
	thread, err := service.store.GetThread(ctx, "new-thread-id")
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thread == nil || thread.CWD != "/Users/example/project" {
		t.Fatalf("thread = %#v, want saved new thread", thread)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "new-thread-id" {
		t.Fatalf("binding = %#v, want new thread binding for recovery", binding)
	}
}

func TestNewChatCommandCreatesCodexUIChatCWDAndBinds(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	chatRoot := t.TempDir()
	fixedNow := time.Date(2026, 5, 5, 14, 32, 5, 0, time.Local)
	service.cfg.CodexChatsRoot = chatRoot
	service.now = func() time.Time { return fixedNow }
	expectedCWD := filepath.Join(chatRoot, "2026-05-05", "tool-call")
	ctx := context.Background()
	stub := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "new-chat-thread", "title": "New chat"}},
		threadReads: map[string]map[string]any{
			"new-chat-thread": {
				"thread": map[string]any{
					"id":     "new-chat-thread",
					"title":  "New chat",
					"cwd":    expectedCWD,
					"status": "active",
					"turns": []any{map[string]any{
						"id":     "started-turn",
						"status": "inProgress",
						"items":  []any{map[string]any{"id": "user-item", "type": "userMessage", "content": []any{map[string]any{"text": "Проверь tool call по погоде"}}}},
					}},
				},
			},
		},
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/newchat Проверь tool call по погоде", 0)
	if err != nil {
		t.Fatalf("handleCommand(/newchat) failed: %v", err)
	}
	if response == nil || response.ThreadID != "new-chat-thread" || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want new chat thread/turn", response)
	}
	if len(stub.threadStartCalls) != 1 || stub.threadStartCalls[0] != expectedCWD {
		t.Fatalf("threadStartCalls = %#v, want chat cwd %q", stub.threadStartCalls, expectedCWD)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one turn start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.threadID != "new-chat-thread" || operatorPrompt(got.message) != "Проверь tool call по погоде" || got.cwd != expectedCWD {
		t.Fatalf("turnStartCall = %#v, want new chat first prompt with cwd %q", got, expectedCWD)
	}
	if info, err := os.Stat(expectedCWD); err != nil || !info.IsDir() {
		t.Fatalf("expected chat cwd %q to exist as directory, info=%#v err=%v", expectedCWD, info, err)
	}
	thread, err := service.store.GetThread(ctx, "new-chat-thread")
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thread == nil || thread.ProjectName != "Chats" || thread.DirectoryName != "tool-call" || thread.CWD != expectedCWD {
		t.Fatalf("thread = %#v, want stored Chats thread at generated cwd", thread)
	}
	catalog, err := service.projectCatalog(ctx)
	if err != nil {
		t.Fatalf("projectCatalog failed: %v", err)
	}
	if len(catalog.Chats) != 1 || catalog.Chats[0].ID != "new-chat-thread" {
		t.Fatalf("catalog.Chats = %#v, want new chat thread", catalog.Chats)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "new-chat-thread" {
		t.Fatalf("binding = %#v, want new chat binding", binding)
	}
}

func TestNewChatCWDUsesFallbackSlugAndCollisionSuffix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixedNow := time.Date(2026, 5, 5, 14, 32, 5, 0, time.Local)
	existing := filepath.Join(root, "2026-05-05", "chat-143205")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("MkdirAll(existing) failed: %v", err)
	}

	cwd, directoryName, err := createCodexChatCWD(root, "Привет мир", fixedNow)
	if err != nil {
		t.Fatalf("createCodexChatCWD failed: %v", err)
	}
	want := filepath.Join(root, "2026-05-05", "chat-143205-2")
	if cwd != want || directoryName != "chat-143205-2" {
		t.Fatalf("cwd=%q directoryName=%q, want %q and chat-143205-2", cwd, directoryName, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("expected fallback cwd %q to exist as directory, info=%#v err=%v", want, info, err)
	}
}

func TestNewThreadCommandCreatesThreadWithoutCWDAndBinds(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()
	stub := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "new-thread-without-cwd", "title": "New thread"}},
		threadReads: map[string]map[string]any{
			"new-thread-without-cwd": {
				"thread": map[string]any{
					"id":     "new-thread-without-cwd",
					"title":  "New thread",
					"status": "active",
					"turns": []any{map[string]any{
						"id":     "started-turn",
						"status": "inProgress",
						"items":  []any{map[string]any{"id": "user-item", "type": "userMessage", "content": []any{map[string]any{"text": "scratch prompt"}}}},
					}},
				},
			},
		},
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/newthread scratch prompt", 0)
	if err != nil {
		t.Fatalf("handleCommand(/newthread) failed: %v", err)
	}
	if response == nil || response.ThreadID != "new-thread-without-cwd" || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want no-cwd thread/turn", response)
	}
	if len(stub.threadStartCalls) != 1 || stub.threadStartCalls[0] != "" {
		t.Fatalf("threadStartCalls = %#v, want no cwd", stub.threadStartCalls)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one turn start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.threadID != "new-thread-without-cwd" || operatorPrompt(got.message) != "scratch prompt" || got.cwd != "" {
		t.Fatalf("turnStartCall = %#v, want no-cwd first prompt", got)
	}
	thread, err := service.store.GetThread(ctx, "new-thread-without-cwd")
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thread == nil || thread.CWD != "" || thread.ProjectName == "Chats" {
		t.Fatalf("thread = %#v, want no-cwd non-Chat thread", thread)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "new-thread-without-cwd" {
		t.Fatalf("binding = %#v, want newthread binding", binding)
	}
}

func TestNewChatCommandRejectsMissingThreadID(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	service.cfg.CodexChatsRoot = t.TempDir()
	ctx := context.Background()
	stub := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"title": "New chat"}}}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/newchat first chat prompt", 0)
	if err != nil {
		t.Fatalf("handleCommand(/newchat) failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "could not create thread") {
		t.Fatalf("response = %#v, want missing thread id error", response)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no turn start without thread id", stub.turnStartCalls)
	}
}

func TestNewChatCommandTurnStartFailureSavesAndBindsThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	chatRoot := t.TempDir()
	fixedNow := time.Date(2026, 5, 5, 14, 32, 5, 0, time.Local)
	service.cfg.CodexChatsRoot = chatRoot
	service.now = func() time.Time { return fixedNow }
	expectedCWD := filepath.Join(chatRoot, "2026-05-05", "first-chat-prompt")
	ctx := context.Background()
	stub := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "new-chat-thread", "title": "New chat"}},
		turnStartErr:      errors.New("turn start failed"),
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/newchat first chat prompt", 0)
	if err != nil {
		t.Fatalf("handleCommand(/newchat) failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "Created thread") || !strings.Contains(response.Text, "could not start first turn") {
		t.Fatalf("response = %#v, want recoverable turn start failure", response)
	}
	thread, err := service.store.GetThread(ctx, "new-chat-thread")
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thread == nil || thread.ProjectName != "Chats" || thread.CWD != expectedCWD {
		t.Fatalf("thread = %#v, want saved Chats thread with cwd %q", thread, expectedCWD)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "new-chat-thread" {
		t.Fatalf("binding = %#v, want new chat binding for recovery", binding)
	}
}

func TestNewThreadCommandTurnStartFailureSavesAndBindsThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	stub := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "no-cwd-thread", "title": "No cwd"}},
		turnStartErr:      errors.New("turn start failed"),
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/newthread scratch prompt", 0)
	if err != nil {
		t.Fatalf("handleCommand(/newthread) failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "Created thread") || !strings.Contains(response.Text, "could not start first turn") {
		t.Fatalf("response = %#v, want recoverable turn start failure", response)
	}
	thread, err := service.store.GetThread(ctx, "no-cwd-thread")
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thread == nil || thread.CWD != "" || thread.ProjectName == "Chats" {
		t.Fatalf("thread = %#v, want saved no-cwd non-Chat thread", thread)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "no-cwd-thread" {
		t.Fatalf("binding = %#v, want no-cwd thread binding for recovery", binding)
	}
}

func TestSummaryPanelDoesNotShowStalePendingUserInputButtons(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "thread-stale-pending",
		Title:       "Stale pending",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "active",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:             thread,
		LatestTurnID:       "new-turn",
		LatestTurnStatus:   "inProgress",
		LatestProgressText: "Working on new turn.",
		LatestProgressFP:   "new-progress",
	}
	pending := &model.PendingApproval{
		RequestID:   "old-request",
		ThreadID:    thread.ID,
		TurnID:      "old-turn",
		PromptKind:  "user_input",
		Question:    "Old choice?",
		Status:      "pending",
		PayloadJSON: `{"questions":[{"id":"choice","question":"Old choice?","options":[{"label":"Old option","description":"Old."}]}]}`,
	}

	message, buttons, _ := service.renderSummaryPanel(ctx, thread, snapshot, pending)
	if strings.Contains(message.Text, "Old choice?") {
		t.Fatalf("summary text = %q, want no stale pending question", message.Text)
	}
	if callbackTokenForButton(buttons, "Old option") != "" {
		t.Fatalf("summary buttons = %#v, want no stale pending choice button", buttons)
	}
}

func TestTrackedThreadsIncludesRecentlyChangedTerminalThreadForGlobalObserver(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetGlobalObserverTarget(ctx, 123456789, 0, true); err != nil {
		t.Fatalf("SetGlobalObserverTarget failed: %v", err)
	}

	thread := model.Thread{
		ID:          "recent-terminal",
		Title:       "Recent terminal",
		ProjectName: "Codex",
		UpdatedAt:   time.Now().UTC().Unix(),
		Status:      "completed",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	oldSnapshot := model.ThreadSnapshotState{
		ThreadUpdatedAt:      thread.UpdatedAt - 120,
		LastSeenThreadStatus: "completed",
		LastSeenTurnID:       "turn-old",
		LastSeenTurnStatus:   "completed",
	}
	if err := service.store.UpsertSnapshot(ctx, thread.ID, oldSnapshot); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}

	tracked := service.trackedThreads(ctx, 10)
	found := false
	for _, candidate := range tracked {
		if candidate.ID == thread.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("tracked threads do not include recent terminal change: %#v", tracked)
	}
}

func TestTrackedThreadsSkipsRecentTerminalChangeThatPredatesObserveEnable(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Unix()
	if err := service.store.UpsertThread(ctx, model.Thread{
		ID:          "recent-before-enable",
		Title:       "Recent but old for observer",
		ProjectName: "Codex",
		UpdatedAt:   now - 30,
		Status:      "completed",
	}); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.SetGlobalObserverTarget(ctx, 123456789, 0, true); err != nil {
		t.Fatalf("SetGlobalObserverTarget failed: %v", err)
	}

	tracked := service.trackedThreads(ctx, 10)
	for _, thread := range tracked {
		if thread.ID == "recent-before-enable" {
			t.Fatalf("tracked threads unexpectedly include completion from before /observe all: %#v", tracked)
		}
	}
}

func TestCurrentPanelThreadIDsSkipTerminalGlobalObserverPanels(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	threadA := model.Thread{ID: "thread-a", Title: "A", ProjectName: "Codex", UpdatedAt: time.Now().UTC().Unix(), Status: "idle"}
	threadB := model.Thread{ID: "thread-b", Title: "B", ProjectName: "Codex", UpdatedAt: time.Now().UTC().Unix(), Status: "idle"}
	if err := service.store.UpsertThread(ctx, threadA); err != nil {
		t.Fatalf("UpsertThread(threadA) failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, threadB); err != nil {
		t.Fatalf("UpsertThread(threadB) failed: %v", err)
	}

	if _, err := service.store.CreateThreadPanel(ctx, model.ThreadPanel{
		ChatID:           123456789,
		TopicID:          0,
		ProjectName:      "Codex",
		ThreadID:         threadA.ID,
		SourceMode:       "global_observer",
		SummaryMessageID: 1,
		ToolMessageID:    2,
		OutputMessageID:  3,
		CurrentTurnID:    "turn-a",
		Status:           "completed",
		ArchiveEnabled:   true,
	}); err != nil {
		t.Fatalf("CreateThreadPanel(global_observer terminal) failed: %v", err)
	}
	if _, err := service.store.CreateThreadPanel(ctx, model.ThreadPanel{
		ChatID:           123456789,
		TopicID:          0,
		ProjectName:      "Codex",
		ThreadID:         threadB.ID,
		SourceMode:       "explicit",
		SummaryMessageID: 11,
		ToolMessageID:    12,
		OutputMessageID:  13,
		CurrentTurnID:    "turn-b",
		Status:           "completed",
		ArchiveEnabled:   true,
	}); err != nil {
		t.Fatalf("CreateThreadPanel(explicit terminal) failed: %v", err)
	}

	ids := service.currentPanelThreadIDs(ctx)
	foundA := false
	foundB := false
	for _, id := range ids {
		if id == threadA.ID {
			foundA = true
		}
		if id == threadB.ID {
			foundB = true
		}
	}

	if foundA {
		t.Fatalf("currentPanelThreadIDs unexpectedly include terminal global_observer panel: %#v", ids)
	}
	if foundB {
		t.Fatalf("currentPanelThreadIDs unexpectedly include terminal explicit panel: %#v", ids)
	}
}

func TestCurrentPanelThreadIDsSkipTerminalExplicitPanels(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	thread := model.Thread{ID: "thread-explicit-terminal", Title: "Explicit", ProjectName: "Codex", UpdatedAt: time.Now().UTC().Unix(), Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}

	if _, err := service.store.CreateThreadPanel(ctx, model.ThreadPanel{
		ChatID:           123456789,
		TopicID:          0,
		ProjectName:      "Codex",
		ThreadID:         thread.ID,
		SourceMode:       "explicit",
		SummaryMessageID: 1,
		ToolMessageID:    2,
		OutputMessageID:  3,
		CurrentTurnID:    "turn-explicit",
		Status:           "completed",
		ArchiveEnabled:   true,
	}); err != nil {
		t.Fatalf("CreateThreadPanel(explicit terminal) failed: %v", err)
	}

	ids := service.currentPanelThreadIDs(ctx)
	for _, id := range ids {
		if id == thread.ID {
			t.Fatalf("currentPanelThreadIDs unexpectedly include terminal explicit panel: %#v", ids)
		}
	}
}

func TestThreadNeedsLiveSyncSkipsTerminalGlobalObserverPanels(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	thread := model.Thread{ID: "thread-live", Title: "Live", ProjectName: "Codex", UpdatedAt: time.Now().UTC().Unix(), Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if _, err := service.store.CreateThreadPanel(ctx, model.ThreadPanel{
		ChatID:           123456789,
		TopicID:          0,
		ProjectName:      "Codex",
		ThreadID:         thread.ID,
		SourceMode:       "global_observer",
		SummaryMessageID: 1,
		ToolMessageID:    2,
		OutputMessageID:  3,
		CurrentTurnID:    "turn-1",
		Status:           "completed",
		ArchiveEnabled:   true,
	}); err != nil {
		t.Fatalf("CreateThreadPanel failed: %v", err)
	}

	if service.threadNeedsLiveSync(ctx, thread.ID) {
		t.Fatal("threadNeedsLiveSync returned true for terminal global_observer panel")
	}

	if err := service.store.SetBinding(ctx, 123456789, 0, thread.ID, model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}
	if !service.threadNeedsLiveSync(ctx, thread.ID) {
		t.Fatal("threadNeedsLiveSync returned false for bound thread")
	}
}

func TestLiveToolNotificationStoresRunningCommandWithoutRenderingItAsCurrent(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()
	turnID := "turn-live-tool"
	thread := model.Thread{
		ID:           "thread-live-tool",
		Title:        "Live tool",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	staleCurrent := appserver.ThreadReadSnapshot{
		Thread:             thread,
		LatestTurnID:       turnID,
		LatestTurnStatus:   "inProgress",
		LatestProgressText: "printf 'alpha\\nbeta\\n'",
		LatestProgressFP:   "progress-alpha-fp",
		LatestToolID:       "cmd-alpha",
		LatestToolKind:     "commandExecution",
		LatestToolLabel:    "printf 'alpha\\nbeta\\n'",
		LatestToolStatus:   "completed",
		LatestToolOutput:   "alpha\nbeta\n",
		LatestToolFP:       "tool-alpha-fp",
		DetailItems: []model.DetailItem{
			{ID: "cmd-alpha", Kind: model.DetailItemTool, Label: "printf 'alpha\\nbeta\\n'", Status: "completed"},
			{ID: "cmd-alpha:output", Kind: model.DetailItemOutput, Output: "alpha\nbeta\n"},
		},
	}
	if err := service.store.UpsertSnapshot(ctx, thread.ID, appserver.CompactSnapshot(nil, staleCurrent, time.Now().UTC())); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	summaryMessage, _, summaryHash := service.renderSummaryPanel(ctx, thread, &staleCurrent, nil)
	_ = summaryMessage
	_, staleToolHash := service.renderToolPanel(ctx, thread, &staleCurrent)
	_, staleOutputHash := service.renderOutputPanel(ctx, thread, &staleCurrent)
	if _, err := service.store.CreateThreadPanel(ctx, model.ThreadPanel{
		ChatID:           123456789,
		TopicID:          0,
		ProjectName:      thread.ProjectName,
		ThreadID:         thread.ID,
		SourceMode:       model.PanelSourceTelegramInput,
		SummaryMessageID: 201,
		ToolMessageID:    202,
		OutputMessageID:  203,
		CurrentTurnID:    turnID,
		Status:           "inProgress",
		ArchiveEnabled:   true,
		LastSummaryHash:  summaryHash,
		LastToolHash:     staleToolHash,
		LastOutputHash:   staleOutputHash,
	}); err != nil {
		t.Fatalf("CreateThreadPanel failed: %v", err)
	}
	stub := &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: {
				"thread": map[string]any{
					"id":           thread.ID,
					"title":        thread.Title,
					"cwd":          thread.CWD,
					"status":       "active",
					"activeTurnId": turnID,
					"turns": []any{
						map[string]any{
							"id":     turnID,
							"status": "inProgress",
							"items": []any{
								map[string]any{
									"id":               "cmd-alpha",
									"type":             "commandExecution",
									"command":          "printf 'alpha\\nbeta\\n'",
									"status":           "completed",
									"aggregatedOutput": "alpha\nbeta\n",
								},
							},
						},
					},
				},
			},
		},
	}

	service.handleLiveEvent(ctx, stub, appserver.Event{
		Channel: "notification",
		Method:  "item/started",
		Params: map[string]any{
			"threadId": thread.ID,
			"turnId":   turnID,
			"item": map[string]any{
				"id":      "cmd-slow",
				"type":    "commandExecution",
				"command": "sleep 20; printf 'slow-command-done\\n'",
				"status":  "running",
			},
		},
	})

	renderedRunningTool := false
	resetCompletedOutput := false
	for _, edit := range sender.edits {
		switch edit.messageID {
		case 202:
			if strings.Contains(edit.text, "sleep 20") &&
				strings.Contains(edit.text, "slow-command-done") &&
				strings.Contains(edit.text, "Status: running") {
				renderedRunningTool = true
			}
		case 203:
			if strings.Contains(edit.text, "No completed tool output yet.") ||
				(strings.Contains(edit.text, "slow-command-done") && !strings.Contains(edit.text, "alpha")) {
				resetCompletedOutput = true
			}
		}
	}
	if renderedRunningTool {
		t.Fatalf("running command was rendered as current tool; edits=%#v", sender.edits)
	}
	if resetCompletedOutput {
		t.Fatalf("completed output was reset by running live tool; edits=%#v", sender.edits)
	}
	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if stored == nil {
		t.Fatal("snapshot = nil")
	}
	var current appserver.ThreadReadSnapshot
	if err := json.Unmarshal(stored.CompactJSON, &current); err != nil {
		t.Fatalf("unmarshal CompactJSON failed: %v", err)
	}
	if got := current.LatestToolLabel; !strings.Contains(got, "sleep 20") {
		t.Fatalf("LatestToolLabel = %q, want running sleep command", got)
	}
	if got, want := current.LatestToolStatus, "running"; got != want {
		t.Fatalf("LatestToolStatus = %q, want %q", got, want)
	}
	toolText, _ := service.renderToolPanel(ctx, thread, &current)
	if strings.Contains(toolText, "sleep 20") || strings.Contains(toolText, "Status: running") {
		t.Fatalf("rendered tool = %q, want running command hidden", toolText)
	}
	if !strings.Contains(toolText, "printf") || !strings.Contains(toolText, "Status: completed") {
		t.Fatalf("rendered tool = %q, want last completed command", toolText)
	}
	outputText, _ := service.renderOutputPanel(ctx, thread, &current)
	if !strings.Contains(outputText, "alpha") || strings.Contains(outputText, "slow-command-done") {
		t.Fatalf("rendered output = %q, want last completed output", outputText)
	}

	current.LatestToolStatus = "completed"
	current.LatestToolOutput = "slow-command-done\n"
	current.LatestToolFP = "tool-slow-completed-fp"
	if err := service.store.UpsertSnapshot(ctx, thread.ID, appserver.CompactSnapshot(stored, current, time.Now().UTC())); err != nil {
		t.Fatalf("UpsertSnapshot(completed tool) failed: %v", err)
	}
	if service.applyLiveToolSnapshot(ctx, thread.ID, appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
		LatestToolID:     "cmd-slow",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "sleep 20; printf 'slow-command-done\\n'",
		LatestToolStatus: "running",
		LatestToolFP:     "late-running-fp",
	}) {
		t.Fatal("late running live tool update downgraded completed tool")
	}
	stored, err = service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot(after late live update) failed: %v", err)
	}
	if err := json.Unmarshal(stored.CompactJSON, &current); err != nil {
		t.Fatalf("unmarshal CompactJSON(after late live update) failed: %v", err)
	}
	if got, want := current.LatestToolStatus, "completed"; got != want {
		t.Fatalf("LatestToolStatus(after late live update) = %q, want %q", got, want)
	}
}

func TestLiveToolNotificationIgnoresOlderTurnAfterNewerCompletion(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "thread-old-live-tool",
		Title:       "Old live tool",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		UpdatedAt:   time.Now().UTC().Unix(),
		Status:      "idle",
	}
	current := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "01900000-0000-7000-8000-000000000020",
		LatestTurnStatus: "completed",
		LatestToolID:     "cmd-new",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "printf 'alpha\\nbeta\\n'",
		LatestToolStatus: "completed",
		LatestToolOutput: "alpha\nbeta\n",
		LatestToolFP:     "tool-new-completed",
		LatestFinalText:  "OK_LIVE_COMMANDS_printf",
		LatestFinalFP:    "final-new",
	}
	state := appserver.CompactSnapshot(nil, current, time.Now().UTC())
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.UpsertSnapshot(ctx, thread.ID, state); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}

	applied := service.applyLiveToolSnapshot(ctx, thread.ID, appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "01900000-0000-7000-8000-000000000010",
		LatestTurnStatus: "inProgress",
		LatestToolID:     "cmd-old",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "date",
		LatestToolStatus: "completed",
		LatestToolOutput: "Sat May  2\n",
		LatestToolFP:     "tool-old-completed",
	})
	if applied {
		t.Fatal("older live tool update overwrote newer completed turn")
	}
	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	var compact appserver.ThreadReadSnapshot
	if err := json.Unmarshal(stored.CompactJSON, &compact); err != nil {
		t.Fatalf("unmarshal compact snapshot: %v", err)
	}
	if compact.LatestTurnID != current.LatestTurnID {
		t.Fatalf("LatestTurnID = %q, want %q", compact.LatestTurnID, current.LatestTurnID)
	}
	if strings.Contains(compact.LatestToolLabel, "date") {
		t.Fatalf("LatestToolLabel = %q, want newer command preserved", compact.LatestToolLabel)
	}
}

func TestLiveToolNotificationIgnoresOlderSameTurnToolAfterNewerTool(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:           "thread-old-same-turn-tool",
		Title:        "Old same turn tool",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: "turn-same",
	}
	current := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "turn-same",
		LatestTurnStatus: "inProgress",
		LatestToolID:     "cmd-range-3",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "/tmp/math.py 60001 90000",
		LatestToolStatus: "completed",
		LatestToolOutput: "RANGE 60001 90000\n",
		LatestToolFP:     "tool-range-3-completed",
		DetailItems: []model.DetailItem{
			{ID: "cmd-range-1", Kind: model.DetailItemTool, Label: "/tmp/math.py 1 30000", Status: "completed"},
			{ID: "cmd-range-1:output", Kind: model.DetailItemOutput, Output: "RANGE 1 30000\n"},
			{ID: "cmd-range-2", Kind: model.DetailItemTool, Label: "/tmp/math.py 30001 60000", Status: "completed"},
			{ID: "cmd-range-2:output", Kind: model.DetailItemOutput, Output: "RANGE 30001 60000\n"},
			{ID: "cmd-range-3", Kind: model.DetailItemTool, Label: "/tmp/math.py 60001 90000", Status: "completed"},
			{ID: "cmd-range-3:output", Kind: model.DetailItemOutput, Output: "RANGE 60001 90000\n"},
		},
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.UpsertSnapshot(ctx, thread.ID, appserver.CompactSnapshot(nil, current, time.Now().UTC())); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}

	applied := service.applyLiveToolSnapshot(ctx, thread.ID, appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "turn-same",
		LatestTurnStatus: "inProgress",
		LatestToolID:     "cmd-range-1",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "/tmp/math.py 1 30000",
		LatestToolStatus: "running",
		LatestToolFP:     "tool-range-1-late-running",
	})
	if applied {
		t.Fatal("older same-turn live tool update overwrote newer tool")
	}
	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	var compact appserver.ThreadReadSnapshot
	if err := json.Unmarshal(stored.CompactJSON, &compact); err != nil {
		t.Fatalf("unmarshal compact snapshot: %v", err)
	}
	if compact.LatestToolID != current.LatestToolID {
		t.Fatalf("LatestToolID = %q, want %q", compact.LatestToolID, current.LatestToolID)
	}

	applied = service.applyLiveToolSnapshot(ctx, thread.ID, appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "turn-same",
		LatestTurnStatus: "inProgress",
		LatestToolID:     "cmd-range-4",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "/tmp/math.py 90001 120000",
		LatestToolStatus: "running",
		LatestToolFP:     "tool-range-4-running",
	})
	if !applied {
		t.Fatal("new same-turn live tool update was rejected")
	}
	stored, err = service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot(after newer tool) failed: %v", err)
	}
	if err := json.Unmarshal(stored.CompactJSON, &compact); err != nil {
		t.Fatalf("unmarshal compact snapshot(after newer tool): %v", err)
	}
	if compact.LatestToolID != "cmd-range-4" {
		t.Fatalf("LatestToolID(after newer tool) = %q, want cmd-range-4", compact.LatestToolID)
	}
}

func TestPollSnapshotWithoutToolDoesNotPreserveSameTurnRunningToolAsCurrent(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	turnID := "turn-live-tool-preserve"
	thread := model.Thread{
		ID:           "thread-live-tool-preserve",
		Title:        "Live tool preserve",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	firstSeen := time.Date(2026, time.May, 1, 23, 46, 1, 0, time.UTC)
	previousCurrent := appserver.ThreadReadSnapshot{
		Thread:              thread,
		LatestTurnID:        turnID,
		LatestTurnStatus:    "inProgress",
		LatestToolID:        "cmd-slow",
		LatestToolKind:      "commandExecution",
		LatestToolLabel:     "sleep 20; printf 'slow-command-done\\n'",
		LatestToolStatus:    "running",
		LatestToolFP:        "cmd-slow-running-fp",
		LatestToolStartedAt: firstSeen.Format(time.RFC3339Nano),
		LatestToolUpdatedAt: firstSeen.Format(time.RFC3339Nano),
	}
	previous := appserver.CompactSnapshot(nil, previousCurrent, firstSeen)
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.UpsertSnapshot(ctx, thread.ID, previous); err != nil {
		t.Fatalf("UpsertSnapshot(previous) failed: %v", err)
	}
	pollWithoutTool := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
	}
	next := appserver.CompactSnapshot(&previous, pollWithoutTool, firstSeen.Add(8*time.Second))
	if err := service.store.UpsertSnapshot(ctx, thread.ID, next); err != nil {
		t.Fatalf("UpsertSnapshot(next) failed: %v", err)
	}
	_, current, err := service.loadThreadPanelSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("loadThreadPanelSnapshot failed: %v", err)
	}

	text, _ := service.renderToolPanelAt(ctx, thread, current, firstSeen.Add(8*time.Second))

	if strings.Contains(text, "sleep 20") || strings.Contains(text, "Status: running") {
		t.Fatalf("rendered tool = %q, want omitted running tool hidden", text)
	}
	if !strings.Contains(text, "No completed tool yet.") {
		t.Fatalf("rendered tool = %q, want neutral completed-tool absence", text)
	}
	summaryMessages := service.renderSummaryPanelMarkdownAt(ctx, thread, current, nil, nil, firstSeen.Add(8*time.Second))
	if len(summaryMessages) != 1 {
		t.Fatalf("len(summaryMessages) = %d, want 1", len(summaryMessages))
	}
	if !strings.Contains(summaryMessages[0].Text, "Run active for: 8s") {
		t.Fatalf("rendered summary = %q, want elapsed run time", summaryMessages[0].Text)
	}
}

func TestPollSnapshotWithoutToolPreservesTelegramOriginLiveCurrentTool(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	turnID := "turn-telegram-live-current-preserve"
	thread := model.Thread{
		ID:           "thread-telegram-live-current-preserve",
		Title:        "Telegram live preserve",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	firstSeen := time.Date(2026, time.May, 1, 23, 46, 1, 0, time.UTC)
	previousCurrent := appserver.ThreadReadSnapshot{
		Thread:                thread,
		LatestTurnID:          turnID,
		LatestTurnStatus:      "inProgress",
		LatestToolID:          "cmd-slow",
		LatestToolKind:        "commandExecution",
		LatestToolLabel:       "sleep 20; printf 'slow-command-done\\n'",
		LatestToolStatus:      "running",
		LatestToolFP:          "cmd-slow-running-fp",
		LatestToolLiveCurrent: true,
		LatestToolStartedAt:   firstSeen.Format(time.RFC3339Nano),
		LatestToolUpdatedAt:   firstSeen.Format(time.RFC3339Nano),
		DetailItems: []model.DetailItem{
			{ID: "item-user", Kind: model.DetailItemUser, Text: "run slow command"},
			{ID: "cmd-slow", Kind: model.DetailItemTool, Label: "sleep 20; printf 'slow-command-done\\n'", Status: "running", FP: "cmd-slow-running-fp"},
		},
	}
	previous := appserver.CompactSnapshot(nil, previousCurrent, firstSeen)
	pollWithoutTool := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
		DetailItems: []model.DetailItem{
			{ID: "item-user", Kind: model.DetailItemUser, Text: "run slow command"},
		},
	}

	service.preserveTelegramOriginLiveCurrentTool(ctx, &pollWithoutTool, &previous)

	if !pollWithoutTool.LatestToolLiveCurrent {
		t.Fatal("LatestToolLiveCurrent = false, want preserved live current tool")
	}
	if got := pollWithoutTool.LatestToolLabel; !strings.Contains(got, "slow-command-done") {
		t.Fatalf("LatestToolLabel = %q, want preserved live current command", got)
	}
	text, _ := service.renderToolPanelAt(ctx, thread, &pollWithoutTool, firstSeen.Add(8*time.Second))
	if !strings.Contains(text, "Current tool:") || !strings.Contains(text, "slow-command-done") || !strings.Contains(text, "Status: running") {
		t.Fatalf("rendered tool = %q, want preserved current command", text)
	}
	if strings.Contains(text, "item-user") {
		t.Fatalf("rendered tool = %q, want no duplicated user detail", text)
	}
}

func TestPollSnapshotWithOlderCompletedToolPreservesTelegramOriginLiveCurrentTool(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	turnID := "turn-telegram-live-current-over-completed"
	thread := model.Thread{
		ID:           "thread-telegram-live-current-over-completed",
		Title:        "Telegram live preserve over completed",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	firstSeen := time.Date(2026, time.May, 1, 23, 48, 1, 0, time.UTC)
	previousCurrent := appserver.ThreadReadSnapshot{
		Thread:                thread,
		LatestTurnID:          turnID,
		LatestTurnStatus:      "inProgress",
		LatestToolID:          "cmd-sleep20",
		LatestToolKind:        "commandExecution",
		LatestToolLabel:       "sleep 20",
		LatestToolStatus:      "running",
		LatestToolFP:          "cmd-sleep20-running-fp",
		LatestToolLiveCurrent: true,
		LatestToolStartedAt:   firstSeen.Add(10 * time.Second).Format(time.RFC3339Nano),
		LatestToolUpdatedAt:   firstSeen.Add(10 * time.Second).Format(time.RFC3339Nano),
		DetailItems: []model.DetailItem{
			{ID: "item-user", Kind: model.DetailItemUser, Text: "run two sleeps"},
			{ID: "cmd-sleep10", Kind: model.DetailItemTool, Label: "sleep 10", Status: "completed", FP: "cmd-sleep10-completed-fp"},
			{ID: "cmd-sleep10:output", Kind: model.DetailItemOutput, Output: "sleep10 done\n"},
			{ID: "cmd-sleep20", Kind: model.DetailItemTool, Label: "sleep 20", Status: "running", FP: "cmd-sleep20-running-fp"},
		},
	}
	previous := appserver.CompactSnapshot(nil, previousCurrent, firstSeen.Add(12*time.Second))
	pollWithOlderCompleted := appserver.ThreadReadSnapshot{
		Thread:                thread,
		LatestTurnID:          turnID,
		LatestTurnStatus:      "inProgress",
		LatestToolID:          "cmd-sleep10",
		LatestToolKind:        "commandExecution",
		LatestToolLabel:       "sleep 10",
		LatestToolStatus:      "completed",
		LatestToolOutput:      "sleep10 done\n",
		LatestToolFP:          "cmd-sleep10-completed-fp",
		LatestToolStartedAt:   firstSeen.Format(time.RFC3339Nano),
		LatestToolUpdatedAt:   firstSeen.Add(10 * time.Second).Format(time.RFC3339Nano),
		LatestToolLiveCurrent: false,
		DetailItems: []model.DetailItem{
			{ID: "item-user", Kind: model.DetailItemUser, Text: "run two sleeps"},
			{ID: "cmd-sleep10", Kind: model.DetailItemTool, Label: "sleep 10", Status: "completed", FP: "cmd-sleep10-completed-fp"},
			{ID: "cmd-sleep10:output", Kind: model.DetailItemOutput, Output: "sleep10 done\n"},
		},
	}

	service.preserveTelegramOriginLiveCurrentTool(ctx, &pollWithOlderCompleted, &previous)

	if !pollWithOlderCompleted.LatestToolLiveCurrent {
		t.Fatal("LatestToolLiveCurrent = false, want preserved live current tool")
	}
	if got := pollWithOlderCompleted.LatestToolLabel; got != "sleep 20" {
		t.Fatalf("LatestToolLabel = %q, want preserved live current command", got)
	}
	text, _ := service.renderToolPanelAt(ctx, thread, &pollWithOlderCompleted, firstSeen.Add(15*time.Second))
	if !strings.Contains(text, "Current tool:") || !strings.Contains(text, "sleep 20") || !strings.Contains(text, "Status: running") {
		t.Fatalf("rendered tool = %q, want preserved current command", text)
	}
	if strings.Contains(text, "Last completed tool:") || strings.Contains(text, "sleep 10") {
		t.Fatalf("rendered tool = %q, want older completed command hidden while current command is live", text)
	}
	outputText, _ := service.renderOutputPanel(ctx, thread, &pollWithOlderCompleted)
	if !strings.Contains(outputText, "sleep10 done") {
		t.Fatalf("rendered output = %q, want last completed output preserved", outputText)
	}
}

func TestSnapshotHasPassiveChangeIgnoresIdenticalTerminalReplay(t *testing.T) {
	t.Parallel()

	thread := model.Thread{
		ID:          "thread-passive",
		Title:       "Passive",
		ProjectName: "Codex",
		Status:      "idle",
	}
	current := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "turn-1",
		LatestTurnStatus: "completed",
		LatestFinalFP:    "final-fp-1",
		LatestFinalText:  "Done.",
	}
	previous := appserver.CompactSnapshot(nil, current, time.Now().UTC())

	if snapshotHasPassiveChange(&previous, &current) {
		t.Fatal("snapshotHasPassiveChange returned true for identical terminal replay")
	}

	current.LatestFinalFP = "upgraded-fingerprint"
	current.LatestFinalText = "Done."
	if !snapshotHasPassiveChange(&previous, &current) {
		t.Fatal("snapshotHasPassiveChange returned false for same terminal turn with changed final fingerprint")
	}

	current.LatestTurnID = "turn-2"
	current.LatestFinalFP = "final-fp-2"
	current.LatestFinalText = "Done again."
	if !snapshotHasPassiveChange(&previous, &current) {
		t.Fatal("snapshotHasPassiveChange returned false for new terminal turn")
	}
}

func TestPollTrackedSyncsFirstSeenRecentTerminalCatchup(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()
	now := time.Now().UTC().Unix()
	thread := model.Thread{
		ID:          "thread-catchup-terminal",
		Title:       "Catchup terminal",
		ProjectName: "Codex",
		CWD:         `C:\Users\you\Projects\Codex`,
		UpdatedAt:   now,
		Status:      "completed",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.SetGlobalObserverTarget(ctx, 123456789, 0, true); err != nil {
		t.Fatalf("SetGlobalObserverTarget failed: %v", err)
	}
	service.poll = &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: {
				"id":     thread.ID,
				"name":   thread.Title,
				"cwd":    thread.CWD,
				"status": "completed",
				"turns": []any{
					map[string]any{
						"id":     "turn-catchup",
						"status": "completed",
						"items": []any{
							map[string]any{
								"id":    "agent-final",
								"type":  "agentMessage",
								"phase": "final_answer",
								"text":  "CATCHUP_OK",
							},
						},
					},
				},
			},
		},
	}
	service.pollConnected = true

	service.pollTracked(ctx)

	if len(sender.messages) != 4 {
		t.Fatalf("message count = %d, want 3 live trio messages plus Final for first-seen terminal catchup; messages=%#v", len(sender.messages), sender.messages)
	}
	foundFinal := false
	for _, message := range sender.messages {
		if hasHeaderKind(message.text, "Final") && strings.Contains(message.text, "[Codex]") && strings.Contains(message.text, "[Catchup terminal]") && strings.Contains(message.text, "CATCHUP_OK") {
			if message.options.Silent {
				t.Fatalf("final message = %#v, want audible Final", message)
			}
			foundFinal = true
			break
		}
	}
	if !foundFinal {
		t.Fatalf("final card message not found: %#v", sender.messages)
	}
	if len(sender.deletes) != 3 {
		t.Fatalf("deletes = %#v, want commentary/tool/output cleanup after final", sender.deletes)
	}
}

func TestPollTrackedDefersTelegramOriginEmptyInterruptedAndKeepsActiveState(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	logs := captureServiceLogs(service)
	ctx := context.Background()
	turnID := "turn-empty-interrupted"
	thread := model.Thread{
		ID:           "thread-empty-interrupted",
		Title:        "Empty interrupted",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	previousCurrent := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
	}
	previous := appserver.CompactSnapshot(nil, previousCurrent, time.Now().UTC())
	if err := service.store.UpsertSnapshot(ctx, thread.ID, previous); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	service.poll = &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: diagnosticThreadReadPayload(thread, turnID, "interrupted"),
		},
	}
	service.pollConnected = true

	service.pollTracked(ctx)

	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if stored == nil {
		t.Fatal("snapshot = nil")
	}
	if stored.LastSeenTurnStatus != "inProgress" {
		t.Fatalf("LastSeenTurnStatus = %q, want inProgress", stored.LastSeenTurnStatus)
	}
	if stored.LastCompletionFP != "" {
		t.Fatalf("LastCompletionFP = %q, want empty while deferred", stored.LastCompletionFP)
	}
	if stored.NextPollAfter == "" {
		t.Fatal("NextPollAfter is empty, want hot polling while deferred")
	}
	if !service.threadNeedsCatchupPolling(ctx, thread, stored) {
		t.Fatal("threadNeedsCatchupPolling = false, want deferred empty interrupted to keep polling")
	}
	state := loadTerminalGateState(t, service, ctx, terminalGateDeferKey(thread.ID, turnID))
	if state.EmptyInterruptedSeenCount != 1 || state.LastDecision != string(terminalGateDefer) {
		t.Fatalf("defer state = %#v, want one deferred empty interrupted", state)
	}
	got := logs.String()
	requireLogContains(t, got, `"event":"telegram_origin_terminal_deferred"`)
	if strings.Contains(got, `"event":"telegram_origin_turn_terminal"`) {
		t.Fatalf("terminal log should be deferred, got:\n%s", got)
	}
}

func TestPollTrackedDeferredInterruptedDoesNotOverwriteFreshLiveToolSnapshot(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	turnID := "turn-empty-interrupted-live-tool"
	thread := model.Thread{
		ID:           "thread-empty-interrupted-live-tool",
		Title:        "Empty interrupted live tool",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	stalePrevious := appserver.CompactSnapshot(nil, appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
		DetailItems: []model.DetailItem{
			{ID: "user-1", Kind: model.DetailItemUser, Text: "run sleep"},
		},
	}, time.Now().UTC())
	freshLiveCurrent := appserver.CompactSnapshot(nil, appserver.ThreadReadSnapshot{
		Thread:                thread,
		LatestTurnID:          turnID,
		LatestTurnStatus:      "inProgress",
		LatestToolID:          "cmd-sleep",
		LatestToolKind:        "commandExecution",
		LatestToolLabel:       "sleep 10",
		LatestToolStatus:      "running",
		LatestToolFP:          "cmd-sleep-running",
		LatestToolLiveCurrent: true,
		DetailItems: []model.DetailItem{
			{ID: "user-1", Kind: model.DetailItemUser, Text: "run sleep"},
			{ID: "cmd-sleep", Kind: model.DetailItemTool, Label: "sleep 10", Status: "running", FP: "cmd-sleep-running"},
		},
	}, time.Now().UTC())
	if err := service.store.UpsertSnapshot(ctx, thread.ID, freshLiveCurrent); err != nil {
		t.Fatalf("UpsertSnapshot(freshLiveCurrent) failed: %v", err)
	}
	emptyInterrupted := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "interrupted",
		DetailItems: []model.DetailItem{
			{ID: "user-1", Kind: model.DetailItemUser, Text: "run sleep"},
		},
	}

	if !service.applyTelegramOriginTerminalGate(ctx, "poll_tracked", &emptyInterrupted, &stalePrevious) {
		t.Fatal("applyTelegramOriginTerminalGate = false, want deferred interrupted")
	}

	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if stored == nil {
		t.Fatal("snapshot = nil")
	}
	_, current, err := service.loadThreadPanelSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("loadThreadPanelSnapshot failed: %v", err)
	}
	if !current.LatestToolLiveCurrent || current.LatestToolLabel != "sleep 10" {
		t.Fatalf("stored live tool = %q live=%v, want preserved sleep 10 current tool", current.LatestToolLabel, current.LatestToolLiveCurrent)
	}
	if stored.NextPollAfter == "" {
		t.Fatal("NextPollAfter is empty, want hot polling metadata on preserved live snapshot")
	}
}

func TestPollTrackedDefersTelegramOriginPartialInterruptedAndKeepsActiveState(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	logs := captureServiceLogs(service)
	ctx := context.Background()
	turnID := "turn-partial-interrupted"
	thread := model.Thread{
		ID:           "thread-partial-interrupted",
		Title:        "Partial interrupted",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	previousCurrent := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
		LatestToolID:     "cmd-slow",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "sleep 20; printf 'slow-command-done\\n'",
		LatestToolStatus: "running",
		LatestToolFP:     "cmd-slow-running",
	}
	previous := appserver.CompactSnapshot(nil, previousCurrent, time.Now().UTC())
	if err := service.store.UpsertSnapshot(ctx, thread.ID, previous); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	service.poll = &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: diagnosticThreadReadPayloadWithTool(thread, turnID, "interrupted"),
		},
	}
	service.pollConnected = true

	service.pollTracked(ctx)

	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if stored == nil {
		t.Fatal("snapshot = nil")
	}
	if stored.LastSeenTurnStatus != "inProgress" {
		t.Fatalf("LastSeenTurnStatus = %q, want inProgress", stored.LastSeenTurnStatus)
	}
	if stored.LastCompletionFP != "" {
		t.Fatalf("LastCompletionFP = %q, want empty while partial interrupted is deferred", stored.LastCompletionFP)
	}
	if stored.NextPollAfter == "" {
		t.Fatal("NextPollAfter is empty, want hot polling while partial interrupted is deferred")
	}
	state := loadTerminalGateState(t, service, ctx, terminalGateDeferKey(thread.ID, turnID))
	if state.LastReason != "partial_interrupted" || state.LastDecision != string(terminalGateDefer) {
		t.Fatalf("defer state = %#v, want partial_interrupted defer", state)
	}
	got := logs.String()
	requireLogContains(t, got, `"event":"telegram_origin_terminal_deferred"`)
	requireLogContains(t, got, `"reason":"partial_interrupted"`)
	if strings.Contains(got, `"event":"telegram_origin_turn_terminal"`) {
		t.Fatalf("terminal log should be deferred, got:\n%s", got)
	}
}

func TestPollTrackedDefersTelegramOriginFinalInterruptedAndKeepsActiveState(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	logs := captureServiceLogs(service)
	ctx := context.Background()
	turnID := "turn-final-interrupted"
	thread := model.Thread{
		ID:           "thread-final-interrupted",
		Title:        "Final interrupted",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	previousCurrent := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
		LatestToolID:     "cmd-pwd",
		LatestToolKind:   "commandExecution",
		LatestToolLabel:  "pwd",
		LatestToolStatus: "completed",
		LatestToolFP:     "cmd-pwd-completed",
	}
	previous := appserver.CompactSnapshot(nil, previousCurrent, time.Now().UTC())
	if err := service.store.UpsertSnapshot(ctx, thread.ID, previous); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	service.poll = &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: diagnosticThreadReadPayloadWithFinal(thread, turnID, "interrupted", "OK_FINAL"),
		},
	}
	service.pollConnected = true

	service.pollTracked(ctx)

	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if stored == nil {
		t.Fatal("snapshot = nil")
	}
	if stored.LastSeenTurnStatus != "inProgress" {
		t.Fatalf("LastSeenTurnStatus = %q, want inProgress", stored.LastSeenTurnStatus)
	}
	if stored.LastCompletionFP != "" {
		t.Fatalf("LastCompletionFP = %q, want empty while final interrupted is deferred", stored.LastCompletionFP)
	}
	if stored.NextPollAfter == "" {
		t.Fatal("NextPollAfter is empty, want hot polling while final interrupted is deferred")
	}
	state := loadTerminalGateState(t, service, ctx, terminalGateDeferKey(thread.ID, turnID))
	if state.LastReason != "final_interrupted" || state.LastDecision != string(terminalGateDefer) {
		t.Fatalf("defer state = %#v, want final_interrupted defer", state)
	}
	got := logs.String()
	requireLogContains(t, got, `"event":"telegram_origin_terminal_deferred"`)
	requireLogContains(t, got, `"reason":"final_interrupted"`)
	if strings.Contains(got, `"event":"telegram_origin_turn_terminal"`) {
		t.Fatalf("terminal log should be deferred, got:\n%s", got)
	}
}

func TestTelegramOriginHotPollCapturesRunningTool(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	turnID := "turn-hot-poll-tool"
	thread := model.Thread{
		ID:           "thread-hot-poll-tool",
		Title:        "Hot poll tool",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	previous := appserver.CompactSnapshot(nil, appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
	}, time.Now().UTC())
	if err := service.store.UpsertSnapshot(ctx, thread.ID, previous); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	payload := diagnosticThreadReadPayloadWithTool(thread, turnID, "inProgress")
	threadPayload := payload["thread"].(map[string]any)
	threadPayload["status"] = "active"
	threadPayload["activeTurnId"] = turnID
	turn := threadPayload["turns"].([]any)[0].(map[string]any)
	items := turn["items"].([]any)
	tool := items[1].(map[string]any)
	tool["status"] = "inProgress"
	tool["aggregatedOutput"] = nil
	service.poll = &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: payload,
		},
	}
	service.pollConnected = true

	keepGoing := service.telegramOriginHotPollOnce(ctx, thread.ID, turnID)

	if !keepGoing {
		t.Fatal("telegramOriginHotPollOnce returned false for in-progress turn")
	}
	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if stored == nil {
		t.Fatal("snapshot = nil")
	}
	if stored.LastSeenTurnStatus != "inProgress" {
		t.Fatalf("LastSeenTurnStatus = %q, want inProgress", stored.LastSeenTurnStatus)
	}
	_, current, err := service.loadThreadPanelSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("loadThreadPanelSnapshot failed: %v", err)
	}
	if !strings.Contains(current.LatestToolLabel, "sleep 20") || current.LatestToolStatus != "inProgress" {
		t.Fatalf("stored tool = %q/%q, want running sleep command", current.LatestToolLabel, current.LatestToolStatus)
	}
	if stored.NextPollAfter == "" {
		t.Fatal("NextPollAfter is empty, want hot poll continuation")
	}
}

func TestRefreshThreadForOperationDefersEmptyInterrupted(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	turnID := "turn-refresh-empty-interrupted"
	thread := model.Thread{
		ID:           "thread-refresh-empty-interrupted",
		Title:        "Refresh empty interrupted",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	previousCurrent := appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
	}
	previous := appserver.CompactSnapshot(nil, previousCurrent, time.Now().UTC())
	if err := service.store.UpsertSnapshot(ctx, thread.ID, previous); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	stub := &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: diagnosticThreadReadPayload(thread, turnID, "interrupted"),
		},
	}

	refreshed, err := service.refreshThreadForOperation(ctx, stub, thread.ID, "thread_read")
	if err != nil {
		t.Fatalf("refreshThreadForOperation failed: %v", err)
	}
	if refreshed == nil || refreshed.Status != "active" || refreshed.ActiveTurnID != turnID {
		t.Fatalf("refreshed thread = %#v, want existing active thread", refreshed)
	}
	stored, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if stored == nil {
		t.Fatal("snapshot = nil")
	}
	if stored.LastSeenTurnStatus != "inProgress" {
		t.Fatalf("LastSeenTurnStatus = %q, want inProgress", stored.LastSeenTurnStatus)
	}
	if stored.LastCompletionFP != "" {
		t.Fatalf("LastCompletionFP = %q, want empty while deferred", stored.LastCompletionFP)
	}
	if stored.NextPollAfter == "" {
		t.Fatal("NextPollAfter is empty, want hot polling while deferred")
	}
}

func TestInputDuringInterruptedGraceSteersLatestArmedTarget(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	service.cfg.ObserverPollInterval = time.Second
	ctx := context.Background()
	turnID := "turn-interrupted-grace"
	thread := model.Thread{
		ID:           "thread-interrupted-grace",
		Title:        "Interrupted grace",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	if err := service.armSteer(ctx, 123456789, 0, thread.ID, turnID, 0); err != nil {
		t.Fatalf("armSteer failed: %v", err)
	}
	stub := &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: diagnosticThreadReadPayload(thread, turnID, "interrupted"),
		},
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "do not submit this yet")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != turnID {
		t.Fatalf("response = %#v, want steered interrupted target", response)
	}
	if len(stub.turnSteerCalls) != 1 || len(stub.turnStartCalls) != 0 {
		t.Fatalf("turn calls during interrupted grace: steer=%#v start=%#v, want one steer and no start", stub.turnSteerCalls, stub.turnStartCalls)
	}
}

func TestExpiredInterruptedGraceClearsActiveTurnWithoutRearming(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	service.cfg.ObserverPollInterval = time.Second
	ctx := context.Background()
	turnID := "turn-interrupted-expired"
	thread := model.Thread{
		ID:           "thread-interrupted-expired",
		Title:        "Interrupted expired",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	now := time.Now().UTC()
	if err := service.saveTelegramOriginEmptyInterruptedDefer(ctx, thread.ID, turnID, terminalGateState{
		ThreadID:     thread.ID,
		TurnID:       turnID,
		FirstSeenAt:  model.TimeString(now.Add(-2 * time.Minute).Format(time.RFC3339Nano)),
		ExpiresAt:    model.TimeString(now.Add(-time.Minute).Format(time.RFC3339Nano)),
		LastDecision: string(terminalGateDefer),
		LastReason:   "empty_interrupted",
	}); err != nil {
		t.Fatalf("save terminal gate state failed: %v", err)
	}
	stub := &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: diagnosticThreadReadPayload(thread, turnID, "interrupted"),
		},
	}

	for range 2 {
		refreshed, err := service.refreshThreadForOperation(ctx, stub, thread.ID, "thread_read")
		if err != nil {
			t.Fatalf("refreshThreadForOperation failed: %v", err)
		}
		if refreshed == nil || refreshed.Status != "interrupted" || refreshed.ActiveTurnID != "" {
			t.Fatalf("refreshed thread = %#v, want terminal interrupted without active turn", refreshed)
		}
	}
	state := loadTerminalGateState(t, service, ctx, terminalGateDeferKey(thread.ID, turnID))
	if state.LastDecision != string(terminalGateAccept) || state.LastReason != "grace_expired" {
		t.Fatalf("terminal gate state = %#v, want persistent accepted expiry", state)
	}
	service.live = stub
	service.liveConnected = true
	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "start after interruption")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || response.TurnID != "started-turn" || len(stub.turnSteerCalls) != 0 || len(stub.turnStartCalls) != 1 {
		t.Fatalf("response/calls = %#v steer=%#v start=%#v, want one new turn without steer", response, stub.turnSteerCalls, stub.turnStartCalls)
	}
}

func TestRefreshThreadForOperationTerminalCompletedToolReplacesLiveCurrent(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	turnID := "turn-refresh-terminal-tool"
	thread := model.Thread{
		ID:           "thread-refresh-terminal-tool",
		Title:        "Refresh terminal tool",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "active",
		ActiveTurnID: turnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	previous := appserver.CompactSnapshot(nil, appserver.ThreadReadSnapshot{
		Thread:                thread,
		LatestTurnID:          turnID,
		LatestTurnStatus:      "inProgress",
		LatestToolID:          "cmd-running",
		LatestToolKind:        "commandExecution",
		LatestToolLabel:       "sleep 10",
		LatestToolStatus:      "running",
		LatestToolFP:          "cmd-running-fp",
		LatestToolLiveCurrent: true,
		DetailItems: []model.DetailItem{
			{ID: "user-item", Kind: model.DetailItemUser, Text: "hello"},
			{ID: "cmd-running", Kind: model.DetailItemTool, Label: "sleep 10", Status: "running", FP: "cmd-running-fp"},
		},
	}, time.Now().UTC())
	if err := service.store.UpsertSnapshot(ctx, thread.ID, previous); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	if err := service.markTelegramOriginTurn(ctx, thread.ID, turnID); err != nil {
		t.Fatalf("markTelegramOriginTurn failed: %v", err)
	}
	stub := &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: diagnosticThreadReadPayloadWithFinal(thread, turnID, "completed", "OK_FINAL"),
		},
	}

	if _, err := service.refreshThreadForOperation(ctx, stub, thread.ID, "thread_read"); err != nil {
		t.Fatalf("refreshThreadForOperation failed: %v", err)
	}

	_, current, err := service.loadThreadPanelSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("loadThreadPanelSnapshot failed: %v", err)
	}
	if current.LatestToolLiveCurrent {
		t.Fatal("LatestToolLiveCurrent = true, want completed thread/read tool to replace live current")
	}
	if got := current.LatestToolLabel; !strings.Contains(got, "slow-command-done") {
		t.Fatalf("LatestToolLabel = %q, want completed thread/read tool", got)
	}
	if got := current.LatestToolStatus; got != "completed" {
		t.Fatalf("LatestToolStatus = %q, want completed", got)
	}
	if got := current.LatestTurnStatus; got != "completed" {
		t.Fatalf("LatestTurnStatus = %q, want completed", got)
	}
}

func TestPollTrackedSkipsThreadNotLoadedWithoutRepair(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	thread := model.Thread{
		ID:          "thread-not-loaded",
		Title:       "Not loaded",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		UpdatedAt:   time.Now().UTC().Unix(),
		Status:      "active",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.SetGlobalObserverTarget(ctx, 123456789, 0, true); err != nil {
		t.Fatalf("SetGlobalObserverTarget failed: %v", err)
	}
	service.poll = &stubSession{threadReadErr: errors.New("map[code:-32600 message:thread not loaded: thread-not-loaded]")}
	service.pollConnected = true

	service.pollTracked(ctx)

	repair, err := service.store.GetState(ctx, "control.repair_request")
	if err != nil {
		t.Fatalf("GetState(control.repair_request) failed: %v", err)
	}
	if strings.TrimSpace(repair) != "" {
		t.Fatalf("repair request = %q, want empty for thread not loaded", repair)
	}
	got := logs.String()
	requireLogContains(t, got, `"event":"thread_read_skipped"`)
	requireLogContains(t, got, `"reason":"thread_not_loaded"`)
	if strings.Contains(got, `"event":"repair_requested"`) {
		t.Fatalf("unexpected repair_requested log for thread not loaded: %s", got)
	}
}

func TestRefreshObserverIndexSyncsRecentThreadsWhenBackgroundObserverEnabled(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetGlobalObserverTarget(ctx, 123456789, 0, true); err != nil {
		t.Fatalf("SetGlobalObserverTarget failed: %v", err)
	}

	thread := model.Thread{
		ID:           "thread-from-list",
		Title:        "From list",
		ProjectName:  "Codex",
		CWD:          `C:\Users\you\Projects\Codex`,
		UpdatedAt:    time.Now().UTC().Unix(),
		Status:       "inProgress",
		ActiveTurnID: "turn-1",
	}
	service.poll = &stubSession{
		threadListResult: map[string]any{
			"threads": []any{
				map[string]any{
					"id":           thread.ID,
					"name":         thread.Title,
					"cwd":          thread.CWD,
					"updatedAt":    float64(thread.UpdatedAt),
					"status":       thread.Status,
					"activeTurnId": thread.ActiveTurnID,
				},
			},
		},
	}
	service.pollConnected = true

	service.refreshObserverIndex(ctx)

	stored, err := service.store.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if stored == nil {
		t.Fatal("expected thread from thread/list to be cached by refreshObserverIndex")
	}
	if stored.ID != thread.ID || stored.ActiveTurnID != thread.ActiveTurnID {
		t.Fatalf("stored thread = %#v, want id=%q activeTurn=%q", stored, thread.ID, thread.ActiveTurnID)
	}
}

func TestRefreshObserverIndexSkipsSyncWithoutBackgroundObserver(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetGlobalObserverTarget(ctx, 123456789, 0, false); err != nil {
		t.Fatalf("SetGlobalObserverTarget(disabled) failed: %v", err)
	}
	stub := &stubSession{}
	service.poll = stub
	service.pollConnected = true

	service.refreshObserverIndex(ctx)

	if stub.threadListCalls != 0 {
		t.Fatalf("thread list calls = %d, want 0 without background observer", stub.threadListCalls)
	}
}

func TestPlainReplyToSyntheticPlanPromptUsesTurnSteer(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()
	thread := model.Thread{ID: "synthetic-plan-thread", Title: "Synthetic", ProjectName: "Codex", CWD: `C:\Users\you\Projects\Codex`}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.PutMessageRoute(ctx, model.MessageRoute{
		ChatID:    123456789,
		TopicID:   0,
		MessageID: 777,
		ThreadID:  thread.ID,
		TurnID:    "turn-synthetic",
		EventID:   "synthetic-fp",
		CreatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("PutMessageRoute failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handlePlainText(ctx, 123456789, 0, "Use option A", 777)
	if err != nil {
		t.Fatalf("handlePlainText failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != "turn-synthetic" {
		t.Fatalf("response = %#v, want thread/turn synthetic", response)
	}
	if len(stub.turnSteerCalls) != 1 {
		t.Fatalf("turnSteerCalls = %#v, want one steer", stub.turnSteerCalls)
	}
	if got := stub.turnSteerCalls[0]; got.threadID != thread.ID || got.turnID != "turn-synthetic" || operatorPrompt(got.message) != "Use option A" {
		t.Fatalf("turn steer call = %#v, want synthetic plan answer", got)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no fallback start", stub.turnStartCalls)
	}
}

func TestPlainReplyToSyntheticPlanPromptFallsBackToTurnStart(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()
	thread := model.Thread{ID: "synthetic-stale-thread", Title: "Synthetic stale", ProjectName: "Codex", CWD: `C:\Users\you\Projects\Codex`}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.PutMessageRoute(ctx, model.MessageRoute{
		ChatID:    123456789,
		TopicID:   0,
		MessageID: 778,
		ThreadID:  thread.ID,
		TurnID:    "turn-stale",
		EventID:   "synthetic-fp-stale",
		CreatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("PutMessageRoute failed: %v", err)
	}
	stub := &stubSession{turnSteerErr: errors.New("turn already completed")}
	service.live = stub
	service.liveConnected = true

	response, err := service.handlePlainText(ctx, 123456789, 0, "Start new turn instead", 778)
	if err != nil {
		t.Fatalf("handlePlainText failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want fallback started turn", response)
	}
	if len(stub.turnSteerCalls) != 1 {
		t.Fatalf("turnSteerCalls = %#v, want one failed steer", stub.turnSteerCalls)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one fallback start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.threadID != thread.ID || operatorPrompt(got.message) != "Start new turn instead" {
		t.Fatalf("turn start call = %#v, want fallback answer", got)
	}
}

func TestReplyToActiveThreadSteersActiveTurn(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:           "active-reply-thread",
		Title:        "Active reply",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		Status:       "active",
		ActiveTurnID: "turn-active",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Add this while running")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != "turn-active" {
		t.Fatalf("response = %#v, want active turn steer", response)
	}
	if len(stub.turnSteerCalls) != 1 {
		t.Fatalf("turnSteerCalls = %#v, want one steer", stub.turnSteerCalls)
	}
	if got := stub.turnSteerCalls[0]; got.threadID != thread.ID || got.turnID != "turn-active" || operatorPrompt(got.message) != "Add this while running" {
		t.Fatalf("turn steer call = %#v, want active turn input", got)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no parallel start", stub.turnStartCalls)
	}
}

func TestStaleActiveThreadWithFinalAnswerStartsNewTurn(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:           "stale-active-final-thread",
		Title:        "Stale active final",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		Status:       "active",
		ActiveTurnID: "turn-stale",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{
		threadReads: map[string]map[string]any{
			thread.ID: {
				"id":           thread.ID,
				"name":         thread.Title,
				"cwd":          thread.CWD,
				"status":       "inProgress",
				"activeTurnId": "turn-stale",
				"turns": []any{
					map[string]any{
						"id":     "turn-stale",
						"status": "inProgress",
						"items": []any{
							map[string]any{
								"id":   "user-1",
								"type": "userMessage",
								"content": []any{
									map[string]any{"type": "text", "text": "Original request"},
								},
							},
							map[string]any{
								"id":    "final-1",
								"type":  "agentMessage",
								"phase": "final_answer",
								"text":  "Done.",
							},
						},
					},
				},
			},
		},
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Start after stale final")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want new started turn", response)
	}
	if len(stub.turnSteerCalls) != 0 {
		t.Fatalf("turnSteerCalls = %#v, want no stale steer", stub.turnSteerCalls)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one new turn", stub.turnStartCalls)
	}
	stored, err := service.store.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if stored == nil || stored.ActiveTurnID != "started-turn" || stored.Status != "inProgress" {
		t.Fatalf("stored thread = %#v, want seeded started turn", stored)
	}
	snapshot, err := service.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if snapshot == nil || snapshot.LastSeenTurnID != "started-turn" || snapshot.LastSeenTurnStatus != "inProgress" {
		t.Fatalf("snapshot = %#v, want seeded started turn", snapshot)
	}
}

func TestReplyToActiveThreadDoesNotFallbackToTurnStartWhenSteerFails(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:           "active-not-steerable-thread",
		Title:        "Active not steerable",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		Status:       "active",
		ActiveTurnID: "turn-active",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{turnSteerErr: errors.New("active turn is not steerable")}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThreadTurn(ctx, 123456789, 0, thread.ID, "turn-active", "Do not fork this", "")
	if err != nil {
		t.Fatalf("sendInputToThreadTurn failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "I did not start a parallel turn.") {
		t.Fatalf("response = %#v, want no parallel-turn warning", response)
	}
	if len(stub.turnSteerCalls) != 1 {
		t.Fatalf("turnSteerCalls = %#v, want one failed steer", stub.turnSteerCalls)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no fallback start", stub.turnStartCalls)
	}
}

func TestNoActiveTurnSteerFailureFallsBackToTurnStart(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte(projectAgentPolicyMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	thread := model.Thread{
		ID:           "stale-no-active-thread",
		Title:        "Stale no active",
		ProjectName:  "Codex",
		CWD:          project,
		Status:       "active",
		ActiveTurnID: "turn-stale",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: thread.ActiveTurnID, ChatID: 123456789, DeliveryProtocolVersion: 1, DeliveryNonce: "old-nonce"}); err != nil {
		t.Fatal(err)
	}
	idleThread := thread
	idleThread.Status = "completed"
	idleThread.ActiveTurnID = ""
	stub := &stubSession{
		threadReadQueue: map[string][]map[string]any{thread.ID: {
			diagnosticThreadReadPayload(thread, thread.ActiveTurnID, "inProgress"),
			diagnosticThreadReadPayload(idleThread, thread.ActiveTurnID, "completed"),
		}},
		turnSteerErr: errors.New("map[code:-32600 message:no active turn to steer]"),
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Start because stale active is gone")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want fallback started turn", response)
	}
	if len(stub.turnSteerCalls) != 1 {
		t.Fatalf("turnSteerCalls = %#v, want one failed active steer", stub.turnSteerCalls)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one fallback start", stub.turnStartCalls)
	}
	if !strings.Contains(stub.turnSteerCalls[0].message, `nonce "old-nonce"`) {
		t.Fatalf("legacy steer prompt = %q", stub.turnSteerCalls[0].message)
	}
	if !strings.Contains(stub.turnStartCalls[0].message, "file=v2") || strings.Contains(stub.turnStartCalls[0].message, "nonce") {
		t.Fatalf("fallback start prompt = %q", stub.turnStartCalls[0].message)
	}
	origin, err := service.store.GetTelegramTurnOrigin(ctx, thread.ID, "started-turn")
	if err != nil || origin == nil || origin.DeliveryProtocolVersion != 2 || origin.DeliveryNonce != "" {
		t.Fatalf("fallback origin = %#v, err = %v", origin, err)
	}
}

func TestNoActiveTurnSteerFailureRereadActiveTurnDoesNotStartParallel(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{ID: "stale-reread-active-thread", Title: "Stale reread active", ProjectName: "Codex", CWD: "/Users/example/project", Status: "active", ActiveTurnID: "turn-stale"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.armSteer(ctx, 123456789, 0, thread.ID, thread.ActiveTurnID, 0); err != nil {
		t.Fatal(err)
	}
	newThread := thread
	newThread.ActiveTurnID = "turn-new-active"
	newThread.Status = "active"
	stub := &stubSession{
		threadReadQueue: map[string][]map[string]any{thread.ID: {
			diagnosticThreadReadPayload(thread, thread.ActiveTurnID, "inProgress"),
			diagnosticThreadReadPayload(newThread, newThread.ActiveTurnID, "inProgress"),
		}},
		turnSteerErr: errors.New("map[code:-32600 message:no active turn to steer]"),
	}
	service.live = stub
	service.liveConnected = true
	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Use the reread active turn")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != newThread.ActiveTurnID {
		t.Fatalf("response = %#v, want reread active turn", response)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no new turn", stub.turnStartCalls)
	}
	state, err := service.store.GetSteerState(ctx, 123456789, 0)
	if err != nil || state != nil {
		t.Fatalf("steer state = %#v, err = %v; want cleared stale target", state, err)
	}
}

func TestExpiredArmedTurnUsesCurrentActiveTurnWithoutStarting(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{ID: "expired-armed-thread", CWD: "/Users/example/project", Status: "active", ActiveTurnID: "turn-current"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if err := service.armSteer(ctx, 123456789, 0, thread.ID, "turn-old", 0); err != nil {
		t.Fatal(err)
	}
	if err := service.saveTelegramOriginEmptyInterruptedDefer(ctx, thread.ID, "turn-old", terminalGateState{
		ThreadID: thread.ID, TurnID: "turn-old", LastDecision: string(terminalGateAccept), LastReason: "grace_expired",
		ExpiresAt: model.TimeString(time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)),
	}); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{threadReads: map[string]map[string]any{
		thread.ID: diagnosticThreadReadPayload(thread, thread.ActiveTurnID, "inProgress"),
	}}
	service.live = stub
	service.liveConnected = true
	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Steer the current turn")
	if err != nil || response == nil || response.TurnID != thread.ActiveTurnID {
		t.Fatalf("response = %#v, err = %v", response, err)
	}
	if len(stub.turnSteerCalls) != 1 || stub.turnSteerCalls[0].turnID != thread.ActiveTurnID || len(stub.turnStartCalls) != 0 {
		t.Fatalf("steer=%#v start=%#v", stub.turnSteerCalls, stub.turnStartCalls)
	}
}

func TestSteerTimeoutDoesNotStartParallelTurn(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{ID: "steer-timeout-thread", CWD: "/Users/example/project", Status: "active", ActiveTurnID: "turn-active"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{turnSteerErr: context.DeadlineExceeded}
	service.live = stub
	service.liveConnected = true
	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Do not duplicate this")
	if err != nil || response == nil || !strings.Contains(response.Text, "did not start a parallel turn") {
		t.Fatalf("response = %#v, err = %v", response, err)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want none", stub.turnStartCalls)
	}
}

func TestActiveTurnMismatchRetriesFoundTurn(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	oldTurnID := "01900000-0000-7000-8000-000000000101"
	foundTurnID := "01900000-0000-7000-8000-000000000102"
	thread := model.Thread{
		ID:           "active-mismatch-thread",
		Title:        "Active mismatch",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		Status:       "active",
		ActiveTurnID: oldTurnID,
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: oldTurnID, ChatID: 123456789, DeliveryProtocolVersion: 1, DeliveryNonce: "old-nonce"}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: foundTurnID, ChatID: 123456789, DeliveryProtocolVersion: 2}); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{
		turnSteerErrs: []error{
			fmt.Errorf("map[code:-32600 message:expected active turn id `%s` but found `%s`]", oldTurnID, foundTurnID),
			nil,
		},
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Steer authoritative active turn")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != foundTurnID {
		t.Fatalf("response = %#v, want retry steer turn", response)
	}
	if len(stub.turnSteerCalls) != 2 {
		t.Fatalf("turnSteerCalls = %#v, want old then found", stub.turnSteerCalls)
	}
	if got := stub.turnSteerCalls[0].turnID; got != oldTurnID {
		t.Fatalf("first steer turn = %q, want old", got)
	}
	if got := stub.turnSteerCalls[1].turnID; got != foundTurnID {
		t.Fatalf("second steer turn = %q, want found", got)
	}
	if !strings.Contains(stub.turnSteerCalls[0].message, `nonce "old-nonce"`) {
		t.Fatalf("first steer prompt = %q", stub.turnSteerCalls[0].message)
	}
	if !strings.Contains(stub.turnSteerCalls[1].message, "file=v2") || strings.Contains(stub.turnSteerCalls[1].message, "nonce") {
		t.Fatalf("retry steer prompt = %q", stub.turnSteerCalls[1].message)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no new parallel start", stub.turnStartCalls)
	}
}

func TestReplyToActiveThreadWithoutTurnIDDoesNotStartParallelTurn(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "active-without-turn-thread",
		Title:       "Active missing turn",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "active",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThread(ctx, 123456789, 0, thread.ID, "Do not start")
	if err != nil {
		t.Fatalf("sendInputToThread failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "active turn id is not available") {
		t.Fatalf("response = %#v, want missing active turn warning", response)
	}
	if len(stub.turnSteerCalls) != 0 {
		t.Fatalf("turnSteerCalls = %#v, want no steer without turn id", stub.turnSteerCalls)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no parallel start", stub.turnStartCalls)
	}
}

func TestPlanCommandStartsPlanCollaborationMode(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	if err := service.store.SetState(ctx, codexReasoningStateKey, "high"); err != nil {
		t.Fatalf("SetState(reasoning) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "plan-command-thread",
		Title:       "Plan command",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan "+thread.ID+" propose options", 0)
	if err != nil {
		t.Fatalf("handleCommand(/plan) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want started plan turn", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	got := stub.turnStartCalls[0]
	if got.collaborationMode != collaborationModePlan || got.model != "gpt-test" || got.reasoningEffort != "high" {
		t.Fatalf("turn start options = %#v, want plan/gpt-test/high", got)
	}
}

func TestPlanCommandUsesBoundThreadWhenNoExplicitThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "bound-plan-thread",
		Title:       "Bound plan",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, thread.ID, model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan propose options", 0)
	if err != nil {
		t.Fatalf("handleCommand(/plan text) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want bound plan turn", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	got := stub.turnStartCalls[0]
	if got.threadID != thread.ID || operatorPrompt(got.message) != "propose options" || got.collaborationMode != collaborationModePlan {
		t.Fatalf("turn start call = %#v, want bound plan prompt", got)
	}
}

func TestPlanCommandUnknownHeadUsesBoundThreadAsPromptText(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "bound-plan-thread-unknown-head",
		Title:       "Bound plan unknown head",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, thread.ID, model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan first second third", 0)
	if err != nil {
		t.Fatalf("handleCommand(/plan first second third) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want bound thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.threadID != thread.ID || operatorPrompt(got.message) != "first second third" {
		t.Fatalf("turn start call = %#v, want full prompt on bound thread", got)
	}
}

func TestPlanCommandUnknownHeadWithoutImplicitRouteShowsUsage(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan first second third", 0)
	if err != nil {
		t.Fatalf("handleCommand(/plan no route) failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "Usage: /plan <text>") {
		t.Fatalf("response = %#v, want /plan usage", response)
	}
	if len(stub.turnStartCalls) != 0 {
		t.Fatalf("turnStartCalls = %#v, want no explicit first-token start", stub.turnStartCalls)
	}
}

func TestPlanCommandUUIDLikeHeadStaysExplicit(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	bound := model.Thread{
		ID:          "bound-plan-thread-with-uuid-command",
		Title:       "Bound plan",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, bound); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, bound.ID, model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}
	explicitID := "01900000-0000-7000-8000-000000000999"

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan "+explicitID+" propose options", 0)
	if err != nil {
		t.Fatalf("handleCommand(/plan uuid text) failed: %v", err)
	}
	if response == nil || !strings.Contains(response.Text, "Unknown thread: "+explicitID) {
		t.Fatalf("response = %#v, want explicit unknown UUID-like thread", response)
	}
}

func TestPlanCommandKnownThreadHeadStaysExplicit(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	bound := model.Thread{
		ID:          "bound-plan-thread-with-known-command",
		Title:       "Bound plan",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	explicit := model.Thread{
		ID:          "explicit-plan-thread",
		Title:       "Explicit plan",
		ProjectName: "Codex",
		CWD:         "/Users/example/other-project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, bound); err != nil {
		t.Fatalf("UpsertThread(bound) failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, explicit); err != nil {
		t.Fatalf("UpsertThread(explicit) failed: %v", err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, bound.ID, model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan "+explicit.ID+" propose options", 0)
	if err != nil {
		t.Fatalf("handleCommand(/plan known-thread text) failed: %v", err)
	}
	if response == nil || response.ThreadID != explicit.ID {
		t.Fatalf("response = %#v, want explicit thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.threadID != explicit.ID || operatorPrompt(got.message) != "propose options" {
		t.Fatalf("turn start call = %#v, want explicit plan prompt", got)
	}
}

func TestTelegramTurnLifecycleLogsSuccessfulStart(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "diag-success-thread",
		Title:       "Diagnostics",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{threadReads: map[string]map[string]any{
		thread.ID: diagnosticThreadReadPayload(thread, "existing-turn", "completed"),
	}}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThreadTurn(ctx, 123456789, 0, thread.ID, "", "keep this prompt private", collaborationModePlan)
	if err != nil {
		t.Fatalf("sendInputToThreadTurn failed: %v", err)
	}
	if response == nil || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want started-turn", response)
	}
	got := logs.String()
	requireLogContains(t, got, `"event":"telegram_turn_input_start"`)
	requireLogContains(t, got, `"method":"ThreadResume"`)
	requireLogContains(t, got, `"method":"TurnStart"`)
	requireLogContains(t, got, `"event":"telegram_origin_turn_marked"`)
	requireLogContains(t, got, `"collaboration_mode":"plan"`)
	requireLogContains(t, got, `"model":"gpt-test"`)
	requireLogContains(t, got, `"text_len":24`)
	requireLogContains(t, got, `"text_sha256"`)
	if strings.Contains(got, "keep this prompt private") {
		t.Fatalf("diagnostic log leaked prompt body: %s", got)
	}
}

func TestSnapshotHasPassiveChangeAllowsTerminalFinalAfterInterrupted(t *testing.T) {
	t.Parallel()

	previous := &model.ThreadSnapshotState{
		LastSeenTurnID:     "turn-terminal",
		LastSeenTurnStatus: "interrupted",
		LastCompletionFP:   "old-interrupted-fp",
	}
	current := &appserver.ThreadReadSnapshot{
		Thread: model.Thread{
			ID:          "thread-terminal",
			Title:       "Terminal correction",
			ProjectName: "Codex",
			Status:      "idle",
		},
		LatestTurnID:     "turn-terminal",
		LatestTurnStatus: "completed",
		LatestFinalText:  "Done.",
		LatestFinalFP:    "final-fp",
	}

	if !snapshotHasPassiveChange(previous, current) {
		t.Fatal("snapshotHasPassiveChange = false, want final correction after interrupted terminal state")
	}
}

func TestSnapshotHasPassiveChangeIgnoresRepeatedTerminalSnapshot(t *testing.T) {
	t.Parallel()

	current := appserver.ThreadReadSnapshot{
		Thread: model.Thread{
			ID:          "thread-terminal-repeat",
			Title:       "Terminal repeat",
			ProjectName: "Codex",
			Status:      "idle",
		},
		LatestTurnID:     "turn-terminal-repeat",
		LatestTurnStatus: "completed",
		LatestFinalText:  "Done.",
		LatestFinalFP:    "final-fp-repeat",
	}
	previous := appserver.CompactSnapshot(nil, current, time.Now().UTC())

	if snapshotHasPassiveChange(&previous, &current) {
		t.Fatal("snapshotHasPassiveChange = true, want repeated terminal snapshot ignored")
	}
}

func TestTelegramTurnLifecycleLogsThreadResumeFailure(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	thread := model.Thread{ID: "diag-resume-fail", Title: "Diagnostics", CWD: "/Users/example/project", Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{threadResumeErr: errors.New("resume failed")}
	service.live = stub
	service.liveConnected = true

	_, err := service.sendInputToThreadTurn(ctx, 123456789, 0, thread.ID, "", "hello", "")
	if err == nil {
		t.Fatal("sendInputToThreadTurn succeeded, want resume failure")
	}
	got := logs.String()
	requireLogContains(t, got, `"method":"ThreadResume"`)
	requireLogContains(t, got, `"outcome":"error"`)
	requireLogContains(t, got, `"error":"resume failed"`)
}

func TestTelegramTurnLifecycleLogsTurnStartFailure(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	thread := model.Thread{ID: "diag-turn-start-fail", Title: "Diagnostics", CWD: "/Users/example/project", Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{turnStartErr: errors.New("start failed")}
	service.live = stub
	service.liveConnected = true

	_, err := service.sendInputToThreadTurn(ctx, 123456789, 0, thread.ID, "", "hello", "")
	if err == nil {
		t.Fatal("sendInputToThreadTurn succeeded, want turn start failure")
	}
	got := logs.String()
	requireLogContains(t, got, `"method":"ThreadResume"`)
	requireLogContains(t, got, `"method":"TurnStart"`)
	requireLogContains(t, got, `"outcome":"error"`)
	requireLogContains(t, got, `"error":"start failed"`)
}

func TestTelegramTurnLifecycleLogsRefreshFailuresAroundStart(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	thread := model.Thread{ID: "diag-refresh-fail", Title: "Diagnostics", CWD: "/Users/example/project", Status: "idle"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{threadReadErr: errors.New("thread read failed")}
	service.live = stub
	service.liveConnected = true

	response, err := service.sendInputToThreadTurn(ctx, 123456789, 0, thread.ID, "", "hello", "")
	if err != nil {
		t.Fatalf("sendInputToThreadTurn failed: %v", err)
	}
	if response == nil || response.TurnID != "started-turn" {
		t.Fatalf("response = %#v, want started-turn despite refresh failures", response)
	}
	got := logs.String()
	requireLogContains(t, got, `"operation":"refresh_thread_before_start"`)
	requireLogContains(t, got, `"operation":"refresh_thread_after_start"`)
	requireLogContains(t, got, `"event":"thread_refresh_failed"`)
	requireLogContains(t, got, `"method":"TurnStart"`)
}

func TestLiveEventLoopExitRecordsRepairReason(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	ch := make(chan appserver.Event)
	close(ch)
	live := &stubSession{}
	service.live = live
	service.liveEvents = ch
	service.liveConnected = true

	service.liveEventLoop(ctx, live, ch, 0)

	value, err := service.store.GetState(ctx, "repair.last_reason")
	if err != nil {
		t.Fatalf("GetState(repair.last_reason) failed: %v", err)
	}
	if value != "live_event_loop_closed" {
		t.Fatalf("repair.last_reason = %q, want live_event_loop_closed", value)
	}
	got := logs.String()
	requireLogContains(t, got, `"event":"appserver_live_event_loop_closed"`)
	requireLogContains(t, got, `"event":"repair_requested"`)
}

func TestEnsureSessionsSuppressesDuplicateConcurrentStarts(t *testing.T) {
	service := newTestService(t)
	service.cfg.RequestTimeout = 2 * time.Second
	live := newStartCountingSession()
	poll := newStartCountingSession()
	service.live = live
	service.poll = poll

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if index%2 == 0 {
				service.ensureSessions(ctx)
				return
			}
			service.reconcileSessions(ctx)
		}(i)
	}

	live.waitStarted(t, "live")
	live.release()
	poll.waitStarted(t, "poll")
	poll.release()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent ensure/reconcile did not finish")
	}
	if got := live.StartCalls(); got != 1 {
		t.Fatalf("live Start calls = %d, want 1", got)
	}
	if got := poll.StartCalls(); got != 1 {
		t.Fatalf("poll Start calls = %d, want 1", got)
	}
}

func TestStaleLiveEventLoopDoesNotClearNewLiveState(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	oldLive := &stubSession{}
	oldEvents := make(chan appserver.Event)
	newLive := &stubSession{}
	newEvents := make(chan appserver.Event)

	service.mu.Lock()
	service.live = oldLive
	service.liveEvents = oldEvents
	service.liveConnected = true
	service.liveGeneration = 1
	service.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		service.liveEventLoop(ctx, oldLive, oldEvents, 1)
	}()

	service.mu.Lock()
	service.live = newLive
	service.liveEvents = newEvents
	service.liveConnected = true
	service.liveGeneration = 2
	service.mu.Unlock()
	close(oldEvents)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old live event loop did not exit")
	}
	service.mu.RLock()
	currentLive := service.live
	currentEvents := service.liveEvents
	currentGeneration := service.liveGeneration
	liveConnected := service.liveConnected
	service.mu.RUnlock()
	if !liveConnected || currentLive != newLive || currentEvents != newEvents || currentGeneration != 2 {
		t.Fatalf("new live state was disturbed: connected=%t live=%p events_match=%t generation=%d", liveConnected, currentLive, currentEvents == newEvents, currentGeneration)
	}
	value, err := service.store.GetState(ctx, "control.repair_request")
	if err != nil {
		t.Fatalf("GetState(control.repair_request) failed: %v", err)
	}
	if strings.TrimSpace(value) != "" {
		t.Fatalf("repair request = %q, want empty for stale loop", value)
	}
	requireLogContains(t, logs.String(), `"event":"appserver_live_event_loop_stale"`)
}

func TestTransportErrorDiagnosticSanitizesPrivateFields(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	logs := captureServiceLogs(service)
	stub := &stubSession{stderrTail: []string{
		"token=supersecret12345 in /Users/example/private/session.sock",
	}}

	service.handleLiveEvent(ctx, stub, appserver.Event{
		Channel: "transport_error",
		Params: map[string]any{
			"error": "secret=abc123456789 at /Users/example/private/state.sqlite",
		},
	})

	got := logs.String()
	requireLogContains(t, got, `"event":"appserver_transport_error"`)
	requireLogContains(t, got, `redacted`)
	if strings.Contains(got, "abc123456789") || strings.Contains(got, "supersecret12345") || strings.Contains(got, ".sock") || strings.Contains(got, ".sqlite") {
		t.Fatalf("diagnostic log leaked private data: %s", got)
	}
}

func TestDiagnosticLogsAreRateLimited(t *testing.T) {
	service := newTestService(t)
	logs := captureServiceLogs(service)

	for i := 0; i < 300; i++ {
		service.logLifecycle("looping_event", lifecycleFields{"index": i})
	}

	lineCount := strings.Count(strings.TrimSpace(logs.String()), "\n")
	if strings.TrimSpace(logs.String()) != "" {
		lineCount++
	}
	if lineCount > diagnosticEventLimit("looping_event") {
		t.Fatalf("diagnostic log lines = %d, want <= %d", lineCount, diagnosticEventLimit("looping_event"))
	}
}

func TestDiagnosticLoggerCanBeDisabled(t *testing.T) {
	service := newTestService(t)
	logs := captureServiceLogs(service)

	service.logLifecycle("enabled_event", lifecycleFields{"value": "before"})
	requireLogContains(t, logs.String(), `"event":"enabled_event"`)

	service.SetLogger(nil)
	service.logLifecycle("disabled_event", lifecycleFields{"value": "after"})
	if got := logs.String(); strings.Contains(got, `"event":"disabled_event"`) {
		t.Fatalf("disabled diagnostic log was written: %s", got)
	}
}

func TestObserverSyncResultLogsAreDebounced(t *testing.T) {
	service := newTestService(t)
	logs := captureServiceLogs(service)
	snapshot := appserver.ThreadReadSnapshot{
		Thread: model.Thread{
			ID:          "thread-observer-debounce",
			Title:       "Observer debounce",
			ProjectName: "Codex",
			Status:      "idle",
		},
		LatestTurnID:     "turn-observer-debounce",
		LatestTurnStatus: "interrupted",
		DetailItems: []model.DetailItem{
			{Kind: model.DetailItemCommentary, Text: "Working."},
		},
	}

	for i := 0; i < 10; i++ {
		snapshot.DetailItems = append(snapshot.DetailItems, model.DetailItem{Kind: model.DetailItemTool, Text: "tool"})
		service.logObserverSyncResult("thread_read", snapshot)
	}

	got := logs.String()
	if count := strings.Count(got, `"event":"observer_sync_result"`); count != 1 {
		t.Fatalf("observer_sync_result logs = %d, want 1; logs:\n%s", count, got)
	}
	requireLogContains(t, got, `"thread_id":"thread-observer-debounce"`)
}

func TestGenericThreadReadDiagnosticsAreDebounced(t *testing.T) {
	service := newTestService(t)
	logs := captureServiceLogs(service)

	for i := 0; i < 10; i++ {
		service.logAppServerCall("ThreadRead", time.Now(), nil, &stubSession{}, lifecycleFields{
			"operation":     "thread_read",
			"thread_id":     "thread-read-debounce",
			"include_turns": true,
		})
	}

	got := logs.String()
	if count := strings.Count(got, `"event":"appserver_call"`); count != 1 {
		t.Fatalf("appserver_call logs = %d, want 1; logs:\n%s", count, got)
	}
	requireLogContains(t, got, `"method":"ThreadRead"`)
	requireLogContains(t, got, `"thread_id":"thread-read-debounce"`)
}

func TestThreadReadSkippedLogsAreDebounced(t *testing.T) {
	service := newTestService(t)
	logs := captureServiceLogs(service)

	for i := 0; i < 10; i++ {
		service.logThreadReadSkipped("thread-1", "thread_not_loaded")
	}
	service.logThreadReadSkipped("thread-2", "thread_not_loaded")

	got := logs.String()
	if count := strings.Count(got, `"event":"thread_read_skipped"`); count != 2 {
		t.Fatalf("thread_read_skipped logs = %d, want 2; logs:\n%s", count, got)
	}
	requireLogContains(t, got, `"thread_id":"thread-1"`)
	requireLogContains(t, got, `"thread_id":"thread-2"`)
	requireLogContains(t, got, `"debounce":"10m0s"`)
}

func TestReplyPlanFlagStartsPlanCollaborationMode(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	if err := service.store.SetState(ctx, codexReasoningStateKey, "medium"); err != nil {
		t.Fatalf("SetState(reasoning) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "reply-plan-thread",
		Title:       "Reply plan",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/reply --plan "+thread.ID+" sketch the plan", 0)
	if err != nil {
		t.Fatalf("handleCommand(/reply --plan) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want reply plan thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.collaborationMode != collaborationModePlan || operatorPrompt(got.message) != "sketch the plan" {
		t.Fatalf("turn start call = %#v, want plan input", got)
	}
}

func TestReplyDefaultFlagStartsDefaultCollaborationMode(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	if err := service.store.SetState(ctx, codexReasoningStateKey, "medium"); err != nil {
		t.Fatalf("SetState(reasoning) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "reply-default-thread",
		Title:       "Reply default",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/reply --default "+thread.ID+" do the work", 0)
	if err != nil {
		t.Fatalf("handleCommand(/reply --default) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want reply default thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.collaborationMode != collaborationModeDefault || operatorPrompt(got.message) != "do the work" {
		t.Fatalf("turn start call = %#v, want default input", got)
	}
}

func TestDefaultModeCommandStartsDefaultCollaborationMode(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "default-command-thread",
		Title:       "Default command",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/default "+thread.ID+" do the work", 0)
	if err != nil {
		t.Fatalf("handleCommand(/default) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want default command thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.threadID != thread.ID || got.collaborationMode != collaborationModeDefault || operatorPrompt(got.message) != "do the work" {
		t.Fatalf("turn start call = %#v, want default-mode command", got)
	}
}

func TestHelpHidesDefaultModeFallback(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	response, err := service.handleCommand(context.Background(), 123456789, 0, "/help", 0)
	if err != nil {
		t.Fatalf("handleCommand(/help) failed: %v", err)
	}
	if response == nil {
		t.Fatal("handleCommand(/help) returned nil response")
	}
	if strings.Contains(response.Text, "/default") || strings.Contains(response.Text, "--default") {
		t.Fatalf("/help text exposes hidden default fallback:\n%s", response.Text)
	}
	if !strings.Contains(response.Text, "/plan") || !strings.Contains(response.Text, "/reply [--plan]") {
		t.Fatalf("/help text = %q, want public plan/reply commands", response.Text)
	}
	if !strings.Contains(response.Text, "/agent model <model-id|sol|luna|astra> [effort]") {
		t.Fatalf("/help text = %q, want model-neutral Lead command", response.Text)
	}
}

func TestControlThreadListAndReadUsePollSession(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	stub := &stubSession{
		threadListResult: map[string]any{"threads": []any{map[string]any{"id": "thread-1"}}},
		threadReads: map[string]map[string]any{
			"thread-1": {"thread": map[string]any{"id": "thread-1"}},
		},
	}
	service.poll = stub
	service.pollConnected = true

	list, err := service.ControlThreadList(ctx, 25, "next")
	if err != nil {
		t.Fatalf("ControlThreadList failed: %v", err)
	}
	if list == nil || stub.threadListCalls != 1 || stub.threadListLimit != 25 || stub.threadListCursor != "next" {
		t.Fatalf("list=%#v calls=%d limit=%d cursor=%q", list, stub.threadListCalls, stub.threadListLimit, stub.threadListCursor)
	}
	read, err := service.ControlThreadRead(ctx, "thread-1", true)
	if err != nil {
		t.Fatalf("ControlThreadRead failed: %v", err)
	}
	if read == nil || stub.threadReadID != "thread-1" || !stub.threadReadIncludeTurns {
		t.Fatalf("read=%#v id=%q include=%t", read, stub.threadReadID, stub.threadReadIncludeTurns)
	}
}

func TestStopSetsDefaultOverrideForActiveThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:           "stop-active-default-thread",
		Title:        "Stop active default",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		Status:       "active",
		ActiveTurnID: "turn-active",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/stop "+thread.ID, 0)
	if err != nil {
		t.Fatalf("handleCommand(/stop) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID || response.TurnID != thread.ActiveTurnID {
		t.Fatalf("response = %#v, want active stop response", response)
	}
	if strings.TrimSpace(response.Text) == "" {
		t.Fatalf("response = %#v, want visible /stop command text", response)
	}
	if got := service.threadCollaborationOverride(ctx, thread.ID); got != collaborationModeDefault {
		t.Fatalf("threadCollaborationOverride = %q, want default after /stop", got)
	}
}

func TestStopSetsDefaultOverrideForIdleThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "stop-idle-default-thread",
		Title:       "Stop idle default",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/stop "+thread.ID, 0)
	if err != nil {
		t.Fatalf("handleCommand(/stop idle) failed: %v", err)
	}
	if response == nil || !strings.Contains(response.CallbackText, "already idle") {
		t.Fatalf("response = %#v, want idle stop response", response)
	}
	if !strings.Contains(response.Text, "already idle") {
		t.Fatalf("response = %#v, want visible idle /stop command text", response)
	}
	if got := service.threadCollaborationOverride(ctx, thread.ID); got != collaborationModeDefault {
		t.Fatalf("threadCollaborationOverride = %q, want default after idle /stop", got)
	}
}

func TestStopTreatsCompletedThreadWithStaleActiveTurnAsIdle(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:           "stop-completed-stale-active-thread",
		Title:        "Stop completed stale active",
		ProjectName:  "Codex",
		CWD:          "/Users/example/project",
		Status:       "completed",
		ActiveTurnID: "stale-completed-turn",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/stop "+thread.ID, 0)
	if err != nil {
		t.Fatalf("handleCommand(/stop stale completed) failed: %v", err)
	}
	if response == nil || !strings.Contains(response.CallbackText, "already idle") {
		t.Fatalf("response = %#v, want idle stop response", response)
	}
	if !strings.Contains(response.Text, "already idle") {
		t.Fatalf("response = %#v, want visible idle /stop command text", response)
	}
	if len(stub.turnInterruptCalls) != 0 {
		t.Fatalf("turnInterruptCalls = %#v, want no interrupt for completed thread", stub.turnInterruptCalls)
	}
	if got := service.threadCollaborationOverride(ctx, thread.ID); got != collaborationModeDefault {
		t.Fatalf("threadCollaborationOverride = %q, want default after stale completed /stop", got)
	}
}

func TestStopInterruptFailureDoesNotSetDefaultOverride(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{ID: "stop-failed-default-thread", Title: "Stop failed default", ProjectName: "Codex", CWD: "/Users/example/project", Status: "active", ActiveTurnID: "turn-active"}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{turnInterruptErr: errors.New("interrupt failed")}
	service.live = stub
	service.liveConnected = true
	if _, err := service.handleCommand(ctx, 123456789, 0, "/stop "+thread.ID, 0); err == nil {
		t.Fatal("handleCommand(/stop) succeeded despite interrupt failure")
	}
	if got := service.threadCollaborationOverride(ctx, thread.ID); got != "" {
		t.Fatalf("threadCollaborationOverride = %q, want empty after failed interrupt", got)
	}
}

func TestPlanModeCommandCanRouteByReply(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "reply-routed-plan-thread",
		Title:       "Reply routed plan",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.PutMessageRoute(ctx, model.MessageRoute{
		ChatID:    123456789,
		TopicID:   0,
		MessageID: 812,
		ThreadID:  thread.ID,
		CreatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("PutMessageRoute failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan plan this reply-routed task", 812)
	if err != nil {
		t.Fatalf("handleCommand(/plan) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want routed thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	got := stub.turnStartCalls[0]
	if got.collaborationMode != collaborationModePlan || operatorPrompt(got.message) != "plan this reply-routed task" {
		t.Fatalf("turn start call = %#v, want reply-routed plan text", got)
	}
}

func TestContextCardBoundThreadIncludesFullThreadID(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "full-context-thread-id",
		Title:       "Context title",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.store.SetBinding(ctx, 123456789, 0, thread.ID, model.BindingModeBound); err != nil {
		t.Fatalf("SetBinding failed: %v", err)
	}

	text, err := service.contextCard(ctx, 123456789, 0)
	if err != nil {
		t.Fatalf("contextCard failed: %v", err)
	}
	for _, want := range []string{
		"Mode: Bound thread",
		"Thread: [Codex] Context title",
		"Thread ID: full-context-thread-id",
		"CWD: /Users/example/project",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("context card missing %q in:\n%s", want, text)
		}
	}
}

func TestSummaryPanelGetThreadIDButtonSendsCopyableIDs(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "summary-thread-full-id",
		Title:       "Summary",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "active",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:             thread,
		LatestTurnID:       "summary-turn-full-id",
		LatestTurnStatus:   "inProgress",
		LatestProgressText: "Working",
		LatestProgressFP:   "progress-fp",
	}

	_, buttons, _ := service.renderSummaryPanel(ctx, thread, snapshot, nil)
	token := callbackTokenForButton(buttons, "Get thread id")
	if token == "" {
		t.Fatalf("Get thread id button not found in %#v", buttons)
	}

	response, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, token)
	if err != nil {
		t.Fatalf("HandleCallback(get_thread_id) failed: %v", err)
	}
	if response == nil || response.Text != "Thread ID:\nsummary-thread-full-id\n\nTurn ID:\nsummary-turn-full-id" {
		t.Fatalf("response = %#v, want copyable thread/turn ids", response)
	}
	if response.ThreadID != thread.ID || response.TurnID != "summary-turn-full-id" {
		t.Fatalf("response route = thread %q turn %q, want full ids", response.ThreadID, response.TurnID)
	}
}

func TestFinalSummaryPanelHasGetThreadIDButton(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "final-thread-full-id",
		Title:       "Final",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "final-turn-full-id",
		LatestTurnStatus: "completed",
		LatestFinalText:  "Done.",
		LatestFinalFP:    "final-fp",
	}

	_, buttons, _ := service.renderSummaryPanel(ctx, thread, snapshot, nil)
	if token := callbackTokenForButton(buttons, "Get thread id"); token == "" {
		t.Fatalf("Get thread id button not found in final summary buttons %#v", buttons)
	}
}

func TestFinalCardGetThreadIDButtonSendsCopyableIDs(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "final-card-thread-full-id",
		Title:       "Final card",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "final-card-turn-full-id",
		LatestTurnStatus: "completed",
		LatestFinalText:  "Done.",
		LatestFinalFP:    "final-card-fp",
	}

	_, buttons, _ := service.renderFinalCard(ctx, 42, thread, snapshot)
	token := callbackTokenForButton(buttons, "Get thread id")
	if token == "" {
		t.Fatalf("Get thread id button not found in final card buttons %#v", buttons)
	}

	response, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, token)
	if err != nil {
		t.Fatalf("HandleCallback(final get_thread_id) failed: %v", err)
	}
	if response == nil || response.Text != "Thread ID:\nfinal-card-thread-full-id\n\nTurn ID:\nfinal-card-turn-full-id" {
		t.Fatalf("response = %#v, want copyable final card ids", response)
	}
}

func TestFinalCardShowsRunDuration(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "final-duration-thread",
		Title:       "Final duration",
		ProjectName: "Codex",
		Status:      "idle",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:              thread,
		LatestTurnID:        "final-duration-turn",
		LatestTurnStatus:    "completed",
		LatestTurnStartedAt: "2026-05-02T12:00:00Z",
		LatestTurnUpdatedAt: "2026-05-02T12:01:12Z",
		LatestFinalText:     "Done.",
		LatestFinalFP:       "final-duration-fp",
	}

	message, _, _ := service.renderFinalCard(ctx, 42, thread, snapshot)
	if !strings.Contains(message.Text, "Done.") {
		t.Fatalf("final card = %q, want final answer", message.Text)
	}
	if !strings.Contains(message.Text, "Run duration: 1m 12s") {
		t.Fatalf("final card = %q, want run duration footer", message.Text)
	}
}

func TestPlanFinalCardShowsTurnOffPlanButton(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "final-plan-thread",
		Title:       "Final plan",
		ProjectName: "Codex",
		Status:      "idle",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "final-plan-turn",
		LatestTurnStatus: "completed",
		LatestFinalText:  "Plan mode final.",
		LatestFinalFP:    "final-plan-fp",
		DetailItems: []model.DetailItem{
			{ID: "plan-1", Kind: model.DetailItemPlan, Text: "Plan text.", CommentaryIndex: 1},
			{ID: "final-1", Kind: model.DetailItemFinal, Text: "Plan mode final.", CommentaryIndex: 1},
		},
	}

	_, buttons, _ := service.renderFinalCard(ctx, 42, thread, snapshot)
	if token := callbackTokenForButton(buttons, "Turn off Plan"); token == "" {
		t.Fatalf("Turn off Plan button not found in final card buttons %#v", buttons)
	}
}

func TestPlanFinalCardShowsTurnOffPlanButtonFromLocalMarker(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "final-plan-marker-thread",
		Title:       "Final plan marker",
		ProjectName: "Codex",
		Status:      "idle",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "final-plan-marker-turn",
		LatestTurnStatus: "completed",
		LatestFinalText:  "Plan mode final.",
		LatestFinalFP:    "final-plan-marker-fp",
		DetailItems: []model.DetailItem{
			{ID: "final-1", Kind: model.DetailItemFinal, Text: "Plan mode final."},
		},
	}
	if err := service.setThreadCollaborationMarker(ctx, thread.ID, snapshot.LatestTurnID, collaborationModePlan); err != nil {
		t.Fatalf("setThreadCollaborationMarker failed: %v", err)
	}

	_, buttons, _ := service.renderFinalCard(ctx, 42, thread, snapshot)
	if token := callbackTokenForButton(buttons, "Turn off Plan"); token == "" {
		t.Fatalf("Turn off Plan button not found in marker-based final card buttons %#v", buttons)
	}
}

func TestNormalFinalCardDoesNotShowTurnOffPlanButton(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{
		ID:          "final-normal-thread",
		Title:       "Final normal",
		ProjectName: "Codex",
		Status:      "idle",
	}
	snapshot := &appserver.ThreadReadSnapshot{
		Thread:           thread,
		LatestTurnID:     "final-normal-turn",
		LatestTurnStatus: "completed",
		LatestFinalText:  "Done.",
		LatestFinalFP:    "final-normal-fp",
		DetailItems: []model.DetailItem{
			{ID: "final-1", Kind: model.DetailItemFinal, Text: "Done."},
		},
	}

	_, buttons, _ := service.renderFinalCard(ctx, 42, thread, snapshot)
	if token := callbackTokenForButton(buttons, "Turn off Plan"); token != "" {
		t.Fatalf("Turn off Plan button token = %q, want absent in non-plan final buttons %#v", token, buttons)
	}
}

func TestReplyCommandKeepsDefaultCollaborationMode(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	if err := service.store.SetState(ctx, codexReasoningStateKey, "high"); err != nil {
		t.Fatalf("SetState(reasoning) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "plain-reply-thread",
		Title:       "Plain reply",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/reply "+thread.ID+" do the work", 0)
	if err != nil {
		t.Fatalf("handleCommand(/reply) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want reply thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.collaborationMode != "" {
		t.Fatalf("collaborationMode = %q, want empty default turn", got.collaborationMode)
	}
}

func TestReplyCommandConsumesDefaultOverrideOnce(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "plain-reply-default-override-thread",
		Title:       "Plain reply default override",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.setThreadCollaborationDefaultOverride(ctx, thread.ID); err != nil {
		t.Fatalf("setThreadCollaborationDefaultOverride failed: %v", err)
	}
	if err := service.setThreadCollaborationMarker(ctx, thread.ID, "old-plan-turn", collaborationModePlan); err != nil {
		t.Fatalf("setThreadCollaborationMarker failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/reply "+thread.ID+" do the work", 0)
	if err != nil {
		t.Fatalf("handleCommand(/reply) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want reply thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.collaborationMode != collaborationModeDefault {
		t.Fatalf("collaborationMode = %q, want default override", got.collaborationMode)
	}
	if got := service.threadCollaborationOverride(ctx, thread.ID); got != "" {
		t.Fatalf("threadCollaborationOverride = %q, want cleared after successful start", got)
	}
	if got := service.threadCollaborationMarker(ctx, thread.ID, "started-turn"); got != "" {
		t.Fatalf("threadCollaborationMarker = %q, want cleared after default override start", got)
	}
}

func TestDefaultOverrideSurvivesTurnStartFailure(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "default-override-failure-thread",
		Title:       "Default override failure",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.setThreadCollaborationDefaultOverride(ctx, thread.ID); err != nil {
		t.Fatalf("setThreadCollaborationDefaultOverride failed: %v", err)
	}
	stub := &stubSession{turnStartErr: errors.New("turn start failed")}
	service.live = stub
	service.liveConnected = true

	_, err := service.handleCommand(ctx, 123456789, 0, "/reply "+thread.ID+" do the work", 0)
	if err == nil {
		t.Fatal("handleCommand(/reply) succeeded, want turn start error")
	}
	if got := service.threadCollaborationOverride(ctx, thread.ID); got != collaborationModeDefault {
		t.Fatalf("threadCollaborationOverride = %q, want default retained after failed start", got)
	}
}

func TestPlanCommandClearsStaleDefaultOverride(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-test"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	thread := model.Thread{
		ID:          "plan-clears-default-override-thread",
		Title:       "Plan clears default override",
		ProjectName: "Codex",
		CWD:         "/Users/example/project",
		Status:      "idle",
	}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	if err := service.setThreadCollaborationDefaultOverride(ctx, thread.ID); err != nil {
		t.Fatalf("setThreadCollaborationDefaultOverride failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/plan "+thread.ID+" propose options", 0)
	if err != nil {
		t.Fatalf("handleCommand(/plan) failed: %v", err)
	}
	if response == nil || response.ThreadID != thread.ID {
		t.Fatalf("response = %#v, want plan thread", response)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one start", stub.turnStartCalls)
	}
	if got := stub.turnStartCalls[0]; got.collaborationMode != collaborationModePlan {
		t.Fatalf("collaborationMode = %q, want plan", got.collaborationMode)
	}
	if got := service.threadCollaborationOverride(ctx, thread.ID); got != "" {
		t.Fatalf("threadCollaborationOverride = %q, want cleared after explicit plan start", got)
	}
	if got := service.threadCollaborationMarker(ctx, thread.ID, "started-turn"); got != collaborationModePlan {
		t.Fatalf("threadCollaborationMarker = %q, want plan after explicit plan start", got)
	}
}

func TestModelMenuPersistsSelectedModel(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	stub := &stubSession{models: []appserver.ModelOption{
		{ID: "gpt-default", IsDefault: true, SupportedReasoningEffort: []string{"low", "medium"}},
		{ID: "gpt-menu", SupportedReasoningEffort: []string{"minimal", "low"}},
	}}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/model", 0)
	if err != nil {
		t.Fatalf("handleCommand(/model) failed: %v", err)
	}
	token := callbackTokenForButton(response.Buttons, "gpt-menu")
	if token == "" {
		t.Fatalf("model menu buttons = %#v, want gpt-menu", response.Buttons)
	}
	callbackResponse, err := service.HandleCallback(ctx, 123456789, 0, 900, 123456789, token)
	if err != nil {
		t.Fatalf("HandleCallback(model select) failed: %v", err)
	}
	if callbackResponse == nil || !strings.Contains(callbackResponse.Text, "Model saved.") {
		t.Fatalf("callback response = %#v, want saved settings summary", callbackResponse)
	}
	if len(callbackResponse.Buttons) != 0 {
		t.Fatalf("callback buttons = %#v, want choice buttons removed after selection", callbackResponse.Buttons)
	}
	value, err := service.store.GetState(ctx, codexModelStateKey)
	if err != nil {
		t.Fatalf("GetState(model) failed: %v", err)
	}
	if value != "gpt-menu" {
		t.Fatalf("stored model = %q, want gpt-menu", value)
	}
}

func TestReasoningMenuUsesSelectedModelEfforts(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-menu"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	stub := &stubSession{models: []appserver.ModelOption{
		{ID: "gpt-default", IsDefault: true, SupportedReasoningEffort: []string{"low", "medium", "high"}},
		{ID: "gpt-menu", SupportedReasoningEffort: []string{"minimal", "low"}},
	}}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 0, "/effort", 0)
	if err != nil {
		t.Fatalf("handleCommand(/effort) failed: %v", err)
	}
	if callbackTokenForButton(response.Buttons, "minimal") == "" {
		t.Fatalf("reasoning buttons = %#v, want minimal option", response.Buttons)
	}
	if callbackTokenForButton(response.Buttons, "high") != "" {
		t.Fatalf("reasoning buttons = %#v, want no high option for selected model", response.Buttons)
	}
	token := callbackTokenForButton(response.Buttons, "minimal")
	callbackResponse, err := service.HandleCallback(ctx, 123456789, 0, 901, 123456789, token)
	if err != nil {
		t.Fatalf("HandleCallback(reasoning select) failed: %v", err)
	}
	if callbackResponse == nil || !strings.Contains(callbackResponse.Text, "Reasoning effort saved.") {
		t.Fatalf("callback response = %#v, want saved settings summary", callbackResponse)
	}
	if len(callbackResponse.Buttons) != 0 {
		t.Fatalf("callback buttons = %#v, want choice buttons removed after selection", callbackResponse.Buttons)
	}
	value, err := service.store.GetState(ctx, codexReasoningStateKey)
	if err != nil {
		t.Fatalf("GetState(reasoning) failed: %v", err)
	}
	if value != "minimal" {
		t.Fatalf("stored reasoning = %q, want minimal", value)
	}
}

func TestSettingsCallbacksMissingValueUseAuto(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()

	modelResponse, err := service.setCodexModel(ctx, 123456789, 0, 0, nil)
	if err != nil {
		t.Fatalf("setCodexModel(nil payload) failed: %v", err)
	}
	if modelResponse == nil || strings.Contains(modelResponse.Text, "<nil>") {
		t.Fatalf("model response = %#v, want no <nil>", modelResponse)
	}
	modelValue, err := service.store.GetState(ctx, codexModelStateKey)
	if err != nil {
		t.Fatalf("GetState(model) failed: %v", err)
	}
	if modelValue != "" {
		t.Fatalf("stored model = %q, want Auto/blank", modelValue)
	}

	reasoningResponse, err := service.setCodexReasoningEffort(ctx, 123456789, 0, 0, nil)
	if err != nil {
		t.Fatalf("setCodexReasoningEffort(nil payload) failed: %v", err)
	}
	if reasoningResponse == nil || strings.Contains(reasoningResponse.Text, "<nil>") {
		t.Fatalf("reasoning response = %#v, want no <nil>", reasoningResponse)
	}
	reasoningValue, err := service.store.GetState(ctx, codexReasoningStateKey)
	if err != nil {
		t.Fatalf("GetState(reasoning) failed: %v", err)
	}
	if reasoningValue != "" {
		t.Fatalf("stored reasoning = %q, want Auto/blank", reasoningValue)
	}
}

func TestAnswerChoiceMissingTextDoesNotSendNil(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.answerChoice(ctx, 123456789, 0, &model.CallbackRoute{
		ThreadID:    "thread-missing-text",
		TurnID:      "turn-missing-text",
		PayloadJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("answerChoice(missing text) failed: %v", err)
	}
	if response == nil || response.CallbackText != "Answer option is empty." {
		t.Fatalf("response = %#v, want empty answer callback", response)
	}
	if len(stub.turnSteerCalls) != 0 || len(stub.turnStartCalls) != 0 || len(stub.respondRequestCalls) != 0 {
		t.Fatalf("unexpected calls for missing answer text: steer=%#v start=%#v respond=%#v", stub.turnSteerCalls, stub.turnStartCalls, stub.respondRequestCalls)
	}
}

func TestLeadAgentReadCommands(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	stub := &stubSession{projectListResult: map[string]any{"data": []any{
		map[string]any{"id": "project-telegram", "name": "telegram-agent", "roots": []any{map[string]any{"path": "/work/telegram"}}},
		map[string]any{"id": "project-market", "name": "market-research", "roots": []any{map[string]any{"path": "/work/market"}}},
		map[string]any{"id": "project-platform", "name": "agent-platform", "roots": []any{map[string]any{"path": "/work/platform"}}},
	}}}
	service.live = stub
	service.liveConnected = true
	agents := []model.LeadAgent{
		{
			ID: "lead-builder", Name: "Builder", ChatID: 123456789, TopicID: 7,
			ThreadID: "thread-builder", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
			ProjectID: "project-telegram", Status: "idle", PolicyID: "lead-default", PolicyVersion: 3,
		},
		{
			ID: "lead-research", Name: "Research", ChatID: 123456789, TopicID: 8,
			ThreadID: "thread-research", Model: "gpt-6-astra", ReasoningEffort: "low",
			ProjectID: "project-market", Status: "working", PolicyID: "lead-default", PolicyVersion: 3,
		},
	}
	for _, agent := range agents {
		if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
			t.Fatalf("CreateLeadAgent(%s) failed: %v", agent.ID, err)
		}
	}

	list, err := service.handleCommand(ctx, 123456789, 7, "/agents", 0)
	if err != nil {
		t.Fatalf("handleCommand(/agents) failed: %v", err)
	}
	for _, want := range []string{"Lead agents (2)", "Builder", "telegram-agent", "Research", "market-research"} {
		if !strings.Contains(list.Text, want) {
			t.Fatalf("/agents text missing %q:\n%s", want, list.Text)
		}
	}

	show, err := service.handleCommand(ctx, 123456789, 7, "/agent show", 0)
	if err != nil {
		t.Fatalf("handleCommand(/agent show) failed: %v", err)
	}
	policy, err := service.handleCommand(ctx, 123456789, 7, "/agent policy", 0)
	if err != nil || !strings.Contains(policy.Text, "Lead policy: lead-default v3") || !strings.Contains(policy.Text, "Status: current") {
		t.Fatalf("/agent policy = %#v err=%v", policy, err)
	}
	for _, want := range []string{"Lead: Builder", "gpt-5.6-sol", "medium", "telegram-agent", "thread-builder"} {
		if !strings.Contains(show.Text, want) {
			t.Fatalf("/agent show text missing %q:\n%s", want, show.Text)
		}
	}

	updated, err := service.handleCommand(ctx, 123456789, 7, "/agent project agent-platform", 0)
	if err != nil {
		t.Fatalf("handleCommand(/agent project) failed: %v", err)
	}
	if !strings.Contains(updated.Text, "Builder is now the lead for Codex Project agent-platform") {
		t.Fatalf("/agent project = %q, want update confirmation", updated.Text)
	}
	stored, err := service.store.GetLeadAgentByTopic(ctx, 123456789, 7)
	if err != nil {
		t.Fatalf("GetLeadAgentByTopic after project update failed: %v", err)
	}
	if stored == nil || stored.ProjectID != "project-platform" {
		t.Fatalf("updated agent = %#v, want project agent-platform", stored)
	}

	unbound, err := service.handleCommand(ctx, 123456789, 99, "/agent show", 0)
	if err != nil {
		t.Fatalf("handleCommand(unbound /agent show) failed: %v", err)
	}
	if !strings.Contains(unbound.Text, "No lead agent is assigned") {
		t.Fatalf("unbound /agent show = %q, want assignment guidance", unbound.Text)
	}
}

func TestCreateLeadAgentStartsPersistentSolThread(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	stub := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{
			"id": "lead-thread", "cwd": service.cfg.DefaultCWD, "title": "New thread",
		}},
	}
	service.live = stub
	service.liveConnected = true

	response, err := service.handleCommand(ctx, 123456789, 21, "/agent create Builder", 0)
	if err != nil {
		t.Fatalf("handleCommand(/agent create) failed: %v", err)
	}
	if response == nil || response.ThreadID != "lead-thread" || response.TurnID != "started-turn" {
		t.Fatalf("create response = %#v, want lead thread and initialization turn", response)
	}
	if len(stub.threadStartCalls) != 1 || stub.threadStartCalls[0] != service.cfg.DefaultCWD {
		t.Fatalf("threadStartCalls = %#v, want default cwd", stub.threadStartCalls)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v, want one initialization turn", stub.turnStartCalls)
	}
	turn := stub.turnStartCalls[0]
	if turn.model != "gpt-5.6-sol" || turn.reasoningEffort != "medium" {
		t.Fatalf("initialization model = %q effort = %q, want Sol medium", turn.model, turn.reasoningEffort)
	}
	for _, want := range []string{"Lead Agent Policy", "luna_executor", "astra_advisor", "Critical path"} {
		if !strings.Contains(turn.message, want) {
			t.Fatalf("initialization prompt missing %q:\n%s", want, turn.message)
		}
	}
	agent, err := service.store.GetLeadAgentByTopic(ctx, 123456789, 21)
	if err != nil {
		t.Fatalf("GetLeadAgentByTopic failed: %v", err)
	}
	if agent == nil || agent.Name != "Builder" || agent.ThreadID != "lead-thread" || agent.Model != "gpt-5.6-sol" || agent.PolicyID != "lead-default" || agent.PolicyVersion != 3 {
		t.Fatalf("stored agent = %#v, want Builder Sol lead", agent)
	}
	binding, err := service.store.GetBinding(ctx, 123456789, 21)
	if err != nil {
		t.Fatalf("GetBinding failed: %v", err)
	}
	if binding == nil || binding.ThreadID != "lead-thread" {
		t.Fatalf("binding = %#v, want lead-thread", binding)
	}

	again, err := service.handleCommand(ctx, 123456789, 21, "/agent create Duplicate", 0)
	if err != nil {
		t.Fatalf("duplicate /agent create failed: %v", err)
	}
	if !strings.Contains(again.Text, "already belongs to Builder") || len(stub.threadStartCalls) != 1 {
		t.Fatalf("duplicate response = %#v calls = %#v, want no second thread", again, stub.threadStartCalls)
	}

	sameName, err := service.handleCommand(ctx, 123456789, 22, "/agent create builder", 0)
	if err != nil {
		t.Fatalf("same-name /agent create failed: %v", err)
	}
	if !strings.Contains(sameName.Text, "A lead named Builder already exists") || len(stub.threadStartCalls) != 1 {
		t.Fatalf("same-name response = %#v calls = %#v, want no orphan thread", sameName, stub.threadStartCalls)
	}
}

func TestLeadPolicyStatusAndApplyUsePersistentThread(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	ctx := context.Background()
	agent := model.LeadAgent{
		ID: "lead-builder", Name: "Builder", ChatID: 123456789, TopicID: 51,
		ThreadID: "lead-policy-thread", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
		ProjectID: "project-policy", Status: "idle", PolicyID: "lead-default", PolicyVersion: 0,
	}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatalf("CreateLeadAgent failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, model.Thread{ID: agent.ThreadID, Title: "Builder", CWD: "/old", Status: "idle", PreferredModel: "gpt-5.6-sol"}); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{projectListResult: map[string]any{"data": []any{
		map[string]any{"id": "project-policy", "name": "Builder", "roots": []any{map[string]any{"path": "/projects/builder"}}},
	}}}
	service.live = stub
	service.liveConnected = true

	status, err := service.handleCommand(ctx, agent.ChatID, agent.TopicID, "/agent policy", 0)
	if err != nil || !strings.Contains(status.Text, "update available") {
		t.Fatalf("policy status = %#v err=%v", status, err)
	}
	response, err := service.handleCommand(ctx, agent.ChatID, agent.TopicID, "/agent policy apply", 0)
	if err != nil {
		t.Fatalf("policy apply failed: %v", err)
	}
	if response.TurnID == "" || !strings.Contains(response.Text, "Applying lead-default v3") {
		t.Fatalf("policy apply response = %#v", response)
	}
	if len(stub.turnStartCalls) != 1 || !strings.Contains(stub.turnStartCalls[0].message, "luna_executor") || !strings.Contains(stub.turnStartCalls[0].message, "astra_advisor") {
		t.Fatalf("policy turn = %#v", stub.turnStartCalls)
	}
	stored, err := service.store.GetLeadAgentByTopic(ctx, agent.ChatID, agent.TopicID)
	if err != nil || stored.PolicyID != "lead-default" || stored.PolicyVersion != 3 {
		t.Fatalf("stored policy = %#v err=%v", stored, err)
	}
}

func TestCurrentLeadPolicyAddsRuntimeReminder(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	ctx := context.Background()
	agent := model.LeadAgent{
		ID: "lead-reminder", Name: "Reminder", ChatID: 123456789, TopicID: 52,
		ThreadID: "lead-reminder-thread", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
		ProjectID: "project-reminder", Status: "idle", PolicyID: "lead-default", PolicyVersion: 3,
	}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatalf("CreateLeadAgent failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, model.Thread{ID: agent.ThreadID, Title: "Reminder", CWD: "/old", Status: "idle", PreferredModel: "gpt-5.6-sol"}); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{projectListResult: map[string]any{"data": []any{
		map[string]any{"id": "project-reminder", "name": "Reminder", "roots": []any{map[string]any{"path": "/projects/reminder"}}},
	}}}
	service.live = stub
	service.liveConnected = true
	_, err := service.sendInputToThreadTurn(ctx, agent.ChatID, agent.TopicID, agent.ThreadID, "", "fix the issue", "")
	if err != nil {
		t.Fatalf("sendInputToThreadTurn failed: %v", err)
	}
	if len(stub.turnStartCalls) != 1 {
		t.Fatalf("turnStartCalls = %#v", stub.turnStartCalls)
	}
	turn := stub.turnStartCalls[0]
	for _, want := range []string{"runtime reminder", "luna_executor", "astra_advisor", "Operator request:\nfix the issue"} {
		if !strings.Contains(turn.message, want) {
			t.Fatalf("runtime message missing %q:\n%s", want, turn.message)
		}
	}
	if turn.cwd != "/projects/reminder" || turn.model != "gpt-5.6-sol" || turn.reasoningEffort != "medium" {
		t.Fatalf("turn route = %#v", turn)
	}
}

func TestAdoptedProjectPolicyUsesShortLeadMarkerAndFileV2(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte(projectAgentPolicyMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := model.LeadAgent{
		ID: "lead-project-policy", Name: "ProjectLead", ChatID: 123456789, TopicID: 62,
		ThreadID: "lead-project-policy-thread", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
		ProjectID: "project-policy-v1", Status: "idle", PolicyID: leadpolicy.ID, PolicyVersion: leadpolicy.Version,
	}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := service.store.UpsertThread(ctx, model.Thread{ID: agent.ThreadID, CWD: "/stale", Status: "idle", PreferredModel: agent.Model}); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{projectListResult: map[string]any{"data": []any{
		map[string]any{"id": agent.ProjectID, "name": "Project", "roots": []any{map[string]any{"path": project}}},
	}}}
	service.live = stub
	service.liveConnected = true
	response, err := service.sendInputToThreadTurn(ctx, agent.ChatID, agent.TopicID, agent.ThreadID, "", "fix it", "")
	if err != nil || response.TurnID == "" || len(stub.turnStartCalls) != 1 {
		t.Fatalf("response = %#v, calls = %#v, err = %v", response, stub.turnStartCalls, err)
	}
	call := stub.turnStartCalls[0]
	if call.cwd != project || !strings.Contains(call.message, "Project-root AGENTS.md") || !strings.Contains(call.message, "role=lead; file=v2") {
		t.Fatalf("turn call = %#v", call)
	}
	if strings.Contains(call.message, "runtime reminder") || strings.Contains(call.message, "luna_executor") || strings.Contains(call.message, "nonce") {
		t.Fatalf("short prompt repeated static policy: %s", call.message)
	}
	origin, err := service.store.GetTelegramTurnOrigin(ctx, agent.ThreadID, response.TurnID)
	if err != nil || origin == nil || origin.DeliveryProtocolVersion != 2 || origin.DeliveryNonce != "" {
		t.Fatalf("origin = %#v, err = %v", origin, err)
	}
}

func TestAdoptedProjectPolicyKeepsLegacyPromptForActiveV1Turn(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte(projectAgentPolicyMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := model.LeadAgent{ID: "lead-v1-steer", Name: "Legacy", ChatID: 123456789, TopicID: 63, ThreadID: "thread-v1-steer", Model: "gpt-5.6-sol", ReasoningEffort: "medium", ProjectID: "project-v1-steer", Status: "active", PolicyID: leadpolicy.ID, PolicyVersion: leadpolicy.Version}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	thread := model.Thread{ID: agent.ThreadID, CWD: "/stale", Status: "active", ActiveTurnID: "turn-v1", PreferredModel: agent.Model}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: thread.ActiveTurnID, ChatID: agent.ChatID, TopicID: agent.TopicID, DeliveryProtocolVersion: 1, DeliveryNonce: "legacy-nonce"}); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{projectListResult: map[string]any{"data": []any{
		map[string]any{"id": agent.ProjectID, "roots": []any{map[string]any{"path": project}}},
	}}}
	service.live = stub
	service.liveConnected = true
	if _, err := service.sendInputToThreadTurn(ctx, agent.ChatID, agent.TopicID, thread.ID, "", "continue", ""); err != nil {
		t.Fatal(err)
	}
	if len(stub.turnSteerCalls) != 1 {
		t.Fatalf("steer calls = %#v", stub.turnSteerCalls)
	}
	prompt := stub.turnSteerCalls[0].message
	for _, want := range []string{"runtime reminder", `nonce "legacy-nonce"`, "Operator request:\ncontinue"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("legacy steer prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "file=v2") {
		t.Fatalf("legacy turn received v2 marker: %s", prompt)
	}
}

func TestOutdatedLeadPolicyUpgradesOnNextRequest(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	ctx := context.Background()
	agent := model.LeadAgent{
		ID: "lead-upgrade", Name: "Upgrade", ChatID: 123456789, TopicID: 53,
		ThreadID: "lead-upgrade-thread", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
		ProjectID: "project-upgrade", Status: "idle",
	}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatalf("CreateLeadAgent failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, model.Thread{ID: agent.ThreadID, Title: "Upgrade", CWD: "/old", Status: "idle", PreferredModel: "gpt-5.6-sol"}); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{projectListResult: map[string]any{"data": []any{
		map[string]any{"id": "project-upgrade", "name": "Upgrade", "roots": []any{map[string]any{"path": "/projects/upgrade"}}},
	}}}
	service.live = stub
	service.liveConnected = true
	_, err := service.sendInputToThreadTurn(ctx, agent.ChatID, agent.TopicID, agent.ThreadID, "", "start the task", "")
	if err != nil {
		t.Fatalf("sendInputToThreadTurn failed: %v", err)
	}
	if len(stub.turnStartCalls) != 1 || !strings.Contains(stub.turnStartCalls[0].message, "Lead Agent Policy") || !strings.Contains(stub.turnStartCalls[0].message, "## Operator request\n\nstart the task") {
		t.Fatalf("upgrade turn = %#v", stub.turnStartCalls)
	}
	stored, err := service.store.GetLeadAgentByTopic(ctx, agent.ChatID, agent.TopicID)
	if err != nil || stored.PolicyID != "lead-default" || stored.PolicyVersion != 3 {
		t.Fatalf("stored policy = %#v err=%v", stored, err)
	}
}

func TestLeadAgentRouteOverridesGlobalLunaModel(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, codexModelStateKey, "gpt-5.6-luna"); err != nil {
		t.Fatalf("SetState(model) failed: %v", err)
	}
	if err := service.store.SetState(ctx, codexReasoningStateKey, "low"); err != nil {
		t.Fatalf("SetState(reasoning) failed: %v", err)
	}
	agent := model.LeadAgent{
		ID: "lead-builder", Name: "Builder", ChatID: 123456789, TopicID: 31,
		ThreadID: "lead-thread", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
		ProjectID: "project-telegram", Status: "idle", PolicyID: "lead-default", PolicyVersion: 3,
	}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatalf("CreateLeadAgent failed: %v", err)
	}
	thread := &model.Thread{ID: "lead-thread", PreferredModel: "gpt-5.6-sol", CWD: "/projects/telegram"}

	leadOptions := service.turnStartOptionsForRoute(ctx, 123456789, 31, "", thread)
	if leadOptions.Model != "gpt-5.6-sol" || leadOptions.ReasoningEffort != "medium" {
		t.Fatalf("lead options = %#v, want Sol medium", leadOptions)
	}
	if leadOptions.SandboxMode != "workspaceWrite" || len(leadOptions.WritableRoots) != 1 || leadOptions.WritableRoots[0] != "/projects/telegram" || leadOptions.ApprovalPolicy != "on-request" || leadOptions.ApprovalsReviewer != "auto_review" {
		t.Fatalf("lead permissions = %#v, want workspaceWrite with auto review", leadOptions)
	}

	ordinaryOptions := service.turnStartOptionsForRoute(ctx, 123456789, 32, "", thread)
	if ordinaryOptions.Model != "gpt-5.6-luna" || ordinaryOptions.ReasoningEffort != "low" {
		t.Fatalf("ordinary options = %#v, want global Luna low", ordinaryOptions)
	}
	if ordinaryOptions.SandboxMode != "" || ordinaryOptions.ApprovalPolicy != "" || ordinaryOptions.ApprovalsReviewer != "" {
		t.Fatalf("ordinary permissions unexpectedly overridden: %#v", ordinaryOptions)
	}
}

func TestLeadAgentProjectRootComesFromCodexProject(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	ctx := context.Background()
	agent := model.LeadAgent{
		ID: "lead-builder", Name: "Builder", ChatID: 123456789, TopicID: 31,
		ThreadID: "lead-thread", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
		ProjectID: "project-telegram", Status: "idle", PolicyID: "lead-default", PolicyVersion: 3,
	}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatalf("CreateLeadAgent failed: %v", err)
	}
	stub := &stubSession{projectListResult: map[string]any{"data": []any{
		map[string]any{"id": "project-telegram", "name": "Telegram", "roots": []any{map[string]any{"path": "/projects/telegram"}}},
	}}}
	root, err := service.leadProjectRoot(ctx, stub, 123456789, 31, "lead-thread")
	if err != nil {
		t.Fatalf("leadProjectRoot failed: %v", err)
	}
	if root != "/projects/telegram" {
		t.Fatalf("leadProjectRoot = %q, want official Codex root", root)
	}
}

func TestLeadAgentModelAndLifecycleStatus(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	agent := model.LeadAgent{
		ID: "lead-axiom", Name: "Axiom", ChatID: 123456789, TopicID: 41,
		ThreadID: "axiom-thread", Model: "gpt-5.6-sol", ReasoningEffort: "medium",
		ProjectID: "project-workspace", Status: "initializing", PolicyID: "lead-default", PolicyVersion: 3,
	}
	if err := service.store.CreateLeadAgent(ctx, agent); err != nil {
		t.Fatalf("CreateLeadAgent failed: %v", err)
	}
	if err := service.store.UpsertThread(ctx, model.Thread{ID: agent.ThreadID, PreferredModel: agent.Model}); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	stub := &stubSession{models: []appserver.ModelOption{
		{ID: "gpt-5.6-sol", DefaultReasoningEffort: "medium", SupportedReasoningEffort: []string{"low", "medium", "high"}},
		{ID: "gpt-5.6-luna", DefaultReasoningEffort: "low", SupportedReasoningEffort: []string{"low", "medium"}},
		{ID: "gpt-6-astra", DefaultReasoningEffort: "low", SupportedReasoningEffort: []string{"low", "medium", "high"}},
		{ID: "gpt-custom", DefaultReasoningEffort: "minimal", SupportedReasoningEffort: []string{"minimal", "low"}},
		{ID: "gpt-no-reasoning"},
		{ID: "gpt-hidden", Hidden: true, DefaultReasoningEffort: "low", SupportedReasoningEffort: []string{"low"}},
	}}
	service.live = stub
	service.liveConnected = true

	changed, err := service.handleCommand(ctx, 123456789, 41, "/agent model astra low", 0)
	if err != nil {
		t.Fatalf("/agent model failed: %v", err)
	}
	if !strings.Contains(changed.Text, "gpt-6-astra with low reasoning") {
		t.Fatalf("/agent model response = %q", changed.Text)
	}
	stored, err := service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if err != nil || stored == nil {
		t.Fatalf("GetLeadAgentByTopic = %#v err=%v", stored, err)
	}
	if stored.Model != "gpt-6-astra" || stored.ReasoningEffort != "low" {
		t.Fatalf("stored model = %q effort = %q, want Astra low", stored.Model, stored.ReasoningEffort)
	}

	changed, err = service.handleCommand(ctx, 123456789, 41, "/agent model luna", 0)
	if err != nil {
		t.Fatalf("Luna /agent model failed: %v", err)
	}
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Model != "gpt-5.6-luna" || stored.ReasoningEffort != "low" {
		t.Fatalf("stored Luna model = %q effort = %q", stored.Model, stored.ReasoningEffort)
	}

	changed, err = service.handleCommand(ctx, 123456789, 41, "/agent model gpt-custom minimal", 0)
	if err != nil {
		t.Fatalf("custom /agent model failed: %v", err)
	}
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Model != "gpt-custom" || stored.ReasoningEffort != "minimal" {
		t.Fatalf("stored custom model = %q effort = %q", stored.Model, stored.ReasoningEffort)
	}
	options := service.turnStartOptionsForRoute(ctx, 123456789, 41, "", &model.Thread{ID: agent.ThreadID})
	if options.Model != "gpt-custom" || options.ReasoningEffort != "minimal" {
		t.Fatalf("custom lead turn options = %#v", options)
	}

	rejected, err := service.handleCommand(ctx, 123456789, 41, "/agent model gpt-custom high", 0)
	if err != nil {
		t.Fatalf("unsupported effort failed: %v", err)
	}
	if !strings.Contains(rejected.Text, "is not supported") {
		t.Fatalf("unsupported effort response = %q", rejected.Text)
	}
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Model != "gpt-custom" || stored.ReasoningEffort != "minimal" {
		t.Fatalf("unsupported effort changed model: %#v", stored)
	}

	rejected, err = service.handleCommand(ctx, 123456789, 41, "/agent model missing-model low", 0)
	if err != nil {
		t.Fatalf("invalid /agent model failed: %v", err)
	}
	if !strings.Contains(rejected.Text, "is not available") {
		t.Fatalf("invalid model response = %q", rejected.Text)
	}
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Model != "gpt-custom" || stored.ReasoningEffort != "minimal" {
		t.Fatalf("invalid selection changed model: %#v", stored)
	}

	rejected, err = service.handleCommand(ctx, 123456789, 41, "/agent model gpt-hidden low", 0)
	if err != nil || !strings.Contains(rejected.Text, "is not available") {
		t.Fatalf("hidden model response = %#v err=%v", rejected, err)
	}
	stub.modelListErr = errors.New("catalog unavailable")
	rejected, err = service.handleCommand(ctx, 123456789, 41, "/agent model sol", 0)
	if err != nil || !strings.Contains(rejected.Text, "Could not validate Codex model") {
		t.Fatalf("catalog failure response = %#v err=%v", rejected, err)
	}
	stub.modelListErr = nil
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	thread, _ := service.store.GetThread(ctx, agent.ThreadID)
	if stored.Model != "gpt-custom" || stored.ReasoningEffort != "minimal" || thread.PreferredModel != "gpt-custom" {
		t.Fatalf("rejected selections changed state: agent=%#v thread=%#v", stored, thread)
	}
	if stored.ChatID != agent.ChatID || stored.TopicID != agent.TopicID || stored.ThreadID != agent.ThreadID || stored.ProjectID != agent.ProjectID || stored.PolicyID != agent.PolicyID || stored.PolicyVersion != agent.PolicyVersion {
		t.Fatalf("model changes altered lead identity: %#v", stored)
	}

	changed, err = service.handleCommand(ctx, 123456789, 41, "/agent model gpt-no-reasoning", 0)
	if err != nil || !strings.Contains(changed.Text, "automatic reasoning") {
		t.Fatalf("automatic reasoning response = %#v err=%v", changed, err)
	}
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Model != "gpt-no-reasoning" || stored.ReasoningEffort != "" {
		t.Fatalf("stored automatic reasoning model = %#v", stored)
	}

	service.syncLeadAgentStatus(ctx, agent.ThreadID, &appserver.ThreadReadSnapshot{LatestTurnStatus: "inProgress"})
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Status != "working" {
		t.Fatalf("working status = %q", stored.Status)
	}
	service.syncLeadAgentStatus(ctx, agent.ThreadID, &appserver.ThreadReadSnapshot{LatestTurnStatus: "active", WaitingOnReply: true})
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Status != "waiting" {
		t.Fatalf("waiting status = %q", stored.Status)
	}
	service.syncLeadAgentStatus(ctx, agent.ThreadID, &appserver.ThreadReadSnapshot{LatestTurnStatus: "completed"})
	stored, _ = service.store.GetLeadAgentByTopic(ctx, 123456789, 41)
	if stored.Status != "idle" {
		t.Fatalf("idle status = %q", stored.Status)
	}
}

func TestUserInputResponsePayloadSkipsNilQuestionID(t *testing.T) {
	t.Parallel()

	response := userInputResponsePayload(`{"questions":[{"id":"<nil>","question":"Pick one."},{"question":"Missing id."}]}`, "Yes")
	if _, ok := response["answers"]; ok {
		t.Fatalf("response = %#v, want fallback text payload without <nil> answer id", response)
	}
	if response["text"] != "Yes" || response["value"] != "Yes" || response["response"] != "Yes" || response["input"] != "Yes" {
		t.Fatalf("response = %#v, want fallback text/value/response/input", response)
	}
}

func TestPlainReplyToRealPlanPromptUsesServerRequest(t *testing.T) {
	t.Parallel()

	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SavePendingApproval(ctx, model.PendingApproval{
		RequestID:   "request-plan-reply",
		ThreadID:    "real-plan-thread",
		TurnID:      "real-plan-turn",
		PromptKind:  "user_input",
		Question:    "Need input.",
		PayloadJSON: `{"questions":[{"id":"choice","question":"Need input?","options":[{"label":"The answer","description":"Use answer."}]}]}`,
		Status:      "pending",
		UpdatedAt:   model.NowString(),
	}); err != nil {
		t.Fatalf("SavePendingApproval failed: %v", err)
	}
	if err := service.store.PutMessageRoute(ctx, model.MessageRoute{
		ChatID:    123456789,
		TopicID:   0,
		MessageID: 779,
		ThreadID:  "real-plan-thread",
		TurnID:    "real-plan-turn",
		EventID:   "plan_request:request-plan-reply",
		CreatedAt: model.NowString(),
	}); err != nil {
		t.Fatalf("PutMessageRoute failed: %v", err)
	}
	stub := &stubSession{}
	service.live = stub
	service.liveConnected = true

	response, err := service.handlePlainText(ctx, 123456789, 0, "The answer", 779)
	if err != nil {
		t.Fatalf("handlePlainText failed: %v", err)
	}
	if response == nil || response.ThreadID != "real-plan-thread" || response.TurnID != "real-plan-turn" {
		t.Fatalf("response = %#v, want real plan thread/turn", response)
	}
	if len(stub.respondRequestCalls) != 1 {
		t.Fatalf("respondRequestCalls = %#v, want one server request response", stub.respondRequestCalls)
	}
	got := stub.respondRequestCalls[0]
	answers, _ := got.result["answers"].(map[string]any)
	choice, _ := answers["choice"].(map[string]any)
	values, _ := choice["answers"].([]string)
	if got.requestID != "request-plan-reply" || len(values) != 1 || values[0] != "The answer" {
		t.Fatalf("respond request call = %#v, want request-plan-reply schema answers", got)
	}
	if len(stub.turnSteerCalls) != 0 || len(stub.turnStartCalls) != 0 {
		t.Fatalf("unexpected turn calls: steer=%#v start=%#v", stub.turnSteerCalls, stub.turnStartCalls)
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()

	root := t.TempDir()
	cfg := config.Config{
		Paths: config.Paths{
			Home:    root,
			DataDir: filepath.Join(root, "data"),
			LogDir:  filepath.Join(root, "logs"),
			DBPath:  filepath.Join(root, "data", "state.sqlite"),
		},
		AllowedUserIDs: []int64{123456789},
		DefaultCWD:     `C:\Users\you\Projects\Codex`,
	}
	service, err := New(cfg)
	if err != nil {
		t.Fatalf("daemon.New failed: %v", err)
	}
	t.Cleanup(func() {
		_ = service.Close()
	})
	return service
}

func captureServiceLogs(service *Service) *bytes.Buffer {
	var logs bytes.Buffer
	service.SetLogger(log.New(&logs, "", 0))
	return &logs
}

func requireLogContains(t *testing.T, logs, needle string) {
	t.Helper()
	if !strings.Contains(logs, needle) {
		t.Fatalf("diagnostic log missing %q in:\n%s", needle, logs)
	}
}

func diagnosticThreadReadPayload(thread model.Thread, turnID, status string) map[string]any {
	return map[string]any{
		"thread": map[string]any{
			"id":     thread.ID,
			"title":  thread.Title,
			"cwd":    thread.CWD,
			"status": thread.Status,
			"turns": []any{
				map[string]any{
					"id":     turnID,
					"status": status,
					"items": []any{
						map[string]any{
							"id":      "user-item",
							"type":    "userMessage",
							"content": []any{map[string]any{"text": "hello"}},
						},
					},
				},
			},
		},
	}
}

func diagnosticThreadReadPayloadWithTool(thread model.Thread, turnID, status string) map[string]any {
	payload := diagnosticThreadReadPayload(thread, turnID, status)
	threadPayload := payload["thread"].(map[string]any)
	turns := threadPayload["turns"].([]any)
	turn := turns[0].(map[string]any)
	turn["items"] = []any{
		map[string]any{
			"id":      "user-item",
			"type":    "userMessage",
			"content": []any{map[string]any{"text": "hello"}},
		},
		map[string]any{
			"id":               "cmd-slow",
			"type":             "commandExecution",
			"command":          "sleep 20; printf 'slow-command-done\\n'",
			"status":           "completed",
			"aggregatedOutput": "slow-command-done\n",
		},
	}
	return payload
}

func diagnosticThreadReadPayloadWithFinal(thread model.Thread, turnID, status, finalText string) map[string]any {
	payload := diagnosticThreadReadPayloadWithTool(thread, turnID, status)
	threadPayload := payload["thread"].(map[string]any)
	turns := threadPayload["turns"].([]any)
	turn := turns[0].(map[string]any)
	items := turn["items"].([]any)
	turn["items"] = append(items, map[string]any{
		"id":    "final-item",
		"type":  "agentMessage",
		"phase": "final_answer",
		"text":  finalText,
	})
	return payload
}

func callbackTokenForButton(rows [][]model.ButtonSpec, label string) string {
	for _, row := range rows {
		for _, button := range row {
			if strings.Contains(button.Text, label) {
				return button.CallbackData
			}
		}
	}
	return ""
}

func countButtonsContaining(rows [][]model.ButtonSpec, label string) int {
	count := 0
	for _, row := range rows {
		for _, button := range row {
			if strings.Contains(button.Text, label) {
				count++
			}
		}
	}
	return count
}

func requireTextOrder(t *testing.T, text, before, after string) {
	t.Helper()
	beforeIndex := strings.Index(text, before)
	afterIndex := strings.Index(text, after)
	if beforeIndex < 0 || afterIndex < 0 || beforeIndex >= afterIndex {
		t.Fatalf("text order = before %q at %d, after %q at %d in:\n%s", before, beforeIndex, after, afterIndex, text)
	}
}

func openOnlyProjectMenu(t *testing.T, service *Service, ctx context.Context) *DirectResponse {
	t.Helper()
	projects, err := service.handleCommand(ctx, 123456789, 0, "/projects", 0)
	if err != nil {
		t.Fatalf("handleCommand(/projects) failed: %v", err)
	}
	token := callbackTokenForButton(projects.Buttons, "1. project")
	if token == "" {
		t.Fatalf("/projects buttons = %#v, want project button", projects.Buttons)
	}
	menu, err := service.HandleCallback(ctx, 123456789, 0, 42, 123456789, token)
	if err != nil {
		t.Fatalf("HandleCallback(project_open) failed: %v", err)
	}
	if menu == nil {
		t.Fatal("HandleCallback(project_open) returned nil response")
	}
	return menu
}

type startCountingSession struct {
	stubSession
	mu       sync.Mutex
	started  chan struct{}
	unblock  chan struct{}
	once     sync.Once
	starts   int
	signaled bool
}

func newStartCountingSession() *startCountingSession {
	return &startCountingSession{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
}

func (s *startCountingSession) Start(ctx context.Context) error {
	s.mu.Lock()
	s.starts++
	if !s.signaled {
		close(s.started)
		s.signaled = true
	}
	s.mu.Unlock()
	select {
	case <-s.unblock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *startCountingSession) ThreadList(ctx context.Context, limit int, cursor string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (s *startCountingSession) waitStarted(t *testing.T, role string) {
	t.Helper()
	select {
	case <-s.started:
	case <-time.After(time.Second):
		t.Fatalf("%s session did not start", role)
	}
}

func (s *startCountingSession) release() {
	s.once.Do(func() {
		close(s.unblock)
	})
}

func (s *startCountingSession) StartCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

type stubSession struct {
	threadReads            map[string]map[string]any
	threadReadQueue        map[string][]map[string]any
	threadListResult       map[string]any
	projectListResult      map[string]any
	threadProjectUpdates   []string
	threadListCalls        int
	threadListLimit        int
	threadListCursor       string
	threadReadID           string
	threadReadIncludeTurns bool
	models                 []appserver.ModelOption
	modelListErr           error
	collaborationModes     []appserver.CollaborationModeOption
	threadReadErr          error
	threadResumeErr        error
	threadStartErr         error
	threadStartResult      map[string]any
	turnStartErr           error
	turnSteerErr           error
	turnSteerErrs          []error
	turnInterruptErr       error
	threadStartCalls       []string
	threadResumeCalls      []threadResumeCall
	turnSteerCalls         []turnCall
	turnStartCalls         []turnCall
	turnInterruptCalls     []turnCall
	respondRequestCalls    []respondRequestCall
	stderrTail             []string
}

type threadResumeCall struct {
	threadID string
	cwd      string
}

type turnCall struct {
	threadID          string
	turnID            string
	message           string
	cwd               string
	collaborationMode string
	model             string
	reasoningEffort   string
}

type respondRequestCall struct {
	requestID string
	result    map[string]any
}

func (s *stubSession) Start(ctx context.Context) error { return nil }
func (s *stubSession) Close() error                    { return nil }
func (s *stubSession) Subscribe() <-chan appserver.Event {
	return nil
}
func (s *stubSession) ThreadList(ctx context.Context, limit int, cursor string) (map[string]any, error) {
	s.threadListCalls++
	s.threadListLimit = limit
	s.threadListCursor = cursor
	return s.threadListResult, nil
}
func (s *stubSession) ProjectList(ctx context.Context, limit int, cursor string) (map[string]any, error) {
	return s.projectListResult, nil
}
func (s *stubSession) ThreadProjectUpdate(ctx context.Context, threadID, projectID string) (map[string]any, error) {
	s.threadProjectUpdates = append(s.threadProjectUpdates, threadID+":"+projectID)
	return map[string]any{}, nil
}
func (s *stubSession) ThreadRead(ctx context.Context, threadID string, includeTurns bool) (map[string]any, error) {
	s.threadReadID = threadID
	s.threadReadIncludeTurns = includeTurns
	if s.threadReadErr != nil {
		return nil, s.threadReadErr
	}
	if queue := s.threadReadQueue[threadID]; len(queue) > 0 {
		payload := queue[0]
		s.threadReadQueue[threadID] = queue[1:]
		return payload, nil
	}
	if payload, ok := s.threadReads[threadID]; ok {
		return payload, nil
	}
	return nil, nil
}
func (s *stubSession) ThreadResume(ctx context.Context, threadID, cwd string) (map[string]any, error) {
	s.threadResumeCalls = append(s.threadResumeCalls, threadResumeCall{threadID: threadID, cwd: cwd})
	if s.threadResumeErr != nil {
		return nil, s.threadResumeErr
	}
	return nil, nil
}
func (s *stubSession) ThreadStart(ctx context.Context, cwd string) (map[string]any, error) {
	s.threadStartCalls = append(s.threadStartCalls, cwd)
	if s.threadStartErr != nil {
		return nil, s.threadStartErr
	}
	return s.threadStartResult, nil
}
func (s *stubSession) TurnStart(ctx context.Context, threadID, message, cwd string, options appserver.TurnStartOptions) (map[string]any, error) {
	if s.turnStartErr != nil {
		return nil, s.turnStartErr
	}
	s.turnStartCalls = append(s.turnStartCalls, turnCall{
		threadID:          threadID,
		message:           message,
		cwd:               cwd,
		collaborationMode: options.CollaborationMode,
		model:             options.Model,
		reasoningEffort:   options.ReasoningEffort,
	})
	return map[string]any{"turn": map[string]any{"id": "started-turn"}}, nil
}
func (s *stubSession) TurnSteer(ctx context.Context, threadID, turnID, message string) (map[string]any, error) {
	s.turnSteerCalls = append(s.turnSteerCalls, turnCall{threadID: threadID, turnID: turnID, message: message})
	if len(s.turnSteerErrs) > 0 {
		err := s.turnSteerErrs[0]
		s.turnSteerErrs = s.turnSteerErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if s.turnSteerErr != nil {
		return nil, s.turnSteerErr
	}
	return map[string]any{"turn": map[string]any{"id": turnID}}, nil
}
func (s *stubSession) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	s.turnInterruptCalls = append(s.turnInterruptCalls, turnCall{threadID: threadID, turnID: turnID})
	return s.turnInterruptErr
}
func (s *stubSession) ModelList(ctx context.Context, includeHidden bool) ([]appserver.ModelOption, error) {
	if s.modelListErr != nil {
		return nil, s.modelListErr
	}
	if s.models != nil {
		return s.models, nil
	}
	return []appserver.ModelOption{
		{ID: "gpt-default", IsDefault: true, SupportedReasoningEffort: []string{"low", "medium", "high"}},
		{ID: "gpt-alt", SupportedReasoningEffort: []string{"minimal", "low"}},
	}, nil
}
func (s *stubSession) CollaborationModeList(ctx context.Context) ([]appserver.CollaborationModeOption, error) {
	return s.collaborationModes, nil
}
func (s *stubSession) RespondServerRequest(ctx context.Context, requestID string, result map[string]any) error {
	s.respondRequestCalls = append(s.respondRequestCalls, respondRequestCall{requestID: requestID, result: result})
	return nil
}
func (s *stubSession) StderrTail() []string { return s.stderrTail }
