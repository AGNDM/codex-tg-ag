package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const maxTelegramDocumentBytes int64 = 50_000_000

type fileDeliveryProtocol struct {
	Version int
	Nonce   string
}

func withFileDeliveryInstructions(text, nonce string) string {
	return strings.TrimSpace(text) + "\n\n[Codex Telegram file delivery]\n" +
		"If the operator explicitly asks you to send a Project file to Telegram, append one top-level fenced block named codex-tg-file to your final answer. " +
		"Use strict JSON with version 1, nonce " + fmt.Sprintf("%q", nonce) + ", and files containing Project-relative path and optional caption. " +
		"Do not use this block merely to explain or quote the protocol. Say that you submitted delivery; the bridge reports the actual result."
}

func withFileDeliveryProtocol(text string, protocol fileDeliveryProtocol, role string) string {
	if protocol.Version == 2 {
		return projectRuntimePrompt(text, role)
	}
	return withFileDeliveryInstructions(text, protocol.Nonce)
}

func (s *Service) fileDeliveryPromptForTurn(ctx context.Context, threadID, turnID, projectText, legacyText, role string) (string, fileDeliveryProtocol) {
	protocol := fileDeliveryProtocol{Version: 1, Nonce: randomToken()}
	if origin, err := s.store.GetTelegramTurnOrigin(ctx, threadID, turnID); err == nil && origin != nil {
		protocol.Version = origin.DeliveryProtocolVersion
		protocol.Nonce = origin.DeliveryNonce
	}
	if protocol.Version != 2 && strings.TrimSpace(protocol.Nonce) == "" {
		protocol.Nonce = randomToken()
	}
	text := legacyText
	if protocol.Version == 2 {
		text = projectText
	}
	return withFileDeliveryProtocol(text, protocol, role), protocol
}

type fileDeliveryDirective struct {
	Version int                   `json:"version"`
	Files   []fileDeliveryRequest `json:"files"`
}

type fileDeliveryWireDirective struct {
	Version int                   `json:"version"`
	Nonce   json.RawMessage       `json:"nonce"`
	Files   []fileDeliveryRequest `json:"files"`
}

