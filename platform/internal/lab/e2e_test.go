//go:build e2e

package lab_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"incident-lab/platform/internal/agentproto"
	"incident-lab/platform/internal/lab"
	"incident-lab/platform/internal/sandbox/firecracker"
	"incident-lab/platform/internal/verifier"
	"incident-lab/platform/scenario"
	"incident-lab/scenarios"
)

// Запуск: go test -tags e2e ./platform/internal/lab/ -v -parallel 1 -timeout 3h

func manager(t *testing.T) *lab.Manager {
	t.Helper()
	data, err := filepath.Abs("../../../.data")
	if err != nil {
		t.Fatal(err)
	}
	// Свой каталог: firecracker.New чистит его и иначе удалил бы живые стенды сервера.
	runDir := data + "/run-e2e"
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := firecracker.New(firecracker.Config{
		Binary: data + "/fc/firecracker", Kernel: data + "/fc/vmlinux", ImagesDir: data + "/images",
		RunDir: runDir, VCPUs: 2, MemMiB: 1024, LabDiskMiB: 1024,
		DiskBytesPerSec: 100 << 20, DiskOpsPerSec: 5000,
	})
	if err != nil {
		t.Skip(err)
	}
	return lab.NewManager(p, scenarios.All(), lab.Limits{MaxLabs: runtime.GOMAXPROCS(0) + 4, MaxLifetime: 10 * time.Minute, IdleTimeout: 5 * time.Minute})
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

// shell выполняет команду в терминале стенда и возвращает весь вывод с эхом.
func shell(t *testing.T, l *lab.Lab, cmd string) string {
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
			if strings.Contains(out.String(), "END-OF-CMD-") {
				done <- out.String()
				return
			}
		}
	}()
	select {
	case o := <-done:
		if !strings.Contains(o, "END-OF-CMD-0") {
			t.Fatalf("команда %q упала:\n%s", cmd, o)
		}
		return o
	case <-ctx.Done():
		t.Fatalf("команда %q не завершилась", cmd)
	}
	return ""
}

// showStep выполняет команду так, как её запустил бы дежурный. Строка идёт в base64, чтобы
// эхо не содержало ожидаемого текста, а timeout останавливает `svc logs`, как Ctrl+C.
func showStep(t *testing.T, l *lab.Lab, s scenario.Step) {
	t.Helper()
	enc := base64.StdEncoding.EncodeToString([]byte(s.Cmd))
	if out := shell(t, l, "echo "+enc+" | base64 -d | timeout 3 bash; true"); !strings.Contains(out, s.Expect) {
		t.Fatalf("шаг %q: нет %q в выводе:\n%s", s.Title, s.Expect, out)
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
	v := waitState(t, l, verifier.Timeout+30*time.Second, lab.StateResolved, lab.StateAlert, lab.StateHealthy)
	for _, i := range v.Report.Items {
		t.Logf("  [%v] %s: %s", i.Pass, i.Name, i.Detail)
	}
	t.Logf("  ошибок нагрузки: %d", v.Report.LoadFailed)
	return &v
}

func start(t *testing.T, m *lab.Manager, id string) *lab.Lab {
	t.Helper()
	l, err := m.Start(id, 42)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(l.View().ID) })
	waitState(t, l, time.Minute, lab.StateHealthy)
	return l
}

func runRepair(t *testing.T, l *lab.Lab, r scenario.Repair) {
	t.Helper()
	for _, e := range r.Edits {
		patch(t, l, e.File, e.From, e.To)
	}
	if r.Shell != "" {
		shell(t, l, r.Shell)
	}
}

func checkOutcome(t *testing.T, v *lab.View, r scenario.Repair) {
	t.Helper()
	if v.Report.Passed != r.Pass {
		t.Fatalf("Passed=%v, ждали %v: %+v", v.Report.Passed, r.Pass, v.Report)
	}
	last := v.Report.Items[len(v.Report.Items)-1]
	if last.Hint != "" && r.Hint == "" || !strings.Contains(last.Hint, r.Hint) {
		t.Fatalf("подсказка %q, ждали %q", last.Hint, r.Hint)
	}
	if r.FailAt != "" && (last.Pass || !strings.Contains(last.Name+last.Detail, r.FailAt)) {
		t.Fatalf("провал не там: %+v", last)
	}
	if !r.Pass {
		return
	}
	// Верная починка не теряет ни одного запроса: иначе порог ошибок маскирует шум.
	if v.Report.LoadFailed != 0 {
		t.Fatalf("на верной починке %d ошибок: %+v", v.Report.LoadFailed, v.Report.Items[0])
	}
	if v.State != lab.StateResolved || v.Debrief == "" {
		t.Fatalf("нет разбора: %+v", v)
	}
}

func firstGood(list []scenario.Repair) scenario.Repair {
	for _, r := range list {
		if r.Pass {
			return r
		}
	}
	panic("контракт: нет верной починки")
}

// TestScenario проверяет каждый сюжет на настоящих виртуалках: сбой случается сам,
// верные починки проходят, ложные нет. Выбрать один: -run TestScenario/<id>.
func TestScenario(t *testing.T) {
	m := manager(t)
	for _, sc := range scenarios.All() {
		t.Run(sc.ID, func(t *testing.T) {
			t.Run("без починки", func(t *testing.T) {
				t.Parallel()
				l := start(t, m, sc.ID)
				begin := time.Now()
				waitState(t, l, 4*time.Minute, lab.StateAlert)
				t.Logf("алерт через %s", time.Since(begin).Round(time.Second))
				if v := verify(t, l); v.Report.Passed {
					t.Fatal("проверка прошла без починки")
				}
			})
			t.Run("эталонный путь", func(t *testing.T) {
				t.Parallel()
				l := start(t, m, sc.ID)
				waitState(t, l, 4*time.Minute, lab.StateAlert)
				for _, s := range sc.Reference {
					showStep(t, l, s)
				}
				fix := firstGood(sc.Repairs)
				runRepair(t, l, fix)
				checkOutcome(t, verify(t, l), fix)
			})
			for _, r := range sc.Repairs {
				t.Run(r.Name, func(t *testing.T) {
					t.Parallel()
					l := start(t, m, sc.ID)
					runRepair(t, l, r)
					checkOutcome(t, verify(t, l), r)
				})
			}
		})
	}
}

