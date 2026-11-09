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

// Запуск — scripts/e2e.sh, уровни прогона — ADR 0008.

// Сюжеты, на которых проверяется каркас: без лимита памяти и с MEMORY_MAX. Правка этих
// сюжетов запускает и контрактные тесты, scripts/e2e.sh читает имена отсюда.
const (
	fwPlain  = "pool-exhausted"
	fwMemory = "keeps-restarting"
)

// env — префикс команды в госте: SERVICE, PROBE и MEMORY_MAX сюжета из service.env,
// чтобы тесты каркаса не знали имени сервиса и адреса проверки.
const env = ". /etc/lab/service.env; "

// Ответ проверки в госте склеивается из двух строк, чтобы эхо команды его не содержало.
func ok(name string) string { return name + `-""ok` }

// pickCase решает, гонять ли случай сюжета. LAB_E2E_CASES: пусто — все случаи, smoke —
// только «без починки» и «эталонный путь»: они видят, что каркас сломал сюжет.
func pickCase(t *testing.T, name string) bool {
	t.Helper()
	switch v := os.Getenv("LAB_E2E_CASES"); v {
	case "":
		return true
	case "smoke":
		return name == caseNoFixName || name == caseRefName
	default:
		t.Fatalf("LAB_E2E_CASES=%q: ждали пусто или smoke", v)
	}
	return false
}

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
	p, err := firecracker.New(firecracker.Defaults(data, runDir))
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
	t.Fatalf("нет состояния %v за %s: %s, %+v\nнагрузка за минуту, по 10 с:\n%s", want, d, l.View().State, l.View().Events, loadDigest(l))
	return lab.View{}
}

// loadDigest сводит метрики нагрузки за последнюю минуту по 10 с: без них провал ожидания
// алерта не объяснить по логу CI.
func loadDigest(l *lab.Lab) string {
	var b strings.Builder
	samples := l.Metrics(time.Now().Add(-time.Minute).Unix())
	for i := 0; i < len(samples); i += 10 {
		sent, failed, p95 := 0, 0, []int{}
		for _, s := range samples[i:min(i+10, len(samples))] {
			sent, failed = sent+s.Sent, failed+s.Failed
			p95 = append(p95, s.P95Ms)
		}
		fmt.Fprintf(&b, "  отправлено %d, ошибок %d, p95 по секундам %v мс\n", sent, failed, p95)
	}
	return b.String()
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

const (
	caseNoFixName = "без починки"
	caseRefName   = "эталонный путь"
)

// caseNoFix: сбой случается сам, алерт приходит, проверка без починки проваливается.
func caseNoFix(t *testing.T, m *lab.Manager, sc scenario.Scenario) {
	l := start(t, m, sc.ID)
	begin := time.Now()
	waitState(t, l, 4*time.Minute, lab.StateAlert)
	t.Logf("алерт через %s", time.Since(begin).Round(time.Second))
	if v := verify(t, l); v.Report.Passed {
		t.Fatal("проверка прошла без починки")
	}
}

// caseReference: команды эталонного пути показывают признаки, верная починка проходит до разбора.
func caseReference(t *testing.T, m *lab.Manager, sc scenario.Scenario) {
	l := start(t, m, sc.ID)
	waitState(t, l, 4*time.Minute, lab.StateAlert)
	for _, s := range sc.Reference {
		showStep(t, l, s)
	}
	fix := firstGood(sc.Repairs)
	runRepair(t, l, fix)
	checkOutcome(t, verify(t, l), fix)
}

func byID(t *testing.T, id string) scenario.Scenario {
	t.Helper()
	for _, sc := range scenarios.All() {
		if sc.ID == id {
			return sc
		}
	}
	t.Fatalf("нет сюжета %s", id)
	return scenario.Scenario{}
}

// TestScenario проверяет каждый сюжет на настоящих виртуалках: сбой случается сам,
// верные починки проходят, ложные нет. Один сюжет: -run TestScenario/<id>; какие случаи —
// LAB_E2E_CASES, см. pickCase.
func TestScenario(t *testing.T) {
	m := manager(t)
	for _, sc := range scenarios.All() {
		t.Run(sc.ID, func(t *testing.T) {
			run := func(name string, f func(t *testing.T)) {
				if !pickCase(t, name) {
					return
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					f(t)
				})
			}
			run(caseNoFixName, func(t *testing.T) { caseNoFix(t, m, sc) })
			run(caseRefName, func(t *testing.T) { caseReference(t, m, sc) })
			for _, r := range sc.Repairs {
				run(r.Name, func(t *testing.T) {
					l := start(t, m, sc.ID)
					if r.AfterAlert {
						waitState(t, l, 4*time.Minute, lab.StateAlert)
					}
					runRepair(t, l, r)
					checkOutcome(t, verify(t, l), r)
				})
			}
		})
	}
}