type fileDeliveryRequest struct {
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

func parseFileDeliveryFinal(text string, protocol fileDeliveryProtocol) (string, *fileDeliveryDirective, error) {
	const open = "```codex-tg-file\n"
	const close = "\n```"
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	outerFence := ""
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if outerFence != "" {
			if closesMarkdownFence(trimmed, outerFence) {
				outerFence = ""
			}
			continue
		}
		if line == strings.TrimSuffix(open, "\n") {
			if start >= 0 {
				return text, nil, errors.New("only one file delivery block is allowed")
			}
			start = index
			continue
		}
		if start >= 0 && end < 0 && line == strings.TrimPrefix(close, "\n") {
			end = index
			continue
		}
		if delimiter := markdownFenceDelimiter(trimmed); delimiter != "" {
			outerFence = delimiter
			continue
		}
	}
	if start < 0 {
		return text, nil, nil
	}
	if end <= start+1 {
		return text, nil, errors.New("file delivery block is incomplete")
	}
	body := strings.Join(lines[start+1:end], "\n")
	decoder := json.NewDecoder(bytes.NewBufferString(body))
	decoder.DisallowUnknownFields()
	var wire fileDeliveryWireDirective
	if err := decoder.Decode(&wire); err != nil {
		return text, nil, fmt.Errorf("invalid file delivery JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return text, nil, errors.New("file delivery block contains trailing JSON")
	}
	if wire.Version != protocol.Version || (wire.Version != 1 && wire.Version != 2) {
		return text, nil, errors.New("unsupported file delivery protocol version")
	}
	if wire.Version == 2 && wire.Nonce != nil {
		return text, nil, errors.New("file delivery protocol v2 does not accept a nonce")
	}
	var nonce string
	if wire.Version == 1 {
		if wire.Nonce == nil || json.Unmarshal(wire.Nonce, &nonce) != nil || strings.TrimSpace(protocol.Nonce) == "" || nonce != protocol.Nonce {
			return text, nil, errors.New("file delivery nonce does not match this turn")
		}
	}
	directive := fileDeliveryDirective{Version: wire.Version, Files: wire.Files}
	if len(directive.Files) == 0 || len(directive.Files) > 3 {
		return text, nil, errors.New("file delivery requires between one and three files")
	}
	for index := range directive.Files {
		directive.Files[index].Path = strings.TrimSpace(directive.Files[index].Path)
		directive.Files[index].Caption = strings.TrimSpace(directive.Files[index].Caption)
		if directive.Files[index].Path == "" {
			return text, nil, errors.New("file delivery path is required")
		}
		if len([]rune(directive.Files[index].Caption)) > 512 {
			return text, nil, errors.New("file delivery caption is too long")
		}
	}
	visibleLines := append([]string(nil), lines[:start]...)
	visibleLines = append(visibleLines, lines[end+1:]...)
	return strings.TrimSpace(strings.Join(visibleLines, "\n")), &directive, nil
}

func markdownFenceDelimiter(line string) string {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	count := 0
	for count < len(line) && line[count] == line[0] {
		count++
	}
	if count < 3 {
		return ""
	}
	return line[:count]
}

func closesMarkdownFence(line, delimiter string) bool {
	if line == "" || line[0] != delimiter[0] {
		return false
	}
	count := 0
	for count < len(line) && line[count] == delimiter[0] {
		count++
	}
	return count >= len(delimiter) && strings.TrimSpace(line[count:]) == ""
}

func openProjectDeliveryFile(projectRoot, relativePath string, maxBytes int64) (*os.File, os.FileInfo, error) {
	projectRoot = filepath.Clean(strings.TrimSpace(projectRoot))
	if !filepath.IsAbs(projectRoot) {
		return nil, nil, errors.New("Project root must be absolute")
	}
	relativePath = filepath.ToSlash(strings.TrimSpace(relativePath))
	if relativePath == "" || strings.HasPrefix(relativePath, "/") {
		return nil, nil, errors.New("file delivery path must be Project-relative")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relativePath)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, nil, errors.New("file delivery path escapes the Project")
	}
	if sensitiveDeliveryPath(clean) {
		return nil, nil, errors.New("file delivery path is protected")
	}
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(clean, "/")
	for index := range parts {
		info, statErr := root.Lstat(filepath.FromSlash(strings.Join(parts[:index+1], "/")))
		if statErr != nil {
			_ = root.Close()
			return nil, nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, nil, errors.New("file delivery does not allow symlinks")
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			_ = root.Close()
			return nil, nil, errors.New("file delivery requires a regular file")
		}
	}
	file, err := root.Open(filepath.FromSlash(clean))
	_ = root.Close()
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errors.New("file delivery requires a regular file")
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		_ = file.Close()
		return nil, nil, fmt.Errorf("file exceeds Telegram document limit of %d bytes", maxBytes)
	}
	return file, info, nil
}

func sensitiveDeliveryPath(path string) bool {
	parts := strings.Split(strings.ToLower(filepath.ToSlash(path)), "/")
	for _, part := range parts {
		if part == ".git" || part == ".codex" || part == ".codex-tg" || part == ".env" || strings.HasPrefix(part, ".env.") || part == "config.env" {
			return true
		}
		if strings.HasSuffix(part, ".sqlite") || strings.Contains(part, ".sqlite-") || strings.HasSuffix(part, ".db") || strings.HasSuffix(part, ".session") || strings.Contains(part, ".session-") {
			return true
		}
	}
	return false
}

type streamingDocumentSender interface {
	SendDocumentStream(ctx context.Context, chatID, topicID int64, fileName string, reader io.Reader, caption string, options model.SendOptions) (int64, error)
}