// TestLogColors проверяет раскраску в настоящем госте: awk там busybox, а вывод идёт в терминал.
func TestLogColors(t *testing.T) {
	m := manager(t)
	l := start(t, m, scenarios.All()[0].ID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := l.Terminal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cmd := `. /etc/lab/service.env; printf 'a status=500 dur=1ms\nb status=404 dur=1ms\nc status=200 dur=500ms\nd status=200 dur=1.2s\ne status=200 dur=499ms\nf status=200 dur=0s\nsomething\n2026/11/07 21:06:21 shop завершился: сигнал KILL, код 137; перезапуск\n' >>/var/log/$SERVICE.log; svc logs 500` + "\n"
	if err := agentproto.WriteFrame(conn, agentproto.FrameData, []byte(cmd)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"\x1b[91ma status=500", "\x1b[33mb status=404", "\x1b[95mc status=200", "\x1b[95md status=200",
		"\x1b[2me status=200", "\x1b[2mf status=200", "\x1b[1msomething\x1b[0m",
		"\x1b[1;97;41m2026/11/07 21:06:21 shop завершился: сигнал KILL, код 137; перезапуск\x1b[0m",
	}
	var buf bytes.Buffer
	for {
		_, p, err := agentproto.ReadFrame(conn)
		if err != nil {
			t.Fatalf("цвета не появились: %v\n%q", err, buf.String())
		}
		buf.Write(p)
		seen := true
		for _, w := range want {
			seen = seen && strings.Contains(buf.String(), w)
		}
		if seen {
			return
		}
	}
}

// TestServiceExit: падение сервиса видно в логе и счётчике. Агент и супервизор защищены от
// OOM killer, а сервис и всё, что запускает агент, нет: иначе честная ложная починка
// «защитить сервис» убила бы стенд или оставила сервис без супервизора.
func TestServiceExit(t *testing.T) {
	m := manager(t)
	l := start(t, m, scenarios.All()[0].ID)
	out := shell(t, l, `echo "agent=$(cat /proc/$(pidof labagent)/oom_score_adj) term=$(cat /proc/self/oom_score_adj) svc=$(cat /proc/$(pidof shop)/oom_score_adj) sup=$(cat /proc/$(pgrep -of 'SERVICE.restarts')/oom_score_adj)"`)
	if !strings.Contains(out, "agent=-1000 term=0 svc=0 sup=-1000") {
		t.Fatalf("oom_score_adj:\n%s", out)
	}
	shell(t, l, `pkill -9 -x shop; for i in $(seq 50); do curl -fs -o /dev/null 127.0.0.1:8080/products/1 && break; sleep 0.1; done`)
	out = shell(t, l, `echo "lines=$(tail -n 5 /var/log/shop.log | grep -c 'shop завершился: сигнал KILL, код 137; перезапуск')"; echo "n=$(/etc/lab/actions/service-restarts)"; svc status`)
	for _, w := range []string{"lines=1", "n=1", "перезапусков с начала: 1"} {
		if !strings.Contains(out, w) {
			t.Fatalf("нет %q:\n%s", w, out)
		}
	}
}

// TestServiceCgroup: общие действия о памяти сервиса не врут в окно перезапуска. Рестарты
// идут подряд, а действия читаются всё это время: лимит и cgroup всегда на месте,
// счётчик перезапусков всегда число. Сюжет — keeps-restarting, единственный с MEMORY_MAX.
func TestServiceCgroup(t *testing.T) {
	m := manager(t)
	l := start(t, m, "keeps-restarting")
	shell(t, l, `(setsid sh -c 'while :; do svc restart; done' </dev/null >/dev/null 2>&1 &)`)
	out := shell(t, l, `a=/etc/lab/actions; bad=0; for i in $(seq 150); do`+
		` l=$($a/memory-limit); [ "$l" = "268435456 yes" ] || { bad=$((bad+1)); echo "limit=[$l]"; };`+
		` n=$($a/service-restarts); case "$n" in ''|*[!0-9]*) bad=$((bad+1)); echo "restarts=[$n]";; esac;`+
		` o=$($a/oom-events); case "$o" in ''|*[!0-9]*) bad=$((bad+1)); echo "oom=[$o]";; esac;`+
		` done; pkill -f 'while :; do svc restart'; echo "BAD-$bad-END"`)
	if !strings.Contains(out, "BAD-0-END") {
		t.Fatalf("действия врут в окно перезапуска:\n%s", out)
	}
}

// Ctrl+C и Ctrl+\ в терминале останавливают команду, как в обычной консоли:
// маска игнорируемых сигналов наследуется от PID 1 через агента.
func TestTerminalSignals(t *testing.T) {
	m := manager(t)
	l := start(t, m, scenarios.All()[0].ID)

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
	l := start(t, m, scenarios.All()[0].ID)
	shell(t, l, `(sleep 0.2 &) ; sleep 2; ! ps -eo stat | grep -q '^Z'`)
}
