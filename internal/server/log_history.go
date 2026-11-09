package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arabcoders/dcompose/internal/logging"
)

const logHistoryMaxBytes int64 = 10_000_000
const logHistoryBackups = 3

// JSON values remain raw at the persistence/API boundary so integer fields,
// nested groups and arrays are not converted to floating point values.
type logEntry struct {
	ID        int64                      `json:"id"`
	Timestamp time.Time                  `json:"timestamp"`
	Level     string                     `json:"level"`
	Logger    string                     `json:"logger"`
	Message   string                     `json:"message"`
	Rendered  string                     `json:"rendered"`
	Fields    map[string]json.RawMessage `json:"fields"`
	Source    *slog.Source               `json:"source,omitempty"`
}

type logHistory struct {
	mu          sync.Mutex
	path        string
	file        *os.File
	size        int64
	maxBytes    int64
	latestID    int64
	closed      bool
	done        chan struct{}
	subscribers map[chan logEntry]struct{}
}

// StartLogHistory explicitly installs production log capture. New remains inert.
// The existing handler still controls the log level and writes to stderr.
func (s *Server) StartLogHistory() error {
	if nil != s.logHistory {
		return nil
	}
	config, _ := s.snapshot()
	history, err := openLogHistory(filepath.Join(filepath.Dir(config.path), "logs", "dcompose.log"))
	if nil != err {
		return err
	}
	s.logHistory = history
	s.logPrevious = slog.Default()
	s.logLogger = slog.New(newHistoryHandler(s.logPrevious.Handler(), history))
	slog.SetDefault(s.logLogger)
	return nil
}

func (s *Server) closeLogHistory() {
	if nil == s.logHistory {
		return
	}
	if slog.Default() == s.logLogger {
		slog.SetDefault(s.logPrevious)
	}
	_ = s.logHistory.close()
}

func openLogHistory(path string) (*logHistory, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); nil != err {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); nil != err {
		return nil, err
	}
	h := &logHistory{path: path, maxBytes: logHistoryMaxBytes, done: make(chan struct{}), subscribers: make(map[chan logEntry]struct{})}
	if err := h.walkLocked(func(entry logEntry) { h.latestID = max(h.latestID, entry.ID) }); nil != err {
		return nil, err
	}
	if err := h.openFile(); nil != err {
		return nil, err
	}
	// Separate a partial final record left by an interrupted write from new logs.
	if 0 < h.size {
		var tail [1]byte
		if _, err := h.file.ReadAt(tail[:], h.size-1); nil != err {
			_ = h.file.Close()
			return nil, err
		}
		if '\n' != tail[0] {
			if _, err := h.file.Write([]byte{'\n'}); nil != err {
				_ = h.file.Close()
				return nil, err
			}
			h.size++
		}
	}
	return h, nil
}

func (h *logHistory) openFile() error {
	file, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
	if nil != err {
		return err
	}
	if err := file.Chmod(0600); nil != err {
		_ = file.Close()
		return err
	}
	stat, err := file.Stat()
	if nil != err {
		_ = file.Close()
		return err
	}
	h.file, h.size = file, stat.Size()
	return nil
}

