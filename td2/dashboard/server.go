package dash

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/textileio/go-threads/broadcast"
	"golang.org/x/crypto/bcrypt"
)

var (
	Content embed.FS
	rootDir fs.FS
	rex     = regexp.MustCompile(`\W(https?|tcp|wss?)://.+\w`)

	// buildID identifies this running process for HTTP caching purposes. Static
	// assets embedded via go:embed report a zero ModTime, so http.FileServer never
	// emits Last-Modified for them - "Cache-Control: no-cache" then has nothing to
	// revalidate against, and silently degrades into "refetch everything, always".
	// A fixed ETag for the process's whole lifetime fixes that: unchanged between
	// requests (cheap 304s via http.ServeContent's own If-None-Match handling, which
	// respects an ETag already set on the response before it runs), but different
	// after every new deploy (new process => new buildID => the old cached ETag no
	// longer matches => a real refetch) - exactly the guarantee no-cache was added
	// for in the first place.
	buildID = `"` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"`
)

const logLength = 256
const alertHistoryLength = 256

// statusUpdate is the wire shape broadcast/served for the aggregated chain status.
type statusUpdate struct {
	MessageType string `json:"msgType"`
	Status      []*ChainStatus
}

// dashCache holds all dashboard state written by the single background aggregator
// goroutine in Serve and read concurrently by HTTP handlers. All access goes through
// mu. Previously the status map was guarded by a mutex but the three []byte caches
// derived from it were written and read with no synchronization at all — a real data
// race between the writer goroutine and per-request handler goroutines.
type dashCache struct {
	mu sync.RWMutex

	status            map[string]*ChainStatus
	logSlice          []LogMessage
	alertHistorySlice []AlertHistoryEntry

	logCache          []byte
	statusCache       []byte
	alertHistoryCache []byte
}

func newDashCache() *dashCache {
	return &dashCache{
		status:            make(map[string]*ChainStatus),
		logSlice:          make([]LogMessage, 0),
		alertHistorySlice: make([]AlertHistoryEntry, 0),
		logCache:          []byte{'[', ']'},
		statusCache:       []byte{'{', '}'},
		alertHistoryCache: []byte{'[', ']'},
	}
}

// updateStatus applies an incoming chain-status update: redacts leaked RPC endpoints
// from LastError in place when hideLogs is set, merges into status, marshals, and
// caches the result. Returns nil on marshal error, the marshaled bytes otherwise.
func (c *dashCache) updateStatus(u *ChainStatus, hideLogs bool) []byte {
	// try to catch any accidental rpc endpoint leaks
	if hideLogs && rex.MatchString(u.LastError) {
		u.LastError = rex.ReplaceAllString(u.LastError, "-redacted-")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.status[u.Name] = u
	result := make([]*ChainStatus, 0, len(c.status))
	for k := range c.status {
		result = append(result, c.status[k])
	}
	sort.Slice(result, func(i, j int) bool {
		return sort.StringsAreSorted([]string{result[i].Name, result[j].Name})
	})
	j, e := json.Marshal(statusUpdate{
		MessageType: "update",
		Status:      result,
	})
	if e != nil {
		return nil
	}
	c.statusCache = j
	return j
}

// appendLog appends a log message to the capped ring buffer. Returns (nil, nil) when
// hideLogs is set — the message is dropped entirely, not redacted (redaction only ever
// applies to ChainStatus.LastError). Returns the updated cache and the marshaled single
// entry (for the caller to broadcast).
func (c *dashCache) appendLog(l LogMessage, hideLogs bool) (cache []byte, single []byte) {
	if hideLogs {
		return nil, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.logSlice) >= logLength {
		c.logSlice = append([]LogMessage{l}, c.logSlice[0:len(c.logSlice)-1]...)
	} else {
		c.logSlice = append([]LogMessage{l}, c.logSlice...)
	}
	j, e := json.Marshal(c.logSlice)
	if e != nil {
		return nil, nil
	}
	c.logCache = j
	single, e = json.Marshal(l)
	if e != nil {
		return j, nil
	}
	return j, single
}

// appendAlertHistory: identical shape to appendLog, for alert-history entries.
func (c *dashCache) appendAlertHistory(ah AlertHistoryEntry, hideLogs bool) (cache []byte, single []byte) {
	if hideLogs {
		return nil, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.alertHistorySlice) >= alertHistoryLength {
		c.alertHistorySlice = append([]AlertHistoryEntry{ah}, c.alertHistorySlice[0:len(c.alertHistorySlice)-1]...)
	} else {
		c.alertHistorySlice = append([]AlertHistoryEntry{ah}, c.alertHistorySlice...)
	}
	j, e := json.Marshal(c.alertHistorySlice)
	if e != nil {
		return nil, nil
	}
	c.alertHistoryCache = j
	single, e = json.Marshal(ah)
	if e != nil {
		return j, nil
	}
	return j, single
}

func (c *dashCache) getLogCache() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.logCache
}

