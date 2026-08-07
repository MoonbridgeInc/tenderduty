package dash

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/textileio/go-threads/broadcast"
)

func TestCacheHandlerServeHTTP(t *testing.T) {
	t.Run("devMode", func(t *testing.T) {
		h := CacheHandler{devMode: true}
		req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
		}
		if got := rec.Header().Get("X-Powered-By"); got != "https://github.com/MoonbridgeInc/tenderduty" {
			t.Errorf("X-Powered-By = %q, want %q", got, "https://github.com/MoonbridgeInc/tenderduty")
		}
	})

	t.Run("prod mode", func(t *testing.T) {
		originalRootDir := rootDir
		rootDir = fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("hi")},
		}
		defer func() { rootDir = originalRootDir }()

		h := CacheHandler{devMode: false}
		// http.FileServer redirects any request literally ending in "/index.html" to
		// "/" (a documented stdlib special case) — request "/" so it resolves the
		// index document instead of tripping that redirect.
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
			t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=3600")
		}
		if got := rec.Header().Get("X-Powered-By"); got != "https://github.com/MoonbridgeInc/tenderduty" {
			t.Errorf("X-Powered-By = %q, want %q", got, "https://github.com/MoonbridgeInc/tenderduty")
		}
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Body.String(); got != "hi" {
			t.Errorf("body = %q, want %q", got, "hi")
		}
	})
}

