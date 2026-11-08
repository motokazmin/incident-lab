// shop — витрина магазина: карточка товара и рекомендации к ней.
package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	_ "net/http/pprof" // профили на служебном порту :6060
	"time"

	_ "github.com/lib/pq"
)

const (
	dsn          = "host=/run/postgresql user=shop dbname=shop sslmode=disable"
	maxOpenConns = 15
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

	// Служебный порт: наружу не открыт, нагрузка туда не ходит.
	go func() { log.Println(http.ListenAndServe("127.0.0.1:6060", nil)) }()

	s := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}", s.getProduct)

	log.Println("shop слушает :8080")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", accessLog(mux)))
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
