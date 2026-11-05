//go:build e2e

package lab_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"incident-lab/platform/internal/agentproto"
	"incident-lab/platform/internal/lab"
	"incident-lab/platform/internal/sandbox/firecracker"
	"incident-lab/platform/scenario"
	"incident-lab/scenarios/poolexhausted"
)

// Запуск: go test -tags e2e ./platform/internal/lab/ -v -timeout 15m

func manager(t *testing.T) *lab.Manager {
	t.Helper()
	data, err := filepath.Abs("../../../.data")
	if err != nil {
		t.Fatal(err)
	}
	p, err := firecracker.New(firecracker.Config{
		Binary: data + "/fc/firecracker", Kernel: data + "/fc/vmlinux", ImagesDir: data + "/images",
		RunDir: data + "/run", VCPUs: 2, MemMiB: 1024, LabDiskMiB: 1024,
		DiskBytesPerSec: 100 << 20, DiskOpsPerSec: 5000,
	})
	if err != nil {
		t.Skip(err)
	}
	return lab.NewManager(p, []scenario.Scenario{poolexhausted.New()}, lab.Limits{MaxLabs: 12, MaxLifetime: 10 * time.Minute, IdleTimeout: 5 * time.Minute})
}

func waitState(t *testing.T, l *lab.Lab, d time.Duration, want ...string) lab.View {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		v := l.View()
		for _, s := range want {
			if v.State == s {
				return v
			}
		}
		if v.State == lab.StateEnded {
			t.Fatalf("стенд завершился: %+v", v.Events)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("нет состояния %v за %s: %s, %+v", want, d, l.View().State, l.View().Events)
	return lab.View{}
}

func shell(t *testing.T, l *lab.Lab, cmd string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c, err := l.Terminal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Маркер склеивается в оболочке, чтобы эхо команды его не содержало.
	line := fmt.Sprintf("%s; echo END-OF\"\"-CMD-$?\n", cmd)
	if err := agentproto.WriteFrame(c, agentproto.FrameData, []byte(line)); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		for {
			_, p, err := agentproto.ReadFrame(c)
			if err != nil {
				done <- out.String()
				return
			}
			out.Write(p)
			if strings.Contains(out.String(), "END-OF-CMD-0") {
				done <- ""
				return
			}
			if strings.Contains(out.String(), "END-OF-CMD-1") || strings.Contains(out.String(), "END-OF-CMD-2") {
				done <- out.String()
				return
			}
		}
	}()
	select {
	case o := <-done:
		if o != "" {
			t.Fatalf("команда %q упала:\n%s", cmd, o)
		}
	case <-ctx.Done():
		t.Fatalf("команда %q не завершилась", cmd)
	}
}

func patch(t *testing.T, l *lab.Lab, file, from, to string) {
	t.Helper()
	ctx := context.Background()
	src, err := l.ReadFile(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte(from)) {
		t.Fatalf("в %s нет %q", file, from)
	}
	if err := l.WriteFile(ctx, file, bytes.Replace(src, []byte(from), []byte(to), 1)); err != nil {
		t.Fatal(err)
	}
}

func verify(t *testing.T, l *lab.Lab) *lab.View {
	t.Helper()
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitState(t, l, 100*time.Millisecond+time.Second, lab.StateVerifying, lab.StateResolved, lab.StateAlert, lab.StateHealthy)
	v := waitState(t, l, 2*time.Minute, lab.StateResolved, lab.StateAlert, lab.StateHealthy)
	for _, i := range v.Report.Items {
		t.Logf("  [%v] %s: %s", i.Pass, i.Name, i.Detail)
	}
	return &v
}

const soldOut = "\tif stock < req.Qty {\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n\t\treturn\n\t}\n"