func TestLogsEnabledHandler(t *testing.T) {
	tests := []struct {
		name     string
		hideLogs bool
		want     string
	}{
		{"logs visible", false, `{"enabled":true}`},
		{"logs hidden", true, `{"enabled":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := newMux(newDashCache(), tt.hideLogs, false, &broadcast.Broadcaster{}, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/logsenabled", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSilenceHandler(t *testing.T) {
	fixedTime := time.Unix(1700000000, 0)

	tests := []struct {
		name        string
		method      string
		query       string
		stubErr     error
		wantStatus  int
		wantBody    string
		wantCalled  bool
		wantChain   string
		wantMinutes int
	}{
		{
			name:       "wrong method",
			method:     http.MethodGet,
			query:      "chain=Osmosis&minutes=10",
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   "",
		},
		{
			name:       "missing chain",
			method:     http.MethodPost,
			query:      "minutes=10",
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"chain and minutes (integer) are required"}`,
		},
		{
			name:       "invalid minutes",
			method:     http.MethodPost,
			query:      "chain=Osmosis&minutes=abc",
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"chain and minutes (integer) are required"}`,
		},
		{
			name:        "silence func returns error",
			method:      http.MethodPost,
			query:       "chain=Osmosis&minutes=10",
			stubErr:     errors.New("unknown chain"),
			wantStatus:  http.StatusBadRequest,
			wantBody:    `{"error":"unknown chain"}`,
			wantCalled:  true,
			wantChain:   "Osmosis",
			wantMinutes: 10,
		},
		{
			name:        "happy path",
			method:      http.MethodPost,
			query:       "chain=Osmosis&minutes=10",
			wantStatus:  http.StatusOK,
			wantBody:    `{"chain":"Osmosis","silenced_until":1700000000}`,
			wantCalled:  true,
			wantChain:   "Osmosis",
			wantMinutes: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called bool
			var gotChain string
			var gotMinutes int
			silence := func(chain string, minutes int) (time.Time, error) {
				called = true
				gotChain = chain
				gotMinutes = minutes
				if tt.stubErr != nil {
					return time.Time{}, tt.stubErr
				}
				return fixedTime, nil
			}

			mux := newMux(newDashCache(), false, false, &broadcast.Broadcaster{}, silence, nil)
			req := httptest.NewRequest(tt.method, "/silence?"+tt.query, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if called != tt.wantCalled {
				t.Errorf("silence called = %v, want %v", called, tt.wantCalled)
			}
			if tt.wantCalled {
				if gotChain != tt.wantChain {
					t.Errorf("chain = %q, want %q", gotChain, tt.wantChain)
				}
				if gotMinutes != tt.wantMinutes {
					t.Errorf("minutes = %d, want %d", gotMinutes, tt.wantMinutes)
				}
			}
		})
	}
}

func TestUnsilenceHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		query      string
		stubErr    error
		wantStatus int
		wantBody   string
		wantCalled bool
		wantChain  string
	}{
		{
			name:       "wrong method",
			method:     http.MethodGet,
			query:      "chain=Osmosis",
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   "",
		},
		{
			name:       "missing chain",
			method:     http.MethodPost,
			query:      "",
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"chain is required"}`,
		},
		{
			name:       "unsilence func returns error",
			method:     http.MethodPost,
			query:      "chain=Osmosis",
			stubErr:    errors.New("not silenced"),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"not silenced"}`,
			wantCalled: true,
			wantChain:  "Osmosis",
		},
		{
			name:       "happy path",
			method:     http.MethodPost,
			query:      "chain=Osmosis",
			wantStatus: http.StatusOK,
			wantBody:   `{"ok":true}`,
			wantCalled: true,
			wantChain:  "Osmosis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called bool
			var gotChain string
			unsilence := func(chain string) error {
				called = true
				gotChain = chain
				return tt.stubErr
			}

			mux := newMux(newDashCache(), false, false, &broadcast.Broadcaster{}, nil, unsilence)
			req := httptest.NewRequest(tt.method, "/unsilence?"+tt.query, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if called != tt.wantCalled {
				t.Errorf("unsilence called = %v, want %v", called, tt.wantCalled)
			}
			if tt.wantCalled && gotChain != tt.wantChain {
				t.Errorf("chain = %q, want %q", gotChain, tt.wantChain)
			}
		})
	}
}

func TestLogsEndpoint(t *testing.T) {
	dc := newDashCache()
	mux := newMux(dc, false, false, &broadcast.Broadcaster{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != "[]" {
		t.Fatalf("initial body = %q, want %q", got, "[]")
	}

	dc.appendLog(LogMessage{MsgType: "log", Ts: 1, Msg: "hello"}, false)

	req = httptest.NewRequest(http.MethodGet, "/logs", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var got []LogMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(got) != 1 || got[0].Msg != "hello" {
		t.Errorf("got %+v, want a single entry with Msg=hello", got)
	}
}

func TestLogsEndpointRingBufferTrims(t *testing.T) {
	dc := newDashCache()
	for i := 0; i < logLength+5; i++ {
		dc.appendLog(LogMessage{MsgType: "log", Ts: int64(i), Msg: "m"}, false)
	}

	var got []LogMessage
	if err := json.Unmarshal(dc.getLogCache(), &got); err != nil {
		t.Fatalf("failed to unmarshal cache: %v", err)
	}
	if len(got) != logLength {
		t.Fatalf("len = %d, want %d", len(got), logLength)
	}
	// newest-first: the last appended entry should be first in the cache
	if got[0].Ts != int64(logLength+4) {
		t.Errorf("got[0].Ts = %d, want %d", got[0].Ts, logLength+4)
	}
}

func TestStateEndpoint(t *testing.T) {
	dc := newDashCache()
	mux := newMux(dc, false, false, &broadcast.Broadcaster{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/state", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != "{}" {
		t.Fatalf("initial body = %q, want %q", got, "{}")
	}

	dc.updateStatus(&ChainStatus{Name: "Osmosis"}, false)

	req = httptest.NewRequest(http.MethodGet, "/state", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var got statusUpdate
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if got.MessageType != "update" || len(got.Status) != 1 || got.Status[0].Name != "Osmosis" {
		t.Errorf("got %+v, want a single Osmosis entry", got)
	}
}

func TestAlertHistoryEndpoint(t *testing.T) {
	dc := newDashCache()
	mux := newMux(dc, false, false, &broadcast.Broadcaster{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/alert_history", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if got := rec.Body.String(); got != "[]" {
		t.Fatalf("initial body = %q, want %q", got, "[]")
	}

	dc.appendAlertHistory(AlertHistoryEntry{MsgType: "alert_history", Chain: "Osmosis", Message: "test"}, false)

	req = httptest.NewRequest(http.MethodGet, "/alert_history", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var got []AlertHistoryEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if len(got) != 1 || got[0].Chain != "Osmosis" {
		t.Errorf("got %+v, want a single Osmosis entry", got)
	}
}

// TestUpdateStatusRedactsWhenHideLogs and its sibling below pin the redaction
// behavior: only the status/updates path ever redacts LastError. appendLog and
// appendAlertHistory never redact their Msg/Message fields even when hideLogs is
// true — they just drop the entry entirely in that case. This asymmetry is
// pre-existing, intentional-by-omission behavior, not something this change alters.

func TestUpdateStatusRedactsWhenHideLogs(t *testing.T) {
	dc := newDashCache()
	u := &ChainStatus{Name: "Osmosis", LastError: "dial tcp://10.0.0.5:26657: connection refused"}
	dc.updateStatus(u, true)

	if !strings.Contains(u.LastError, "-redacted-") {
		t.Errorf("LastError not redacted in place: %q", u.LastError)
	}
	if strings.Contains(u.LastError, "10.0.0.5") {
		t.Errorf("LastError still leaks the endpoint: %q", u.LastError)
	}

	cache := dc.getStatusCache()
	if strings.Contains(string(cache), "10.0.0.5") {
		t.Errorf("cached status still leaks the endpoint: %s", cache)
	}
}

func TestUpdateStatusNoRedactionWhenHideLogsFalse(t *testing.T) {
	dc := newDashCache()
	original := "dial tcp://10.0.0.5:26657: connection refused"
	u := &ChainStatus{Name: "Osmosis", LastError: original}
	dc.updateStatus(u, false)

	if u.LastError != original {
		t.Errorf("LastError = %q, want unchanged %q", u.LastError, original)
	}
}

func TestAppendLogDoesNotRedact(t *testing.T) {
	dc := newDashCache()
	msg := "connecting to tcp://10.0.0.5:26657 failed"
	dc.appendLog(LogMessage{MsgType: "log", Msg: msg}, false)

	if !strings.Contains(string(dc.getLogCache()), "10.0.0.5") {
		t.Errorf("expected log cache to contain the unredacted URL, got: %s", dc.getLogCache())
	}
}

func TestAppendAlertHistoryDoesNotRedact(t *testing.T) {
	dc := newDashCache()
	msg := "node tcp://10.0.0.5:26657 is down"
	dc.appendAlertHistory(AlertHistoryEntry{MsgType: "alert_history", Message: msg}, false)

	if !strings.Contains(string(dc.getAlertHistoryCache()), "10.0.0.5") {
		t.Errorf("expected alert-history cache to contain the unredacted URL, got: %s", dc.getAlertHistoryCache())
	}
}

func TestAppendLogHideLogsDropsEntirely(t *testing.T) {
	dc := newDashCache()
	initial := dc.getLogCache()

	cache, single := dc.appendLog(LogMessage{MsgType: "log", Msg: "hello"}, true)
	if cache != nil || single != nil {
		t.Errorf("expected (nil, nil), got (%v, %v)", cache, single)
	}
	if string(dc.getLogCache()) != string(initial) {
		t.Errorf("log cache changed despite hideLogs: %s", dc.getLogCache())
	}
}

func TestAppendAlertHistoryHideLogsDropsEntirely(t *testing.T) {
	dc := newDashCache()
	initial := dc.getAlertHistoryCache()

	cache, single := dc.appendAlertHistory(AlertHistoryEntry{MsgType: "alert_history", Message: "hello"}, true)
	if cache != nil || single != nil {
		t.Errorf("expected (nil, nil), got (%v, %v)", cache, single)
	}
	if string(dc.getAlertHistoryCache()) != string(initial) {
		t.Errorf("alert-history cache changed despite hideLogs: %s", dc.getAlertHistoryCache())
	}
}

// TestDashCacheConcurrentAccess makes no value assertions of its own — the
// interleaving of concurrent writers and readers is inherently nondeterministic.
// Its purpose is to run clean under `go test -race` in CI (this sandbox has no cgo,
// so -race can't run here): before dashCache's mutex existed, logCache/statusCache/
// alertHistoryCache were written by a single background goroutine and read directly
// by concurrent HTTP-handler goroutines with no synchronization at all — a real data
// race. This mirrors the reasoning behind ChainConfig.silencedUntil's atomic.Int64
// elsewhere in this codebase: the fix only proves itself under -race, not by any
// assertion a non-race test run could make.
func TestDashCacheConcurrentAccess(t *testing.T) {
	dc := newDashCache()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dc.updateStatus(&ChainStatus{Name: "chain"}, false)
			dc.appendLog(LogMessage{MsgType: "log", Msg: "m"}, false)
			dc.appendAlertHistory(AlertHistoryEntry{MsgType: "alert_history", Message: "m"}, false)
		}()
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = dc.getStatusCache()
			_ = dc.getLogCache()
			_ = dc.getAlertHistoryCache()
		}()
	}

	wg.Wait()
}

func TestWSHandlerConnectAndCheckOrigin(t *testing.T) {
	dc := newDashCache()
	cast := &broadcast.Broadcaster{}
	mux := newMux(dc, false, false, cast, nil, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	// A foreign Origin header proves the upgrader's CheckOrigin override (return true
	// unconditionally) is actually wired up — gorilla's default CheckOrigin rejects
	// cross-origin upgrades.
	headers := http.Header{"Origin": []string{"http://evil.example.com"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial failed: %v (resp=%v)", err, resp)
	}
	defer conn.Close()
}

func TestWSHandlerClosesOnBroadcasterDiscard(t *testing.T) {
	dc := newDashCache()
	cast := &broadcast.Broadcaster{}
	mux := newMux(dc, false, false, cast, nil, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// Deterministic regardless of whether this runs before or after the server-side
	// handler reaches cast.Listen(): Broadcaster.Listen() hands back an
	// already-closed channel if the broadcaster is already discarded, and
	// Broadcaster.Discard() closes every existing listener channel otherwise. Either
	// way the handler's read loop ends and it closes the connection.
	cast.Discard()

	// Safety net against a genuine regression hanging the test, not a timing
	// mechanism for the success path.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected ReadMessage to eventually error after broadcaster discard, got nil")
	}
}
