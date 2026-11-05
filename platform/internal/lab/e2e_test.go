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
	"incident-lab/platform/scenario"
	"incident-lab/scenarios"
)

// Запуск: go test -tags e2e ./platform/internal/lab/ -v -timeout 15m

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
	v := waitState(t, l, 2*time.Minute, lab.StateResolved, lab.StateAlert, lab.StateHealthy)
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
