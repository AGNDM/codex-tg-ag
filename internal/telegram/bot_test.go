package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/daemon"
	"github.com/mideco-tech/codex-tg/internal/model"
)

type recordingBotService struct {
	deny      bool
	documents []struct {
		name, caption string
		data          []byte
		replyTo       int64
	}
}

func (s *recordingBotService) IsAllowed(int64, int64) bool { return !s.deny }

func (s *recordingBotService) HandleMessage(context.Context, int64, int64, int64, string, int64) (*daemon.DirectResponse, error) {
	return nil, nil
}

func (s *recordingBotService) HandleDocument(_ context.Context, _, _, _ int64, name string, data []byte, caption string, replyTo int64) (*daemon.DirectResponse, error) {
	s.documents = append(s.documents, struct {
		name, caption string
		data          []byte
		replyTo       int64
	}{name: name, caption: caption, data: data, replyTo: replyTo})
	return nil, nil
}

func (s *recordingBotService) HandleCallback(context.Context, int64, int64, int64, int64, string) (*daemon.DirectResponse, error) {
	return nil, nil
}

func (s *recordingBotService) RegisterDirectDelivery(context.Context, int64, int64, int64, *daemon.DirectResponse) error {
	return nil
}

func newDocumentBotServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"documents/report.txt"}}`))
		case strings.Contains(r.URL.Path, "/file/bot"):
			_, _ = w.Write([]byte("file body"))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"chat":{"id":42,"type":"private"}}}`))
		}
	}))
	return server, &calls
}

func TestCaptionDocumentDispatchesOnce(t *testing.T) {
	server, _ := newDocumentBotServer(t)
	defer server.Close()
	service := &recordingBotService{}
	client := NewClient("token")
	client.baseURL = server.URL + "/bottoken"
	bot := &Bot{client: client, service: service, logger: log.New(io.Discard, "", 0)}

	err := bot.handleMessage(context.Background(), Message{From: &User{ID: 9}, Chat: Chat{ID: 42}, Caption: "summarize", Document: &DocumentMeta{FileID: "f1", FileName: "report.txt", FileSize: 9}})
	if err != nil {
		t.Fatal(err)
	}
	if len(service.documents) != 1 || service.documents[0].caption != "summarize" || string(service.documents[0].data) != "file body" {
		t.Fatalf("documents = %#v", service.documents)
	}
}

func TestBareDocumentPromptsWithoutDownload(t *testing.T) {
	server, calls := newDocumentBotServer(t)
	defer server.Close()
	service := &recordingBotService{}
	client := NewClient("token")
	client.baseURL = server.URL + "/bottoken"
	bot := &Bot{client: client, service: service, logger: log.New(io.Discard, "", 0)}

	err := bot.handleMessage(context.Background(), Message{From: &User{ID: 9}, Chat: Chat{ID: 42}, Document: &DocumentMeta{FileID: "f1", FileName: "report.txt"}, ReplyToMessage: &Message{MessageID: 6}})
	if err != nil {
		t.Fatal(err)
	}
	if len(service.documents) != 0 || *calls != 1 {
		t.Fatalf("documents = %#v, HTTP calls = %d; want prompt only", service.documents, *calls)
	}
}