func TestRepairs(t *testing.T) {
	m := manager(t)
	cases := []struct {
		name string
		fix  func(t *testing.T, l *lab.Lab)
		pass bool
		fail string // на какой проверке должен быть провал
		hint string // часть подсказки при провале, пусто — подсказки нет
	}{
		{"рестарт", func(t *testing.T, l *lab.Lab) { shell(t, l, "svc restart") }, false, "нагрузку", "Перезапуск"},
		{"заглушка", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "main.go", `mux.HandleFunc("POST /orders", s.createOrder)`,
				`mux.HandleFunc("POST /orders", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusCreated, map[string]any{"order_id": 1}) })`)
			shell(t, l, "svc deploy")
		}, false, "Заказы", ""},
		{"большой пул", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "main.go", "maxOpenConns = 15", "maxOpenConns = 150")
			shell(t, l, "sed -i 's/^max_connections = 25/max_connections = 200/' /var/lib/postgresql/data/postgresql.conf && su postgres -c 'pg_ctl -D /var/lib/postgresql/data -w restart -l /var/log/postgresql.log' >/dev/null && svc deploy")
		}, false, "", "Больший пул"},
		{"убрана проверка остатка", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "orders.go", soldOut, "\t_ = stock\n")
			shell(t, l, "svc deploy")
		}, false, "", ""},
		// Утечка осталась в коде, спрятана от запросов проверки.
		{"отсечка по id", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "orders.go", "\ttx, err := s.db.Begin()\n", "\tif req.ProductID >= 198 {\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n\t\treturn\n\t}\n\ttx, err := s.db.Begin()\n")
			shell(t, l, "svc deploy")
		}, false, "", ""},
		// Честная ошибка новичка: остаток читается до транзакции, в гонке ветка с утечкой жива.
		{"предпроверка", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "orders.go", "\ttx, err := s.db.Begin()\n", "\tvar have int\n\tif s.db.QueryRow(`SELECT stock FROM products WHERE id = $1`, req.ProductID).Scan(&have) == nil && have < req.Qty {\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n\t\treturn\n\t}\n\ttx, err := s.db.Begin()\n")
			shell(t, l, "svc deploy")
		}, false, "", "до транзакции"},
		// Пул без предела, сессии рубит база: слоты теряются, но не кончаются.
		{"таймаут в базе", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "main.go", "db.SetMaxOpenConns(maxOpenConns)", "db.SetMaxOpenConns(0)")
			shell(t, l, "echo \"idle_in_transaction_session_timeout = '10ms'\" >> /var/lib/postgresql/data/postgresql.conf && su postgres -c 'pg_ctl -D /var/lib/postgresql/data reload' >/dev/null && svc deploy")
		}, false, "", "Таймаут"},
		{"rollback", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "orders.go", "\t\twriteError(w, http.StatusConflict, \"sold out\")\n", "\t\ttx.Rollback()\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n")
			shell(t, l, "svc deploy")
		}, true, "", ""},
		{"defer rollback", func(t *testing.T, l *lab.Lab) {
			patch(t, l, "orders.go", "\tvar stock int\n\terr = tx.QueryRow", "\tdefer tx.Rollback()\n\tvar stock int\n\terr = tx.QueryRow")
			shell(t, l, "svc deploy")
		}, true, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l, err := m.Start("pool-exhausted", 42)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { m.Stop(l.View().ID) })
			waitState(t, l, time.Minute, lab.StateHealthy)
			c.fix(t, l)
			v := verify(t, l)
			if v.Report.Passed != c.pass {
				t.Fatalf("Passed=%v, ждали %v: %+v", v.Report.Passed, c.pass, v.Report)
			}
			if got := v.Report.Items[len(v.Report.Items)-1].Hint; c.hint == "" && got != "" || !strings.Contains(got, c.hint) {
				t.Fatalf("подсказка %q, ждали %q", got, c.hint)
			}
			if c.fail != "" {
				last := v.Report.Items[len(v.Report.Items)-1]
				if last.Pass || !strings.Contains(last.Name+last.Detail, c.fail) {
					t.Fatalf("провал не там: %+v", last)
				}
			}
			// Верная починка не теряет ни одного запроса: иначе порог ошибок маскирует шум.
			if c.pass && !strings.Contains(v.Report.Items[0].Detail, "ошибок 0.0%") {
				t.Fatalf("на верной починке есть ошибки: %+v", v.Report.Items[0])
			}
			if c.pass && (v.State != lab.StateResolved || v.Debrief == "") {
				t.Fatalf("нет разбора: %+v", v)
			}
		})
	}
}

// Без починки стенд сам доходит до алерта, а проверка проваливается.
func TestIncidentHappens(t *testing.T) {
	m := manager(t)
	l, err := m.Start("pool-exhausted", 42)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(l.View().ID) })
	waitState(t, l, time.Minute, lab.StateHealthy)
	start := time.Now()
	waitState(t, l, 4*time.Minute, lab.StateAlert)
	t.Logf("алерт через %s", time.Since(start).Round(time.Second))
	if v := verify(t, l); v.Report.Passed {
		t.Fatal("проверка прошла без починки")
	}
}

// Ctrl+C и Ctrl+\ в терминале останавливают команду, как в обычной консоли:
// маска игнорируемых сигналов наследуется от PID 1 через агента.
func TestTerminalSignals(t *testing.T) {
	m := manager(t)
	l, err := m.Start("pool-exhausted", 42)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(l.View().ID) })
	waitState(t, l, time.Minute, lab.StateHealthy)

	for _, c := range []struct {
		name string
		key  byte
		want string
	}{{"Ctrl+C", 0x03, "STOPPED-130"}, {"Ctrl+\\", 0x1c, "STOPPED-131"}} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			conn, err := l.Terminal(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			send := func(b []byte) {
				if err := agentproto.WriteFrame(conn, agentproto.FrameData, b); err != nil {
					t.Fatal(err)
				}
			}
			out := make(chan string, 1)
			go func() {
				var buf bytes.Buffer
				for {
					_, p, err := agentproto.ReadFrame(conn)
					if err != nil {
						out <- buf.String()
						return
					}
					buf.Write(p)
					if strings.Contains(buf.String(), c.want) {
						out <- ""
						return
					}
				}
			}()
			send([]byte("grep SigIgn /proc/self/status\n"))
			send([]byte("svc logs\n"))
			time.Sleep(time.Second)
			send([]byte{c.key})
			time.Sleep(500 * time.Millisecond)
			// Отдельной строкой: оболочка бросает остаток строки после прерванной команды.
			send([]byte("echo STOPPED\"\"-$?\n"))
			select {
			case s := <-out:
				if s != "" {
					t.Fatalf("команда не остановилась:\n%s", s)
				}
			case <-ctx.Done():
				conn.Close()
				t.Fatalf("команда не остановилась:\n%s", <-out)
			}
		})
	}
}

// Осиротевшие процессы подбирает PID 1, зомби в стенде не копятся.
func TestOrphansReaped(t *testing.T) {
	m := manager(t)
	l, err := m.Start("pool-exhausted", 42)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(l.View().ID) })
	waitState(t, l, time.Minute, lab.StateHealthy)
	shell(t, l, `(sleep 0.2 &) ; sleep 2; ! ps -eo stat | grep -q '^Z'`)
}
