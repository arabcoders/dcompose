package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func parseLogFilter(r *http.Request, stream bool) (logFilter, error) {
	query := r.URL.Query()
	filter := logFilter{limit: 100, node: query.Get("node"), logger: query.Get("logger"), search: strings.ToLower(query.Get("search")), levels: make(map[string]bool)}
	for key, values := range query {
		switch key {
		case "limit", "before", "after", "levels", "node", "logger", "search":
		default:
			return filter, fmt.Errorf("unknown log query parameter: %s", key)
		}
		if 1 != len(values) {
			return filter, fmt.Errorf("log query parameters must not be repeated")
		}
	}
	if query.Has("limit") {
		value, err := strconv.Atoi(query.Get("limit"))
		if nil != err || 1 > value || 500 < value {
			return filter, fmt.Errorf("limit must be between 1 and 500")
		}
		filter.limit = value
	}
	if stream && "" != r.Header.Get("Last-Event-ID") {
		query.Set("after", r.Header.Get("Last-Event-ID"))
	}
	if query.Has("before") && (query.Has("after") || stream) {
		return filter, fmt.Errorf("before cannot be combined with after or streaming")
	}
	for key, target := range map[string]*int64{"before": &filter.before, "after": &filter.after} {
		if !query.Has(key) {
			continue
		}
		value, err := strconv.ParseInt(query.Get(key), 10, 64)
		if nil != err || 0 > value || 9_007_199_254_740_991 < value {
			return filter, fmt.Errorf("%s must be a nonnegative safe integer", key)
		}
		*target = value
	}
	if query.Has("levels") {
		for _, level := range strings.Split(query.Get("levels"), ",") {
			level = strings.ToLower(strings.TrimSpace(level))
			if "warn" == level {
				level = "warning"
			}
			switch level {
			case "debug", "info", "warning", "error":
				filter.levels[level] = true
			default:
				return filter, fmt.Errorf("levels must contain debug, info, warning or error")
			}
		}
	}
	return filter, nil
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	filter, err := parseLogFilter(r, false)
	if nil != err {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if nil == s.logHistory {
		apiError(w, http.StatusServiceUnavailable, "Operational log history is not enabled.")
		return
	}
	page, err := s.logHistory.query(filter)
	if nil != err {
		apiError(w, http.StatusInternalServerError, "Could not read operational log history.")
		return
	}
	jsonResponse(w, http.StatusOK, page)
}

func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	filter, err := parseLogFilter(r, true)
	if nil != err {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if nil == s.logHistory {
		apiError(w, http.StatusServiceUnavailable, "Operational log history is not enabled.")
		return
	}
	subscriber, backlog, status, err := s.logHistory.subscribe(filter)
	if nil != err {
		apiError(w, http.StatusServiceUnavailable, "Could not open operational log stream.")
		return
	}
	defer s.logHistory.unsubscribe(subscriber)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(data []byte) bool {
		// A stalled reader must not keep a stream alive through server shutdown.
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := w.Write(data); nil != err {
			return false
		}
		return nil == controller.Flush()
	}
	writeEntry := func(entry logEntry) bool {
		data, err := json.Marshal(entry)
		if nil != err {
			return false
		}
		return write([]byte(fmt.Sprintf("event: log_lines\nid: %d\ndata: %s\n\n", entry.ID, data)))
	}
	if !write([]byte(": connected\n\n")) {
		return
	}
	metadata, _ := json.Marshal(status)
	if !write([]byte(fmt.Sprintf("event: log_status\ndata: %s\n\n", metadata))) {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for _, entry := range backlog {
		select {
		case <-r.Context().Done():
			return
		case <-s.logHistory.done:
			return
		case <-ticker.C:
			if !s.authenticated(r) {
				return
			}
		default:
		}
		if !writeEntry(entry) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.logHistory.done:
			return
		case <-ticker.C:
			if !s.authenticated(r) || !write([]byte(": heartbeat\n\n")) {
				return
			}
		case entry, ok := <-subscriber:
			if !ok {
				return
			}
			if filter.after < entry.ID && filter.matches(entry) && !writeEntry(entry) {
				return
			}
		}
	}
}