func (h *logHistory) rotateLocked() error {
	if err := h.file.Close(); nil != err {
		return err
	}
	for index := logHistoryBackups; 1 <= index; index-- {
		source := h.path
		if 1 < index {
			source += "." + strconv.Itoa(index-1)
		}
		if err := os.Rename(source, h.path+"."+strconv.Itoa(index)); nil != err && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return h.openFile()
}

// walkLocked visits retained files in append order without caching the history.
func (h *logHistory) walkLocked(visit func(logEntry)) error {
	for index := logHistoryBackups; 0 <= index; index-- {
		path := h.path
		if 0 < index {
			path += "." + strconv.Itoa(index)
		}
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if nil != err {
			return err
		}
		if err := file.Chmod(0600); nil != err {
			_ = file.Close()
			return err
		}
		reader := bufio.NewReader(file)
		for {
			line, err := reader.ReadBytes('\n')
			var entry logEntry
			if nil == json.Unmarshal(line, &entry) && 0 < entry.ID {
				if "" == entry.Rendered {
					entry.Rendered = renderStoredLog(entry)
				}
				visit(entry)
			}
			if nil != err {
				_ = file.Close()
				if !errors.Is(err, io.EOF) {
					return err
				}
				break
			}
		}
	}
	return nil
}

// Write receives complete records encoded by the standard slog JSON handler.
func (h *logHistory) Write(data []byte) (int, error) {
	return h.writeRecord(data, "")
}

func (h *logHistory) writeRecord(data []byte, rendered string) (int, error) {
	var record struct {
		Time    time.Time                  `json:"time"`
		Level   slog.Level                 `json:"level"`
		Message string                     `json:"msg"`
		Fields  map[string]json.RawMessage `json:"fields"`
		Source  *slog.Source               `json:"source"`
	}
	if err := json.Unmarshal(data, &record); nil != err {
		return 0, err
	}
	entry := logEntry{Timestamp: record.Time, Level: logLevelName(record.Level), Logger: "server", Message: record.Message, Rendered: rendered, Fields: record.Fields, Source: record.Source}
	if nil == entry.Fields {
		entry.Fields = make(map[string]json.RawMessage)
	}
	if "true" == string(entry.Fields["_from_node"]) {
		var nodeID string
		_ = json.Unmarshal(entry.Fields["node_id"], &nodeID)
		entry.Logger = "node." + nodeID
		// PC=0 on forwarded records: only the node's supplied callsite is valid.
		entry.Source = nil
		_ = json.Unmarshal(entry.Fields["_node_source"], &entry.Source)
	}
	delete(entry.Fields, "_from_node")
	delete(entry.Fields, "_node_source")
	if "" == entry.Rendered {
		entry.Rendered = renderStoredLog(entry)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return len(data), nil
	}
	entry.ID = max(h.latestID+1, time.Now().UnixMicro())
	line, err := json.Marshal(entry)
	if nil != err {
		return 0, err
	}
	line = append(line, '\n')
	if 0 < h.size && h.maxBytes < h.size+int64(len(line)) {
		if err := h.rotateLocked(); nil != err {
			return 0, err
		}
	}
	written, err := h.file.Write(line)
	h.size += int64(written)
	if nil != err {
		return 0, err
	}
	h.latestID = entry.ID
	for subscriber := range h.subscribers {
		select {
		case subscriber <- entry:
		default:
			close(subscriber)
			delete(h.subscribers, subscriber)
		}
	}
	return len(data), nil
}

func logLevelName(level slog.Level) string {
	switch {
	case slog.LevelError <= level:
		return "error"
	case slog.LevelWarn <= level:
		return "warning"
	case slog.LevelInfo <= level:
		return "info"
	default:
		return "debug"
	}
}

func (h *logHistory) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	close(h.done)
	for subscriber := range h.subscribers {
		close(subscriber)
		delete(h.subscribers, subscriber)
	}
	return errors.Join(h.file.Sync(), h.file.Close())
}

type historyHandler struct {
	local   slog.Handler
	render  slog.Handler
	store   slog.Handler
	output  *historyOutput
	history *logHistory
}

type historyOutput struct {
	mu   sync.Mutex
	text bytes.Buffer
	json bytes.Buffer
}

func newHistoryHandler(local slog.Handler, history *logHistory) *historyHandler {
	output := &historyOutput{}
	return &historyHandler{
		local: local, history: history, output: output,
		render: logging.NewConsoleHandler(&output.text, nil),
		store:  slog.NewJSONHandler(&output.json, &slog.HandlerOptions{AddSource: true, Level: slog.LevelDebug}).WithGroup("fields"),
	}
}

func (h *historyHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.local.Enabled(ctx, level)
}

func (h *historyHandler) Handle(ctx context.Context, record slog.Record) error {
	localErr := h.local.Handle(ctx, record)
	// Bound handler clones share their buffers. Keep rendering, encoding and
	// persistence together so concurrent records cannot exchange rendered text.
	h.output.mu.Lock()
	defer h.output.mu.Unlock()
	h.output.text.Reset()
	h.output.json.Reset()
	renderErr := h.render.Handle(ctx, record)
	encodeErr := h.store.Handle(ctx, record)
	if nil != encodeErr {
		return errors.Join(localErr, renderErr, encodeErr)
	}
	_, storeErr := h.history.writeRecord(h.output.json.Bytes(), h.output.text.String())
	return errors.Join(localErr, renderErr, storeErr)
}

func (h *historyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &historyHandler{local: h.local.WithAttrs(attrs), render: h.render.WithAttrs(attrs), store: h.store.WithAttrs(attrs), output: h.output, history: h.history}
}

