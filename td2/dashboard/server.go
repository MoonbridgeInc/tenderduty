package dash

import (
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
	"time"

	"github.com/gorilla/websocket"
	"github.com/textileio/go-threads/broadcast"
)

var (
	Content embed.FS
	rootDir fs.FS
	rex     = regexp.MustCompile(`\W(https?|tcp|wss?)://.+\w`)
)

const logLength = 256
const alertHistoryLength = 256

func Serve(port string, updates chan *ChainStatus, logs chan LogMessage, hideLogs bool, devMode bool,
	silence func(chain string, minutes int) (time.Time, error), unsilence func(chain string) error,
	alertHistory chan AlertHistoryEntry) {
	var err error
	rootDir, err = fs.Sub(Content, "static")
	if err != nil {
		slog.Error("failed to load embedded static content", "err", err)
		os.Exit(1)
	}
	var cast broadcast.Broadcaster

	// cache the json .... don't serialize on-demand
	logCache, statusCache := []byte{'[', ']'}, []byte{'{', '}'}
	alertHistoryCache := []byte{'[', ']'}

	statusMux := sync.Mutex{}
	status := make(map[string]*ChainStatus)
	logSlice := make([]LogMessage, 0)
	alertHistorySlice := make([]AlertHistoryEntry, 0)

	type statusUpdate struct {
		MessageType string `json:"msgType"`
		Status      []*ChainStatus
	}

	go func() {
		tick := time.NewTicker(time.Second)
		update := false
		for {
			select {
			case <-tick.C:
				if update {
					_ = cast.Send(statusCache)
					update = false
				}

			case u := <-updates:
				// try to catch any accidental rpc endpoint leaks
				if hideLogs && rex.MatchString(u.LastError) {
					u.LastError = rex.ReplaceAllString(u.LastError, "-redacted-")
				}
				statusMux.Lock() // probably unnecessary
				status[u.Name] = u
				result := make([]*ChainStatus, 0)
				for k := range status {
					result = append(result, status[k])
				}
				statusMux.Unlock()
				sort.Slice(result, func(i, j int) bool {
					return sort.StringsAreSorted([]string{result[i].Name, result[j].Name})
				})
				j, e := json.Marshal(statusUpdate{
					MessageType: "update",
					Status:      result,
				})
				if e != nil {
					continue
				}
				statusCache = j
				update = true

			case l := <-logs:
				if hideLogs {
					continue
				}
				if len(logSlice) >= logLength {
					logSlice = append([]LogMessage{l}, logSlice[0:len(logSlice)-1]...)
				} else {
					logSlice = append([]LogMessage{l}, logSlice...)
				}
				j, e := json.Marshal(logSlice)
				if e != nil {
					continue
				}
				logCache = j
				j, e = json.Marshal(l)
				if e != nil {
					continue
				}
				_ = cast.Send(j)

			case ah := <-alertHistory:
				if hideLogs {
					continue
				}
				if len(alertHistorySlice) >= alertHistoryLength {
					alertHistorySlice = append([]AlertHistoryEntry{ah}, alertHistorySlice[0:len(alertHistorySlice)-1]...)
				} else {
					alertHistorySlice = append([]AlertHistoryEntry{ah}, alertHistorySlice...)
				}
				j, e := json.Marshal(alertHistorySlice)
				if e != nil {
					continue
				}
				alertHistoryCache = j
				j, e = json.Marshal(ah)
				if e != nil {
					continue
				}
				_ = cast.Send(j)
			}
		}
	}()

	upgrader := websocket.Upgrader{}
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	upgrader.EnableCompression = true

	http.HandleFunc("/ws", func(writer http.ResponseWriter, request *http.Request) {
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

	http.HandleFunc("/logsenabled", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		j, _ := json.Marshal(map[string]bool{"enabled": !hideLogs})
		_, _ = writer.Write(j)
	})

	http.HandleFunc("/logs", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write(logCache)
	})

	http.HandleFunc("/alert_history", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write(alertHistoryCache)
	})

	http.HandleFunc("/state", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write(statusCache)
	})

	http.HandleFunc("/silence", func(writer http.ResponseWriter, request *http.Request) {
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

	http.HandleFunc("/unsilence", func(writer http.ResponseWriter, request *http.Request) {
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

	http.Handle("/", &CacheHandler{
		devMode: devMode,
	})
	server := &http.Server{
		Addr:              ":" + port,
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
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writer.Header().Set("X-Powered-By", "https://github.com/firstset/tenderduty")
	if ch.devMode {
		http.FileServer(http.Dir("./td2/static")).ServeHTTP(writer, request)
	} else {
		http.FileServer(http.FS(rootDir)).ServeHTTP(writer, request)
	}
}
