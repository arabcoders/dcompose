package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLogsAPI(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	slog.Warn("diagnostic", "stderr", "first line\nRegistry DENIED", "image", "example:latest")
	for _, query := range []string{"", "?levels=warn&node=server&logger=server&search=registry"} {
		w := perform(t, s, "GET", "/api/logs"+query, "")
		var page logPage
		if http.StatusOK != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &page) || 1 != len(page.Entries) ||
			"" == page.Entries[0].Rendered ||
			`"first line\nRegistry DENIED"` != string(page.Entries[0].Fields["stderr"]) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	s.config.Auth.apiKey = "test-api-key"
	r := httptest.NewRequest("GET", "/api/logs", nil)
	r.Header.Set("Authorization", "Bearer test-api-key")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if http.StatusOK != w.Code {
		t.Fatal("API key authentication", w.Code)
	}
	if 1 != len(historyPage(t, s.logHistory, logFilter{}).Entries) {
		t.Fatal("log viewer requests recursively logged")
	}
	for _, path := range []string{"/api/logs", "/api/logs/stream"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if http.StatusUnauthorized != w.Code {
			t.Fatal(path, w.Code)
		}
	}
}

func TestClearActiveLog(t *testing.T) {
	const path = "/api/logs/active"
	s := testServer(t, nil)
	startLogHistory(t, s)
	slog.Warn("active fixture")
	for _, tc := range []struct {
		withSession bool
		origin      string
		status      int
	}{
		{status: http.StatusUnauthorized},
		{withSession: true, origin: "https://other.example", status: http.StatusForbidden},
	} {
		r := httptest.NewRequest("DELETE", path, nil)
		if tc.withSession {
			r.AddCookie(testSession(t, s))
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if tc.status != w.Code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if page := historyPage(t, s.logHistory, logFilter{summary: true}); 1 != len(page.Entries) {
		t.Fatal("unauthorized request cleared the active file", page)
	}

	response := perform(t, s, "DELETE", path, "")
	var result struct {
		Status string `json:"status"`
	}
	if http.StatusOK != response.Code || nil != json.Unmarshal(response.Body.Bytes(), &result) || "ok" != result.Status {
		t.Fatal(response.Code, response.Body.String())
	}
	if active, err := os.ReadFile(s.logHistory.path); nil != err || 0 != len(active) {
		t.Fatal("active file was not emptied", len(active), err)
	}
	full := historyPage(t, s.logHistory, logFilter{})
	summary := historyPage(t, s.logHistory, logFilter{summary: true})
	if 1 != len(full.Entries) || 0 != len(summary.Entries) || full.LatestID != summary.LatestID {
		t.Fatal("archived logs should only appear in full history", full, summary)
	}

	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	stream, cancel := openLogStream(t, app, s, "?summary=true&limit=5", "")
	defer cancel()
	reader := bufio.NewReader(stream.Body)
	if status := nextLogStatus(t, reader); nil != status.OldestID {
		t.Fatal("summary backlog included archived logs", status)
	}
	slog.Warn("new fixture")
	if entry := nextLogEvent(t, reader); full.LatestID >= entry.ID {
		t.Fatal("new logs did not follow the previous cursor", entry)
	}
}

func TestPackLogBackups(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	history := s.logHistory
	archive := func() {
		t.Helper()
		if err := history.archiveActive(); nil != err {
			t.Fatal(err)
		}
	}
	read := func(suffix string) []byte {
		t.Helper()
		data, err := os.ReadFile(history.path + suffix)
		if nil != err {
			t.Fatal(err)
		}
		return data
	}
	var expectedIDs []int64
	write := func() {
		t.Helper()
		slog.Warn("fixture")
		expectedIDs = append(expectedIDs, historyPage(t, history, logFilter{}).LatestID)
	}

	write()
	first := read("")
	archive()
	write()
	second := read("")
	history.maxBytes = int64(len(first) + len(second))
	archive()
	archive()
	packed := read(".1")
	if !bytes.Equal(append(first, second...), packed) || 0 != len(read("")) {
		t.Fatal("clears did not pack both batches in the newest backup")
	}
	if _, err := os.Stat(history.path + ".2"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a fitting or empty clear used a backup slot", err)
	}

	write()
	third := read("")
	archive()
	if !bytes.Equal(packed, read(".2")) || !bytes.Equal(third, read(".1")) || 0 != len(read("")) {
		t.Fatal("a full backup did not rotate")
	}
	page := historyPage(t, history, logFilter{})
	ids := make([]int64, 0, len(page.Entries))
	for _, entry := range page.Entries {
		ids = append(ids, entry.ID)
	}
	if !slices.Equal(ids, expectedIDs) {
		t.Fatal("archived entries are out of order", ids, expectedIDs)
	}
}

func TestActiveLogPage(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	get := func(query string) logPage {
		t.Helper()
		response := perform(t, s, "GET", "/api/logs?"+query, "")
		var page logPage
		if http.StatusOK != response.Code || nil != json.Unmarshal(response.Body.Bytes(), &page) {
			t.Fatal(response.Code, response.Body.String())
		}
		return page
	}

	slog.Warn("archive fixture")
	archivedID := get("active=true").LatestID
	if response := perform(t, s, "DELETE", "/api/logs/active", ""); http.StatusOK != response.Code {
		t.Fatal(response.Code, response.Body.String())
	}
	empty := get("active=true")
	if 0 != len(empty.Entries) || nil == empty.NextBefore || archivedID != empty.LatestID {
		t.Fatal("empty active page lost access to archived logs", empty)
	}
	older := get("before=" + strconv.FormatInt(*empty.NextBefore, 10))
	if 1 != len(older.Entries) || archivedID != older.Entries[0].ID || nil != older.NextBefore {
		t.Fatal("older page did not return the archive", older)
	}

	slog.Info("active fixture")
	active := get("active=true")
	if 1 != len(active.Entries) || active.LatestID != active.Entries[0].ID ||
		nil == active.NextBefore || active.Entries[0].ID != *active.NextBefore {
		t.Fatal("active page mixed active and archived entries", active)
	}
	if filtered := get("active=true&levels=info"); nil != filtered.NextBefore {
		t.Fatal("archive cursor ignored the active filter", filtered)
	}
}

func TestLogQueries(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	for _, query := range []string{
		"limit=0", "limit=501", "limit=no", "before=1&after=2", "after=-1", "after=9007199254740992",
		"before=no", "after=", "limit=1&limit=2", "levels=trace", "levels=", "unknown=value",
	} {
		w := perform(t, s, "GET", "/api/logs?"+query, "")
		if http.StatusBadRequest != w.Code {
			t.Fatal(query, w.Code)
		}
	}
	for _, path := range []string{"/api/logs", "/api/logs?levels=warning,error&limit=500&after=0", "/api/logs?before=0"} {
		w := perform(t, s, "GET", path, "")
		var page logPage
		if http.StatusOK != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &page) || nil == page.Entries || 0 != len(page.Entries) ||
			nil != page.OldestID {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w := perform(t, s, "GET", "/api/logs/stream?before=1", "")
	if http.StatusBadRequest != w.Code {
		t.Fatal(w.Code)
	}
}

func nextLogEvent(t *testing.T, reader *bufio.Reader) logEntry {
	t.Helper()
	var entry logEntry
	var event, id string
	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			t.Fatal(err)
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimSpace(strings.TrimPrefix(line, "id: "))
		case strings.HasPrefix(line, "data: "):
			if "log_status" == event {
				continue
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &entry); nil != err {
				t.Fatal(err)
			}
			if "log_lines" != event || strconv.FormatInt(entry.ID, 10) != id {
				t.Fatal("SSE framing", event, id, entry.ID)
			}
			return entry
		}
	}
}

func nextLogStatus(t *testing.T, reader *bufio.Reader) logStatus {
	t.Helper()
	var status logStatus
	var event string
	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			t.Fatal(err)
		}
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			event = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			if "log_status" != event || nil != json.Unmarshal([]byte(value), &status) {
				t.Fatal("missing stream retention metadata", event, line)
			}
			return status
		}
	}
}