func (h *historyHandler) WithGroup(name string) slog.Handler {
	return &historyHandler{local: h.local.WithGroup(name), render: h.render.WithGroup(name), store: h.store.WithGroup(name), output: h.output, history: h.history}
}

// Records written before rendered text was captured still expose a complete
// server-rendered line. Their original attribute order is no longer available.
func renderStoredLog(entry logEntry) string {
	var level slog.Level
	switch entry.Level {
	case "debug":
		level = slog.LevelDebug
	case "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	record := slog.NewRecord(entry.Timestamp, level, entry.Message, 0)
	keys := make([]string, 0, len(entry.Fields))
	for key := range entry.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// any is the legacy JSON boundary, retaining native field values.
		var value any
		decoder := json.NewDecoder(bytes.NewReader(entry.Fields[key]))
		decoder.UseNumber()
		_ = decoder.Decode(&value)
		record.AddAttrs(slog.Any(key, value))
	}
	var output bytes.Buffer
	_ = logging.NewConsoleHandler(&output, nil).Handle(context.Background(), record)
	return output.String()
}

type logFilter struct {
	limit  int
	before int64
	after  int64
	levels map[string]bool
	node   string
	logger string
	search string
}

func (f logFilter) matches(entry logEntry) bool {
	if 0 < len(f.levels) && !f.levels[entry.Level] {
		return false
	}
	if "" != f.logger && f.logger != entry.Logger {
		return false
	}
	if "server" == f.node && "server" != entry.Logger {
		return false
	}
	if "" != f.node && "server" != f.node && "node."+f.node != entry.Logger {
		return false
	}
	if "" != f.search {
		encoded, _ := json.Marshal(entry)
		// any is the JSON search boundary. Decode escaped string values
		// while preserving integer text in diagnostic fields.
		var record any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		_ = decoder.Decode(&record)
		if !strings.Contains(strings.ToLower(fmt.Sprint(record)), f.search) {
			return false
		}
	}
	return true
}

type logPage struct {
	Entries    []logEntry `json:"entries"`
	NextBefore *int64     `json:"next_before"`
	LatestID   int64      `json:"latest_id"`
	OldestID   *int64     `json:"oldest_id"`
	Truncated  bool       `json:"truncated"`
}

func (h *logHistory) query(filter logFilter) (logPage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	page := logPage{Entries: make([]logEntry, 0), LatestID: h.latestID}
	// Only retain a page, even when scanning a large retained history.
	olderMatch := false
	err := h.walkLocked(func(entry logEntry) {
		if nil == page.OldestID {
			id := entry.ID
			page.OldestID = &id
		}
		if !filter.matches(entry) || (0 < filter.before && filter.before <= entry.ID) || (0 < filter.after && entry.ID <= filter.after) {
			return
		}
		if 0 < filter.after {
			if len(page.Entries) < filter.limit {
				page.Entries = append(page.Entries, entry)
			}
			return
		}
		if filter.limit == len(page.Entries) {
			olderMatch = true
			copy(page.Entries, page.Entries[1:])
			page.Entries = page.Entries[:len(page.Entries)-1]
		}
		page.Entries = append(page.Entries, entry)
	})
	if olderMatch && 0 < len(page.Entries) {
		id := page.Entries[0].ID
		page.NextBefore = &id
	}
	if nil != page.OldestID {
		page.Truncated = (0 < filter.after && filter.after < *page.OldestID) || (0 < filter.before && filter.before < *page.OldestID)
	}
	return page, err
}

// Subscribe and snapshot share the append lock, so no entry can fall between them.
func (h *logHistory) subscribe(filter logFilter) (chan logEntry, []logEntry, logStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	status := logStatus{}
	if h.closed {
		return nil, nil, status, fmt.Errorf("log history is closed")
	}
	backlog := make([]logEntry, 0)
	if err := h.walkLocked(func(entry logEntry) {
		if nil == status.OldestID {
			id := entry.ID
			status.OldestID = &id
			status.Truncated = 0 < filter.after && filter.after < id
		}
		if filter.after < entry.ID && filter.matches(entry) {
			backlog = append(backlog, entry)
		}
	}); nil != err {
		return nil, nil, status, err
	}
	subscriber := make(chan logEntry, 128)
	h.subscribers[subscriber] = struct{}{}
	return subscriber, backlog, status, nil
}

type logStatus struct {
	OldestID  *int64 `json:"oldest_id"`
	Truncated bool   `json:"truncated"`
}

func (h *logHistory) unsubscribe(subscriber chan logEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, subscriber)
}