// TestPipeline — цепочка стенда целиком, а не сюжет: запуск, нагрузка, алерт, проверка,
// подсказки и разбор. Идёт в контрактном наборе.
func TestPipeline(t *testing.T) {
	m := manager(t)
	sc := byID(t, fwPlain)
	t.Run(caseNoFixName, func(t *testing.T) { caseNoFix(t, m, sc) })
	t.Run(caseRefName, func(t *testing.T) { caseReference(t, m, sc) })
}

// TestLogColors проверяет раскраску в настоящем госте: awk там busybox, а вывод идёт в терминал.
func TestLogColors(t *testing.T) {
	m := manager(t)
	l := start(t, m, fwPlain)
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
	l := start(t, m, fwPlain)
	up := `for i in $(seq 50); do curl -fs -o /dev/null 127.0.0.1:8080$PROBE && break; sleep 0.1; done`
	out := shell(t, l, env+`echo "agent=$(cat /proc/$(pidof labagent)/oom_score_adj) term=$(cat /proc/self/oom_score_adj) svc=$(cat /proc/$(pidof $SERVICE)/oom_score_adj) sup=$(cat /proc/$(pgrep -of 'SERVICE.restarts')/oom_score_adj)"`)
	if !strings.Contains(out, "agent=-1000 term=0 svc=0 sup=-1000") {
		t.Fatalf("oom_score_adj:\n%s", out)
	}
	shell(t, l, env+`pkill -9 -x $SERVICE; `+up)
	out = shell(t, l, env+`echo "lines=$(tail -n 5 /var/log/$SERVICE.log | grep -c "$SERVICE завершился: сигнал KILL, код 137; перезапуск")"; echo "n=$(/etc/lab/actions/service-restarts)"; svc status`)
	for _, w := range []string{"lines=1", "n=1", "перезапусков с начала: 1"} {
		if !strings.Contains(out, w) {
			t.Fatalf("нет %q:\n%s", w, out)
		}
	}
	// Без MEMORY_MAX копии в tmpfs нет, а код выше 192 — выход, а не сигнал.
	out = shell(t, l, env+`[ -z "${MEMORY_MAX:-}" ] && [ "$(readlink /proc/$(pidof $SERVICE)/exe)" = /srv/app/bin/$SERVICE ] && echo `+ok("exe")+`;`+
		` cd /srv/app/bin && mv $SERVICE $SERVICE.real && printf '#!/bin/sh\nexit 200\n' >$SERVICE && chmod +x $SERVICE && pkill -x $SERVICE;`+
		` sleep 1; mv $SERVICE.real $SERVICE; `+up+`;`+
		` grep -q "$SERVICE завершился: выход, код 200; перезапуск" /var/log/$SERVICE.log && echo `+ok("exit200")+`;`+
		` [ ! -e /run/lab/bin ] && echo `+ok("nocopy"))
	for _, w := range []string{"exe-ok", "exit200-ok", "nocopy-ok"} {
		if !strings.Contains(out, w) {
			t.Fatalf("нет %s: запуск без лимита или код 200:\n%s", w, out)
		}
	}
}

// TestSvcRestart: svc restart и действие restart-service возвращают управление только после
// ответа сервиса, и каждый перезапуск виден в счётчике. Иначе проверка стартует в окно
// рестарта и ложно проваливается.
func TestSvcRestart(t *testing.T) {
	m := manager(t)
	l := start(t, m, fwPlain)
	probe := `curl -fs -o /dev/null -m 2 127.0.0.1:8080$PROBE`
	out := shell(t, l, env+`a=/etc/lab/actions; n0=$($a/service-restarts); bad=0;`+
		` for i in $(seq 20); do svc restart >/dev/null && `+probe+` || bad=$((bad+1)); done;`+
		` $a/restart-service >/dev/null && `+probe+` || bad=$((bad+1));`+
		` echo "BAD-$bad-DELTA-$(($($a/service-restarts)-n0))-END"`)
	if !strings.Contains(out, "BAD-0-DELTA-21-END") {
		t.Fatalf("перезапуск вернул управление до ответа или не посчитан:\n%s", out)
	}
}