func openLogStream(t *testing.T, app *httptest.Server, s *Server, query, lastID string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, "GET", app.URL+"/api/logs/stream"+query, nil)
	if nil != err {
		t.Fatal(err)
	}
	r.AddCookie(testSession(t, s))
	r.Header.Set("Accept", "text/event-stream")
	if "" != lastID {
		r.Header.Set("Last-Event-ID", lastID)
	}
	response, err := app.Client().Do(r)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if http.StatusOK != response.StatusCode || "text/event-stream" != response.Header.Get("Content-Type") {
		t.Fatal(response.Status, response.Header)
	}
	return response, cancel
}

func TestLogSummary(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	for index := range 6 {
		slog.Warn("warning fixture", "sequence", index)
	}
	slog.Info("update fixture", "sequence", 6, "automatic", true, "status", "succeeded")
	for range 10 {
		slog.Info("noise fixture", "automatic", true, "status", "running")
	}
	const query = "?summary=true&limit=5"
	response := perform(t, s, "GET", "/api/logs"+query, "")
	var page logPage
	if http.StatusOK != response.Code || nil != json.Unmarshal(response.Body.Bytes(), &page) || 5 != len(page.Entries) {
		t.Fatal(response.Code, response.Body.String())
	}
	for i, entry := range page.Entries {
		if strconv.Itoa(i+2) != string(entry.Fields["sequence"]) {
			t.Fatal("summary did not return the latest matches", page.Entries)
		}
	}
	for _, query := range []string{"?limit=1", "?limit=1&summary=false", "?limit=1&summary=", "?limit=1&summary=other"} {
		response := perform(t, s, "GET", "/api/logs"+query, "")
		var ordinary logPage
		if http.StatusOK != response.Code || nil != json.Unmarshal(response.Body.Bytes(), &ordinary) ||
			1 != len(ordinary.Entries) || page.LatestID != ordinary.Entries[0].ID {
			t.Fatal("expected unfiltered logs", query, response.Code, response.Body.String())
		}
	}
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	stream, cancel := openLogStream(t, app, s, query, "")
	defer cancel()
	reader := bufio.NewReader(stream.Body)
	for _, entry := range page.Entries {
		if got := nextLogEvent(t, reader); entry.ID != got.ID {
			t.Fatal("stream and page returned different summaries", got, entry)
		}
	}
	slog.Info("noise fixture", "automatic", false, "status", "succeeded")
	slog.Error("error fixture", "sequence", 7)
	if got := nextLogEvent(t, reader); "7" != string(got.Fields["sequence"]) {
		t.Fatal("summary did not filter live events", got)
	}
}

