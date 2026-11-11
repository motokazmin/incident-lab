// mail — заглушка почтового шлюза. Не часть учебного кода: лежит вне /srv/app, дежурный
// её не чинит. Отправка — ожидание, а не работа процессора, отправки идут параллельно.
// Журнал отправок нужен действиям сюжета: письмо считается ушедшим, только если шлюз его
// принял, а не если сервис так записал в своей таблице.
package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	addr    = "127.0.0.1:9025"
	journal = "/var/log/mail/sent.log"
	delay   = 150 * time.Millisecond
	maxBody = 4 << 20
)

type gateway struct {
	mu    sync.Mutex
	f     *os.File
	sent  map[int64]int // заказ → сколько писем ушло
	total int
	dups  int
}

func main() {
	g := &gateway{sent: map[int64]int{}}
	if err := g.replay(); err != nil {
		log.Fatal(err)
	}
	f, err := os.OpenFile(journal, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	g.f = f
	http.HandleFunc("POST /send", g.send)
	http.HandleFunc("GET /stats", g.stats)
	http.HandleFunc("POST /oldest-unsent", g.oldestUnsent)
	log.Printf("mail слушает %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// replay восстанавливает счётчики из журнала: шлюз мог перезапуститься.
func (g *gateway) replay() error {
	if err := os.MkdirAll("/var/log/mail", 0o755); err != nil {
		return err
	}
	f, err := os.Open(journal)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if id, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			g.record(id)
		}
	}
	return sc.Err()
}

func (g *gateway) record(id int64) bool {
	g.sent[id]++
	g.total++
	if g.sent[id] == 2 {
		g.dups++
	}
	return g.sent[id] > 1
}

// send принимает письмо о заказе: POST /send?order=N.
func (g *gateway) send(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("order"), 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "bad order", http.StatusBadRequest)
		return
	}
	time.Sleep(delay)
	g.mu.Lock()
	dup := g.record(id)
	_, err = fmt.Fprintf(g.f, "%d %d\n", time.Now().UnixMilli(), id)
	g.mu.Unlock()
	if err != nil {
		http.Error(w, "journal", http.StatusInternalServerError)
		return
	}
	if dup {
		log.Printf("повторное письмо о заказе %d", id)
	}
	fmt.Fprintln(w, `{"status":"accepted"}`)
}

// stats печатает «писем заказов-с-повтором повторных-писем»: заказ с повтором — о нём ушло
// больше одного письма, повторные письма — все письма сверх первого по каждому заказу.
func (g *gateway) stats(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fmt.Fprintf(w, "%d %d %d\n", g.total, g.dups, g.total-len(g.sent))
}

// oldestUnsent получает строки «заказ время-создания-в-секундах» по возрастанию номера и
// печатает первый заказ без письма и его возраст в секундах; пусто — все отправлены.
func (g *gateway) oldestUnsent(w http.ResponseWriter, r *http.Request) {
	sc := bufio.NewScanner(http.MaxBytesReader(w, r.Body, maxBody))
	now := float64(time.Now().UnixMilli()) / 1000
	g.mu.Lock()
	defer g.mu.Unlock()
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		id, err1 := strconv.ParseInt(fields[0], 10, 64)
		at, err2 := strconv.ParseFloat(fields[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if g.sent[id] == 0 {
			fmt.Fprintf(w, "%d %.1f\n", id, max(now-at, 0))
			return
		}
	}
}
