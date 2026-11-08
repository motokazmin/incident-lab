// recs — заглушка сервиса рекомендаций чужой команды. Не часть учебного кода: лежит вне
// /srv/app, дежурный её не чинит. Ответ детерминирован по id товара, задержка — ожидание,
// а не работа процессора, чтобы не мешать сервису и базе.
package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	candidates   = 1000
	features     = 64
	popularLast  = 20 // популярным товарам кандидатов ищут дольше
	popularDelay = 400 * time.Millisecond
	normalDelay  = 20 * time.Millisecond
	slowDelay    = 2 * time.Second
	slowMax      = 90 * time.Second
)

// slowUntil — до какого момента отвечать медленно на любой товар (UnixNano). Включает проверка.
var slowUntil atomic.Int64

func main() {
	var f strings.Builder
	f.WriteString("[")
	for i := range features {
		if i > 0 {
			f.WriteString(",")
		}
		fmt.Fprintf(&f, "%.4f", float64((i*7919)%10000)/10000)
	}
	f.WriteString("]")
	feat := f.String()

	http.HandleFunc("GET /candidates", func(w http.ResponseWriter, r *http.Request) {
		p, err := strconv.Atoi(r.URL.Query().Get("product"))
		if err != nil || p < 1 {
			http.Error(w, "bad product", http.StatusBadRequest)
			return
		}
		d := normalDelay
		if p <= popularLast {
			d = popularDelay
		}
		if time.Now().UnixNano() < slowUntil.Load() {
			d = slowDelay
		}
		time.Sleep(d)
		var b bytes.Buffer
		b.WriteString(`{"candidates":[`)
		for i := range candidates {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":%d,"score":%.3f,"features":%s}`, (p*13+i)%candidates+1, float64((i*37)%candidates)/candidates, feat)
		}
		b.WriteString("]}")
		w.Header().Set("Content-Type", "application/json")
		w.Write(b.Bytes())
	})
	http.HandleFunc("POST /admin/slow", func(w http.ResponseWriter, _ *http.Request) {
		slowUntil.Store(time.Now().Add(slowMax).UnixNano())
	})
	http.HandleFunc("POST /admin/normal", func(w http.ResponseWriter, _ *http.Request) {
		slowUntil.Store(0)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:9090", nil))
}
