// Package gateway — единственный путь из браузера к стендам: API, терминал, статика.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"incident-lab/platform/internal/agentproto"
	"incident-lab/platform/internal/lab"
)

const (
	maxBody = 2 << 20
	maxPath = 512
)

// New собирает HTTP-обработчик. webDir — каталог собранного веб-интерфейса.
func New(m *lab.Manager, webDir string) http.Handler {
	g := &gateway{m: m}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/scenarios", g.scenarios)
	mux.HandleFunc("POST /api/labs", g.start)
	mux.HandleFunc("GET /api/labs/{id}", g.withLab(g.view))
	mux.HandleFunc("DELETE /api/labs/{id}", g.stop)
	mux.HandleFunc("GET /api/labs/{id}/metrics", g.withLab(g.metrics))
	mux.HandleFunc("GET /api/labs/{id}/files", g.withLab(g.files))
	mux.HandleFunc("GET /api/labs/{id}/file", g.withLab(g.readFile))
	mux.HandleFunc("PUT /api/labs/{id}/file", g.withLab(g.writeFile))
	mux.HandleFunc("POST /api/labs/{id}/verify", g.withLab(g.verify))
	mux.HandleFunc("POST /api/labs/{id}/hints/{n}", g.withLab(g.openHint))
	mux.HandleFunc("GET /api/labs/{id}/term", g.withLab(g.terminal))
	mux.Handle("GET /", http.FileServer(http.Dir(webDir)))
	return guard(mux)
}

// guard закрывает локальный сервер от чужих сайтов в браузере разработчика:
// Host защищает от DNS rebinding, Origin — от запросов с других страниц.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && host != "127.0.0.1" {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "bad origin", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; worker-src 'self' blob:; connect-src 'self' ws://localhost:* ws://127.0.0.1:*; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type gateway struct {
	m *lab.Manager
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, lab.ErrNotFound):
		http.Error(w, "стенд не найден", http.StatusNotFound)
	case errors.Is(err, lab.ErrLimit):
		http.Error(w, "все места заняты, попробуй позже", http.StatusTooManyRequests)
	case errors.Is(err, lab.ErrBusy):
		http.Error(w, "стенд сейчас занят", http.StatusConflict)
	default:
		// Текст ошибки может содержать данные из песочницы или пути хоста.
		slog.Error("gateway", "err", err)
		http.Error(w, "внутренняя ошибка", http.StatusBadGateway)
	}
}

func (g *gateway) withLab(h func(http.ResponseWriter, *http.Request, *lab.Lab)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, err := g.m.Get(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		h(w, r, l)
	}
}

func (g *gateway) scenarios(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, g.m.Scenarios())
}

func (g *gateway) start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scenario string `json:"scenario"`
		Seed     uint64 `json:"seed,string"`
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "нужен JSON", http.StatusUnsupportedMediaType)
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "плохой запрос", http.StatusBadRequest)
		return
	}
	l, err := g.m.Start(req.Scenario, req.Seed)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, l.View())
}

func (g *gateway) view(w http.ResponseWriter, _ *http.Request, l *lab.Lab) {
	writeJSON(w, l.View())
}

func (g *gateway) stop(w http.ResponseWriter, r *http.Request) {
	if err := g.m.Stop(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *gateway) metrics(w http.ResponseWriter, r *http.Request, l *lab.Lab) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	writeJSON(w, l.Metrics(since))
}

func (g *gateway) files(w http.ResponseWriter, r *http.Request, l *lab.Lab) {
	files, err := l.Files(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, files)
}

// filePath возвращает путь из запроса. Длина ограничена: путь попадает в ленту событий.
func filePath(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := r.URL.Query().Get("path")
	if p == "" || len(p) > maxPath {
		http.Error(w, "плохой путь", http.StatusBadRequest)
		return "", false
	}
	return p, true
}

func (g *gateway) readFile(w http.ResponseWriter, r *http.Request, l *lab.Lab) {
	path, ok := filePath(w, r)
	if !ok {
		return
	}
	data, err := l.ReadFile(r.Context(), path)
	if err != nil {
		fail(w, err)
		return
	}
	// Содержимое из песочницы браузер не должен исполнять ни при каких условиях.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment")
	w.Write(data)
}

func (g *gateway) writeFile(w http.ResponseWriter, r *http.Request, l *lab.Lab) {
	path, ok := filePath(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "файл слишком большой", http.StatusRequestEntityTooLarge)
		return
	}
	if err := l.WriteFile(r.Context(), path, data); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *gateway) verify(w http.ResponseWriter, _ *http.Request, l *lab.Lab) {
	if err := l.Verify(); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (g *gateway) openHint(w http.ResponseWriter, r *http.Request, l *lab.Lab) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.Error(w, "bad hint", http.StatusBadRequest)
		return
	}
	if err := l.OpenHint(n); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, l.View())
}

// terminal: бинарные сообщения — данные терминала, текстовые — {"cols","rows"}.
func (g *gateway) terminal(w http.ResponseWriter, r *http.Request, l *lab.Lab) {
	conn, err := l.Terminal(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer conn.Close()
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(agentproto.MaxFrame)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		defer cancel()
		for {
			typ, p, err := agentproto.ReadFrame(conn)
			if err != nil || typ != agentproto.FrameData {
				return
			}
			wctx, c := context.WithTimeout(ctx, 10*time.Second)
			err = ws.Write(wctx, websocket.MessageBinary, p)
			c()
			if err != nil {
				return
			}
		}
	}()
	for {
		typ, p, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			err = agentproto.WriteFrame(conn, agentproto.FrameData, p)
		} else {
			var sz struct {
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(p, &sz) != nil {
				continue
			}
			err = agentproto.WriteFrame(conn, agentproto.FrameResize, agentproto.ResizePayload(sz.Cols, sz.Rows))
		}
		if err != nil {
			return
		}
	}
}