func (c *dashCache) getStatusCache() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statusCache
}

func (c *dashCache) getAlertHistoryCache() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.alertHistoryCache
}

// BasicAuthConfig controls optional HTTP Basic Auth protecting every dashboard route,
// including /ws — browsers resend a cached Authorization header on subsequent requests
// to the same origin once the user has authenticated once via the browser's native
// prompt, including the WebSocket upgrade request.
type BasicAuthConfig struct {
	Enabled      bool
	Username     string
	PasswordHash string // bcrypt hash, e.g. from `tenderduty -hash-password`
}

// withBasicAuth wraps next with HTTP Basic Auth when cfg.Enabled is true; otherwise it
// returns next unchanged. Username comparison and the cache check below are
// constant-time; bcrypt's own comparison is already constant-time for the password.
func withBasicAuth(next http.Handler, cfg BasicAuthConfig) http.Handler {
	if !cfg.Enabled {
		return next
	}

	// Browsers resend the exact same Authorization header on every request once
	// they've authenticated once - without this, bcrypt (deliberately slow, to resist
	// brute-forcing) would run again on every single asset/API/websocket-handshake
	// request, making the whole dashboard noticeably sluggish. Since the configured
	// username/password never change without a process restart, there's only ever one
	// possible correct header value - cache it and skip straight past bcrypt for
	// repeat requests carrying it.
	var verifiedHeader atomic.Pointer[string]

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authHeader := request.Header.Get("Authorization")
		if cached := verifiedHeader.Load(); authHeader != "" && cached != nil &&
			subtle.ConstantTimeCompare([]byte(authHeader), []byte(*cached)) == 1 {
			next.ServeHTTP(writer, request)
			return
		}

		user, pass, ok := request.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(cfg.Username)) != 1 ||
			bcrypt.CompareHashAndPassword([]byte(cfg.PasswordHash), []byte(pass)) != nil {
			writer.Header().Set("WWW-Authenticate", `Basic realm="tenderduty"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		verifiedHeader.Store(&authHeader)
		next.ServeHTTP(writer, request)
	})
}

// newMux builds the dashboard's routes on a private *http.ServeMux — never the
// package-level http.DefaultServeMux — so it can be constructed independently of
// ListenAndServe (e.g. by tests) and any number of times in one process.
func newMux(dc *dashCache, hideLogs, devMode bool, cast *broadcast.Broadcaster,
	silence func(chain string, minutes int) (time.Time, error), unsilence func(chain string) error) *http.ServeMux {
	mux := http.NewServeMux()

	upgrader := websocket.Upgrader{}
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	upgrader.EnableCompression = true

	mux.HandleFunc("/ws", func(writer http.ResponseWriter, request *http.Request) {
		c, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer c.Close()
		sub := cast.Listen()
		defer sub.Discard()
		for message := range sub.Channel() {
			e := c.WriteMessage(websocket.TextMessage, message.([]byte))
			if e != nil {
				return
			}
		}
	})

	mux.HandleFunc("/logsenabled", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		j, _ := json.Marshal(map[string]bool{"enabled": !hideLogs})
		_, _ = writer.Write(j)
	})

	mux.HandleFunc("/logs", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write(dc.getLogCache())
	})

	mux.HandleFunc("/alert_history", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write(dc.getAlertHistoryCache())
	})

	mux.HandleFunc("/state", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write(dc.getStatusCache())
	})

	mux.HandleFunc("/silence", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		chain := request.URL.Query().Get("chain")
		minutes, err := strconv.Atoi(request.URL.Query().Get("minutes"))
		if chain == "" || err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"chain and minutes (integer) are required"}`))
			return
		}
		until, sErr := silence(chain, minutes)
		if sErr != nil {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"` + sErr.Error() + `"}`))
			return
		}
		j, _ := json.Marshal(map[string]any{"chain": chain, "silenced_until": until.Unix()})
		_, _ = writer.Write(j)
	})

	mux.HandleFunc("/unsilence", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		chain := request.URL.Query().Get("chain")
		if chain == "" {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"chain is required"}`))
			return
		}
		if err := unsilence(chain); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"` + err.Error() + `"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})

	mux.Handle("/", &CacheHandler{
		devMode: devMode,
	})

	return mux
}

