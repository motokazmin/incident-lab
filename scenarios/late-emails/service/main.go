// shop — сервис заказов. Письма о заказах уходят фоном: задачи лежат в таблице jobs,
// их разбирают обработчики из queue.go.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
)

const (
	dsn          = "host=/run/postgresql user=shop dbname=shop sslmode=disable"
	maxOpenConns = 30
)

type server struct {
	db *sql.DB
}

func main() {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	db.SetConnMaxLifetime(30 * time.Minute)

	s := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}", s.getProduct)
	mux.HandleFunc("GET /orders/{id}", s.getOrder)
	mux.HandleFunc("POST /orders", s.createOrder)
	mux.HandleFunc("GET /debug/dbstats", s.dbStats)
	srv := &http.Server{Addr: "127.0.0.1:8080", Handler: accessLog(http.TimeoutHandler(mux, time.Second, "timeout\n"))}

	// Остановка по SIGTERM (svc restart, svc deploy): обработчики дописывают начатые письма.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	var wg sync.WaitGroup
	s.startWorkers(ctx, &wg)

	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Println("shop слушает :8080")
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	wg.Wait()
	log.Println("shop остановлен")
}

func (s *server) dbStats(w http.ResponseWriter, _ *http.Request) {
	st := s.db.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"max_open": st.MaxOpenConnections, "open": st.OpenConnections,
		"in_use": st.InUse, "idle": st.Idle,
		"wait_count": st.WaitCount, "wait_ms": st.WaitDuration.Milliseconds(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) {
	w.status = c
	w.ResponseWriter.WriteHeader(c)
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s status=%d dur=%s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}