func TestTextReplyToDocumentDispatchesOnce(t *testing.T) {
	server, _ := newDocumentBotServer(t)
	defer server.Close()
	service := &recordingBotService{}
	client := NewClient("token")
	client.baseURL = server.URL + "/bottoken"
	bot := &Bot{client: client, service: service, logger: log.New(io.Discard, "", 0)}

	err := bot.handleMessage(context.Background(), Message{MessageID: 12, From: &User{ID: 9}, Chat: Chat{ID: 42}, Text: "summarize", ReplyToMessage: &Message{MessageID: 11, Document: &DocumentMeta{FileID: "f1", FileName: "report.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(service.documents) != 1 || service.documents[0].caption != "summarize" || service.documents[0].replyTo != 11 {
		t.Fatalf("documents = %#v", service.documents)
	}
}

func TestOversizeDocumentNeverDispatches(t *testing.T) {
	server, _ := newDocumentBotServer(t)
	defer server.Close()
	service := &recordingBotService{}
	client := NewClient("token")
	client.baseURL = server.URL + "/bottoken"
	bot := &Bot{client: client, service: service, logger: log.New(io.Discard, "", 0)}

	err := bot.handleMessage(context.Background(), Message{From: &User{ID: 9}, Chat: Chat{ID: 42}, Caption: "inspect", Document: &DocumentMeta{FileID: "f1", FileSize: 20<<20 + 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(service.documents) != 0 {
		t.Fatalf("documents = %#v, want none", service.documents)
	}
}

func TestUnauthorizedDocumentDoesNotDownload(t *testing.T) {
	server, calls := newDocumentBotServer(t)
	defer server.Close()
	service := &recordingBotService{deny: true}
	client := NewClient("token")
	client.baseURL = server.URL + "/bottoken"
	bot := &Bot{client: client, service: service, logger: log.New(io.Discard, "", 0)}

	err := bot.handleMessage(context.Background(), Message{From: &User{ID: 9}, Chat: Chat{ID: 42}, Caption: "inspect", Document: &DocumentMeta{FileID: "f1"}})
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || len(service.documents) != 0 {
		t.Fatalf("HTTP calls = %d, documents = %#v; want none", *calls, service.documents)
	}
}

func TestBotEditMessageRejectsMultiChunkPayload(t *testing.T) {
	t.Parallel()

	bot := &Bot{client: NewClient("token")}
	err := bot.EditMessage(context.Background(), 42, 0, 77, strings.Repeat("x", telegramMessageLimit+10), nil)
	if err == nil {
		t.Fatal("EditMessage must reject multi-chunk payloads")
	}
}

func TestSanitizeTelegramLogErrorRedactsBotTokenURL(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf(`Post "https://api.telegram.org/bot123456789:AAF_secret-token/getUpdates": context deadline exceeded`)
	got := sanitizeTelegramLogError(err)
	if strings.Contains(got, "123456789:AAF_secret-token") {
		t.Fatalf("sanitizeTelegramLogError leaked token: %q", got)
	}
	if !strings.Contains(got, "bot<redacted>") {
		t.Fatalf("sanitizeTelegramLogError = %q, want redacted marker", got)
	}
}

func TestDefaultCommandsExposeNewChatMenuCommand(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for _, command := range defaultCommands() {
		if seen[command.Command] {
			t.Fatalf("defaultCommands contains duplicate command %q", command.Command)
		}
		seen[command.Command] = true
	}
	for _, command := range []string{"newchat", "newthread"} {
		if !seen[command] {
			t.Fatalf("defaultCommands must expose /%s in the Telegram command menu", command)
		}
	}
	if seen["default"] {
		t.Fatal("defaultCommands must not expose hidden /default fallback in the Telegram command menu")
	}
}

func TestBotSendMessageChunksAndReturnsLastMessageID(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":42,"type":"private"}}}`, 100+calls)
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client}

	messageID, err := bot.SendMessage(context.Background(), 42, 0, strings.Repeat("line\n", telegramMessageLimit/4), nil, model.SendOptions{})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want at least 2 chunked requests", calls)
	}
	if got, want := messageID, int64(100+calls); got != want {
		t.Fatalf("messageID = %d, want %d", got, want)
	}
}

func TestBotSendRenderedMessagesFallsBackToPlainEntities(t *testing.T) {
	t.Parallel()

	var calls int
	var second map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: entities are invalid"}`))
			return
		}
		if err := json.Unmarshal(body, &second); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":202,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client}
	ids, err := bot.SendRenderedMessages(context.Background(), 42, 0, []model.RenderedMessage{{
		Text:     "formatted",
		Entities: []model.MessageEntity{{Type: "code", Offset: 0, Length: 9}},
	}}, nil, model.SendOptions{})
	if err != nil {
		t.Fatalf("SendRenderedMessages failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(ids) != 1 || ids[0] != 202 {
		t.Fatalf("ids = %#v, want [202]", ids)
	}
	if _, ok := second["entities"]; ok {
		t.Fatalf("fallback entities = %#v, want omitted", second["entities"])
	}
	if _, ok := second["parse_mode"]; ok {
		t.Fatalf("fallback parse_mode = %#v, want omitted", second["parse_mode"])
	}
}

func TestBotSendDocumentReturnsTelegramMessageID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":555,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client}

	dir := t.TempDir()
	path := filepath.Join(dir, "trace.log")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile(trace.log) failed: %v", err)
	}

	messageID, err := bot.SendDocument(context.Background(), 42, 0, "trace.log", path, "trace", model.SendOptions{})
	if err != nil {
		t.Fatalf("SendDocument failed: %v", err)
	}
	if got, want := messageID, int64(555); got != want {
		t.Fatalf("messageID = %d, want %d", got, want)
	}
}

func TestBotDeliverDirectResponseSendsSilentMessage(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":777,"chat":{"id":42,"type":"private"},"text":"menu"}}`))
	}))
	defer server.Close()

	root := t.TempDir()
	service, err := daemon.New(config.Config{Paths: config.Paths{
		Home:    root,
		DataDir: filepath.Join(root, "data"),
		LogDir:  filepath.Join(root, "logs"),
		DBPath:  filepath.Join(root, "data", "state.sqlite"),
	}})
	if err != nil {
		t.Fatalf("daemon.New failed: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client, service: service}
	if err := bot.deliverDirectResponse(context.Background(), 42, 0, &daemon.DirectResponse{Text: "menu"}); err != nil {
		t.Fatalf("deliverDirectResponse failed: %v", err)
	}
	if got, ok := captured["disable_notification"].(bool); !ok || !got {
		t.Fatalf("disable_notification = %#v, want true", captured["disable_notification"])
	}
}