// TestSvcDeploy: svc deploy запускает именно новую сборку и ждёт её ответа — и без лимита,
// и с копией бинарника в tmpfs. Код сюжета не трогается: метку несёт отдельный файл.
func TestSvcDeploy(t *testing.T) {
	m := manager(t)
	for _, id := range []string{fwPlain, fwMemory} {
		t.Run(id, func(t *testing.T) {
			l := start(t, m, id)
			out := shell(t, l, env+`cd /srv/app && printf 'package main\n\nfunc init() { println("LAB-" + "MARK") }\n' >zz_lab_mark.go &&`+
				` svc deploy >/dev/null && curl -fs -o /dev/null -m 2 127.0.0.1:8080$PROBE && echo `+ok("up")+`;`+
				` grep -q LAB-MARK /var/log/$SERVICE.log && echo `+ok("mark")+`;`+
				` cmp -s /proc/$(pidof $SERVICE)/exe /srv/app/bin/$SERVICE && echo `+ok("exe"))
			for _, w := range []string{"up-ok", "mark-ok", "exe-ok"} {
				if !strings.Contains(out, w) {
					t.Fatalf("нет %s: deploy не запустил новую сборку или не дождался ответа:\n%s", w, out)
				}
			}
		})
	}
}

// TestServiceOOM: в cgroup сервиса ядро убивает процесс, а не машину. Агент, супервизор
// и сервис переживают чужой OOM в той же cgroup, событие oom видно в действии.
func TestServiceOOM(t *testing.T) {
	m := manager(t)
	l := start(t, m, fwMemory)
	out := shell(t, l, env+`[ -n "${MEMORY_MAX:-}" ] || { echo "у сюжета нет MEMORY_MAX"; exit 1; }; a=/etc/lab/actions; o0=$($a/oom-events);`+
		` sh -c "echo \$\$ >/sys/fs/cgroup/$SERVICE/cgroup.procs; exec tail /dev/zero"; echo "hog=$?";`+
		` [ $(($($a/oom-events)-o0)) -ge 1 ] && echo `+ok("oom")+`;`+
		` pidof labagent >/dev/null && pgrep -f SERVICE.restarts >/dev/null && echo `+ok("alive")+`;`+
		` for i in $(seq 50); do curl -fs -o /dev/null -m 2 127.0.0.1:8080$PROBE && echo `+ok("up")+` && break; sleep 0.1; done`)
	for _, w := range []string{"hog=137", "oom-ok", "alive-ok", "up-ok"} {
		if !strings.Contains(out, w) {
			t.Fatalf("нет %s:\n%s", w, out)
		}
	}
}

// TestServiceCgroup: общие действия о памяти сервиса не врут в окно перезапуска. Рестарты
// идут подряд, а действия читаются всё это время: лимит и cgroup всегда на месте,
// счётчик перезапусков всегда число. Лимит берётся из MEMORY_MAX сюжета fwMemory.
func TestServiceCgroup(t *testing.T) {
	m := manager(t)
	l := start(t, m, fwMemory)
	if out := shell(t, l, env+`[ "$(readlink /proc/$(pidof $SERVICE)/exe)" = /run/lab/bin/$SERVICE ] && echo `+ok("exe")); !strings.Contains(out, "exe-ok") {
		t.Fatalf("с MEMORY_MAX сервис не из копии в tmpfs:\n%s", out)
	}
	shell(t, l, `(setsid sh -c 'while :; do svc restart; done' </dev/null >/dev/null 2>&1 &)`)
	out := shell(t, l, env+`case "$MEMORY_MAX" in *M) ;; *) echo "MEMORY_MAX=$MEMORY_MAX: ждали <N>M"; exit 1;; esac;`+
		` want="$((${MEMORY_MAX%M}*1024*1024)) yes"; a=/etc/lab/actions; bad=0; for i in $(seq 150); do`+
		` l=$($a/memory-limit); [ "$l" = "$want" ] || { bad=$((bad+1)); echo "limit=[$l]"; };`+
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
	l := start(t, m, fwPlain)

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
	l := start(t, m, fwPlain)
	shell(t, l, `(sleep 0.2 &) ; sleep 2; ! ps -eo stat | grep -q '^Z'`)
}