func (s *Service) processFinalFileDeliveries(ctx context.Context, sender Sender, thread model.Thread, snapshot *appserver.ThreadReadSnapshot) string {
	visible := strings.TrimSpace(snapshot.LatestFinalText)
	if !strings.EqualFold(strings.TrimSpace(snapshot.LatestTurnStatus), "completed") {
		return visible
	}
	origin, err := s.store.GetTelegramTurnOrigin(ctx, thread.ID, snapshot.LatestTurnID)
	if err != nil || origin == nil {
		return visible
	}
	protocol := fileDeliveryProtocol{Version: origin.DeliveryProtocolVersion, Nonce: origin.DeliveryNonce}
	parsedVisible, directive, parseErr := parseFileDeliveryFinal(visible, protocol)
	if parseErr != nil {
		return appendDeliveryStatus(visible, "File delivery request invalid: "+parseErr.Error())
	}
	if directive == nil {
		return visible
	}
	visible = parsedVisible
	requests := make([]model.FileDelivery, 0, len(directive.Files))
	for index, file := range directive.Files {
		requests = append(requests, model.FileDelivery{DirectiveIndex: index, FilePath: file.Path, Caption: file.Caption})
	}
	claimed, err := s.store.ClaimFileDeliveries(ctx, thread.ID, snapshot.LatestTurnID, snapshot.LatestFinalFP, requests)
	if err != nil {
		return appendDeliveryStatus(visible, "File delivery conflict: "+err.Error())
	}
	streamer, ok := sender.(streamingDocumentSender)
	for _, delivery := range claimed {
		if !ok {
			_ = s.store.UpdateFileDelivery(ctx, thread.ID, snapshot.LatestTurnID, delivery.DirectiveIndex, model.FileDeliveryFailed, 0, "Telegram sender does not support streaming documents")
			continue
		}
		file, info, openErr := openProjectDeliveryFile(thread.CWD, delivery.FilePath, maxTelegramDocumentBytes)
		if openErr != nil {
			_ = s.store.UpdateFileDelivery(ctx, thread.ID, snapshot.LatestTurnID, delivery.DirectiveIndex, model.FileDeliveryFailed, 0, openErr.Error())
			continue
		}
		caption := s.visualHeader(ctx, "File", thread, snapshot.LatestTurnID)
		if delivery.Caption != "" {
			caption += "\n" + delivery.Caption
		}
		messageID, sendErr := streamer.SendDocumentStream(ctx, origin.ChatID, origin.TopicID, filepath.Base(info.Name()), io.LimitReader(file, info.Size()), caption, silentSendOptions())
		_ = file.Close()
		if sendErr != nil {
			_ = s.store.UpdateFileDelivery(ctx, thread.ID, snapshot.LatestTurnID, delivery.DirectiveIndex, model.FileDeliveryUnknown, 0, sendErr.Error())
			continue
		}
		if messageID == 0 {
			_ = s.store.UpdateFileDelivery(ctx, thread.ID, snapshot.LatestTurnID, delivery.DirectiveIndex, model.FileDeliveryUnknown, 0, "Telegram returned no message id")
			continue
		}
		_ = s.store.UpdateFileDelivery(ctx, thread.ID, snapshot.LatestTurnID, delivery.DirectiveIndex, model.FileDeliverySent, messageID, "")
		_ = s.store.PutMessageRoute(ctx, model.MessageRoute{ChatID: origin.ChatID, TopicID: origin.TopicID, MessageID: messageID, ThreadID: thread.ID, TurnID: snapshot.LatestTurnID, EventID: snapshot.LatestFinalFP, CreatedAt: model.NowString()})
	}
	return visible
}

func (s *Service) renderFileDeliveryResult(ctx context.Context, threadID, turnID, finalText string) string {
	visible := strings.TrimSpace(finalText)
	origin, err := s.store.GetTelegramTurnOrigin(ctx, threadID, turnID)
	if err == nil && origin != nil {
		protocol := fileDeliveryProtocol{Version: origin.DeliveryProtocolVersion, Nonce: origin.DeliveryNonce}
		if stripped, directive, parseErr := parseFileDeliveryFinal(visible, protocol); parseErr == nil && directive != nil {
			visible = stripped
		}
	}
	items, listErr := s.store.ListFileDeliveries(ctx, threadID, turnID)
	if listErr != nil {
		return appendDeliveryStatus(visible, "File delivery status unavailable.")
	}
	for _, item := range items {
		line := fmt.Sprintf("File %s: %s", item.FilePath, item.Status)
		visible = appendDeliveryStatus(visible, line)
	}
	return visible
}

func appendDeliveryStatus(text, status string) string {
	if strings.TrimSpace(text) == "" {
		return strings.TrimSpace(status)
	}
	return strings.TrimSpace(text) + "\n\n" + strings.TrimSpace(status)
}
