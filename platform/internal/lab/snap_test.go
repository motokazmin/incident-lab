//go:build e2e

package lab_test

// Временный тест: снимки вывода slow-orders под новым пиком для разбора. Удалить после.

import (
	"fmt"
	"testing"
	"time"

	"incident-lab/platform/internal/lab"
)

const (
	pq       = `psql -X -P pager=off -U postgres -d shop -c `
	explain  = `"explain (analyze, costs off) select id, product_id, qty, created_at from orders where customer_id = %d order by created_at desc limit 20"`
	active   = `"select wait_event_type, wait_event, left(query, 80) from pg_stat_activity where datname='shop' and state='active'"`
	activeBy = `"select count(*), wait_event_type, wait_event, left(query, 40) q from pg_stat_activity where datname='shop' and state='active' group by 2,3,4 order by 1 desc"`
	statuses = `tail -n 3000 /var/log/shop.log | grep -oE '(GET|POST) /[a-z]+|status=[0-9]+' | paste - - | sort | uniq -c`
	dbstats  = `for i in 1 2 3 4; do curl -s localhost:8080/debug/dbstats; echo; sleep 5; done`
)

func snap(t *testing.T, l *lab.Lab, title, cmd string) {
	t.Helper()
	begin := time.Now()
	out := shell(t, l, "{ "+cmd+"; } 2>&1; true")
	t.Logf("\n===== %s (%s)\n$ %s\n%s", title, time.Since(begin).Round(100*time.Millisecond), cmd, out)
}

func explainFor(c int) string { return pq + fmt.Sprintf(explain, c) }

func peakSnaps(t *testing.T, l *lab.Lab) {
	snap(t, l, "статус", "svc status")
	snap(t, l, "логи, хвост", "tail -n 30 /var/log/shop.log")
	snap(t, l, "логи, адрес × статус за 3000 строк", statuses)
	snap(t, l, "медленные ответы 200 по адресам", `tail -n 3000 /var/log/shop.log | grep 'status=200' | grep -oE '(GET|POST) /[a-z]+.* dur=[0-9.]+(ms|s)' | sort -t= -k3 | tail -8`)
	snap(t, l, "отмены в базе", "grep -c 'canceling statement' /var/log/postgresql.log")
	snap(t, l, "пул ×4 через 5 с", dbstats)
	snap(t, l, "что выполняет база", pq+active)
	snap(t, l, "что выполняет база, сводка", pq+activeBy)
	snap(t, l, "процессор", "top -b -n1 | head -15; nproc")
	for _, c := range []int{5, 15000, 30001} {
		snap(t, l, fmt.Sprint("план под нагрузкой ", c), explainFor(c))
	}
	snap(t, l, "индексы", pq+`"\d orders"`)
}

// TestSnapSlowOrders: пик без починки, эталонная починка под пиком и «большой пул».
func TestSnapSlowOrders(t *testing.T) {
	m := manager(t)
	sc := byID(t, "slow-orders")
	t.Run("пик и эталон", func(t *testing.T) {
		l := start(t, m, sc.ID)
		begin := time.Now()
		waitState(t, l, 4*time.Minute, lab.StateAlert)
		t.Logf("алерт через %s", time.Since(begin).Round(time.Second))
		time.Sleep(20 * time.Second)
		peakSnaps(t, l)
		snap(t, l, "сборка индекса под пиком", "time "+pq+`"create index concurrently orders_customer_created on orders (customer_id, created_at desc)"`)
		time.Sleep(10 * time.Second)
		snap(t, l, "пул после", dbstats)
		for _, c := range []int{5, 15000, 30001} {
			snap(t, l, fmt.Sprint("план после ", c), explainFor(c))
		}
		snap(t, l, "индексы после", pq+`"\d orders"`)
		snap(t, l, "логи после", "tail -n 3000 /var/log/shop.log | tail -n 300 | grep -oE '(GET|POST) /[a-z]+|status=[0-9]+' | paste - - | sort | uniq -c")
	})
	t.Run("большой пул", func(t *testing.T) {
		l := start(t, m, sc.ID)
		for _, r := range sc.Repairs {
			if r.Name == "большой пул" {
				runRepair(t, l, r)
			}
		}
		waitState(t, l, 4*time.Minute, lab.StateAlert)
		time.Sleep(20 * time.Second)
		snap(t, l, "пул ×4 через 5 с", dbstats)
		snap(t, l, "что выполняет база, сводка", pq+activeBy)
		snap(t, l, "процессор", "top -b -n1 | head -15")
		snap(t, l, "логи, адрес × статус", statuses)
		snap(t, l, "план под нагрузкой 5", explainFor(5))
		snap(t, l, "отмены в базе", "grep -c 'canceling statement' /var/log/postgresql.log")
	})
}