func TestLogStream(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	slog.Warn("first", "image", "example:latest")
	slog.Info("gap")
	slog.Warn("second", "image", "example:latest")
	page := historyPage(t, s.logHistory, logFilter{})
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	response, cancel := openLogStream(t, app, s, "?after=0&levels=warn&node=server&logger=server&search=example", "")
	reader := bufio.NewReader(response.Body)
	status := nextLogStatus(t, reader)
	if nil == status.OldestID || page.Entries[0].ID != *status.OldestID || status.Truncated {
		t.Fatal("initial stream retention metadata", status)
	}
	first := nextLogEvent(t, reader)
	second := nextLogEvent(t, reader)
	if page.Entries[0].ID != first.ID || page.Entries[2].ID != second.ID || page.Entries[0].Rendered != first.Rendered ||
		page.Entries[2].Rendered != second.Rendered {
		t.Fatal("filtered backlog", first, second)
	}
	slog.Info("live gap")
	slog.Warn("live", "image", "example:latest")
	live := nextLogEvent(t, reader)
	if second.ID >= live.ID || "live" != live.Message || "" == live.Rendered {
		t.Fatal("live entry", live)
	}
	cancel()
	_, _ = io.Copy(io.Discard, response.Body)
	slog.Warn("reconnect", "image", "example:latest")
	response, cancel = openLogStream(t, app, s, "?after=0&levels=warning", strconv.FormatInt(live.ID, 10))
	entry := nextLogEvent(t, bufio.NewReader(response.Body))
	if live.ID >= entry.ID || "reconnect" != entry.Message {
		t.Fatal("reconnect cursor", entry)
	}
	cancel()
}

func TestLogStreamClose(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	response, _ := openLogStream(t, app, s, "", "")
	s.Close()
	if _, err := io.Copy(io.Discard, response.Body); nil != err {
		t.Fatal("stream did not close on server shutdown", err)
	}
}

func TestLogStreamRetention(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	s.logHistory.maxBytes = 1
	slog.Warn("first")
	firstID := historyPage(t, s.logHistory, logFilter{}).LatestID
	for index := 0; 5 > index; index++ {
		slog.Warn("retained", "index", index)
	}
	page := historyPage(t, s.logHistory, logFilter{})
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	response, cancel := openLogStream(t, app, s, "?after="+strconv.FormatInt(firstID, 10), "")
	defer cancel()
	reader := bufio.NewReader(response.Body)
	status := nextLogStatus(t, reader)
	if !status.Truncated || nil == status.OldestID || *page.OldestID != *status.OldestID {
		t.Fatal("missing retention gap", status, page)
	}
	entry := nextLogEvent(t, reader)
	if *status.OldestID != entry.ID {
		t.Fatal("backlog not aligned with retention metadata", status, entry)
	}
}