func Serve(port string, updates chan *ChainStatus, logs chan LogMessage, hideLogs bool, devMode bool,
	silence func(chain string, minutes int) (time.Time, error), unsilence func(chain string) error,
	alertHistory chan AlertHistoryEntry, auth BasicAuthConfig) {
	var err error
	rootDir, err = fs.Sub(Content, "static")
	if err != nil {
		slog.Error("failed to load embedded static content", "err", err)
		os.Exit(1)
	}

	dc := newDashCache()
	// was `var cast broadcast.Broadcaster`; now a pointer purely so newMux can share
	// the same instance — every Broadcaster method already has a pointer receiver, so
	// this is behaviorally identical to the zero-value form.
	cast := &broadcast.Broadcaster{}

	go func() {
		tick := time.NewTicker(time.Second)
		update := false
		for {
			select {
			case <-tick.C:
				if update {
					_ = cast.Send(dc.getStatusCache())
					update = false
				}

			case u := <-updates:
				if dc.updateStatus(u, hideLogs) != nil {
					update = true
				}

			case l := <-logs:
				if _, single := dc.appendLog(l, hideLogs); single != nil {
					_ = cast.Send(single)
				}

			case ah := <-alertHistory:
				if _, single := dc.appendAlertHistory(ah, hideLogs); single != nil {
					_ = cast.Send(single)
				}
			}
		}
	}()

	mux := newMux(dc, hideLogs, devMode, cast, silence, unsilence)
	handler := withBasicAuth(mux, auth)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
	}
	err = server.ListenAndServe()
	cast.Discard()
	slog.Error("tenderduty dashboard server failed", "err", err)
	os.Exit(1)
}

// CacheHandler implements the Handler interface with a Cache-Control set on responses
type CacheHandler struct {
	devMode bool
}

func (ch CacheHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	// no-cache (not no-store): the browser still revalidates before reusing a cached
	// response, so it can never silently keep serving assets from a *previous
	// deploy* for up to an hour like `public, max-age=3600` did — a real deploy
	// footgun (ship a new build, the dashboard renders empty/broken for existing
	// visitors until their cache happens to expire). In devMode, http.FileServer's
	// own Last-Modified (from real file mtimes on disk) makes revalidation cheap
	// automatically. In prod, buildID (see var above) does the same job for
	// go:embed'd assets, which don't carry a usable mtime of their own.
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Powered-By", "https://github.com/MoonbridgeInc/tenderduty")
	if ch.devMode {
		http.FileServer(http.Dir("./td2/static")).ServeHTTP(writer, request)
	} else {
		writer.Header().Set("ETag", buildID)
		http.FileServer(http.FS(rootDir)).ServeHTTP(writer, request)
	}
}
