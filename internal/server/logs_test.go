package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		if http.StatusOK != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &page) || 1 != len(page.Entries) || "" == page.Entries[0].Rendered || `"first line\nRegistry DENIED"` != string(page.Entries[0].Fields["stderr"]) {
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
		if http.StatusOK != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &page) || nil == page.Entries || 0 != len(page.Entries) || nil != page.OldestID {
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
	if page.Entries[0].ID != first.ID || page.Entries[2].ID != second.ID || page.Entries[0].Rendered != first.Rendered || page.Entries[2].Rendered != second.Rendered {
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
