package keepsrestarting

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"incident-lab/platform/scenario"
)

// fakeEnv отдаёт заданную статистику нагрузки, а счётчики — по очереди из списков.
type fakeEnv struct {
	stats      scenario.Stats
	restarts   []int
	oom        []int
	goroutines []int
	limit      string
	actions    []string
}

func (e *fakeEnv) Do(context.Context, string, string, string) (int, []byte, error) {
	return 0, nil, nil
}

func (e *fakeEnv) RunLoad(context.Context, scenario.LoadProfile, time.Duration) (scenario.Stats, error) {
	return e.stats, nil
}

func (e *fakeEnv) Action(_ context.Context, name string) (string, error) {
	e.actions = append(e.actions, name)
	pop := func(l *[]int) string {
		v := (*l)[0]
		*l = (*l)[1:]
		return strconv.Itoa(v)
	}
	switch name {
	case "service-restarts":
		return pop(&e.restarts), nil
	case "oom-events":
		return pop(&e.oom), nil
	case "goroutines":
		return pop(&e.goroutines), nil
	case "memory-limit":
		if e.limit == "" {
			return memoryLimit + " yes\n", nil
		}
		return e.limit, nil
	}
	return "", nil
}

var calm = scenario.Stats{Sent: 1000, OK: 1000, P95: 160 * time.Millisecond}

// Подсказка зависит от признака: перезапуск, потом упор в лимит, потом опоздание.
func TestSlowRecsHint(t *testing.T) {
	for _, c := range []struct {
		name          string
		stats         scenario.Stats
		restarts, oom []int
		hint          string
		pass          bool
	}{
		{"чисто", calm, []int{3, 3}, []int{5, 5}, "", true},
		{"перезапуск без ошибок", calm, []int{3, 4}, []int{0, 0}, hintRestarts, false},
		{"перезапуск с OOM и ошибками", scenario.Stats{Sent: 1000, Failed: 50, P95: time.Second}, []int{0, 2}, []int{0, 2}, hintRestarts, false},
		{"упор в лимит без перезапуска", scenario.Stats{Sent: 1000, Failed: 500, P95: time.Second}, []int{0, 0}, []int{0, 9}, hintOOM, false},
		{"ошибки", scenario.Stats{Sent: 1000, Failed: 6, P95: 160 * time.Millisecond}, []int{0, 0}, []int{0, 0}, hintLate, false},
		{"долго", scenario.Stats{Sent: 1000, P95: time.Second}, []int{0, 0}, []int{0, 0}, hintLate, false},
	} {
		e := &fakeEnv{stats: c.stats, restarts: c.restarts, oom: c.oom}
		res := checkSlowRecs(context.Background(), e)
		if res.Pass != c.pass || res.Hint != c.hint {
			t.Errorf("%s: %+v", c.name, res)
		}
		if !slices.Contains(e.actions, "recs-normal") {
			t.Errorf("%s: замедление не снято: %v", c.name, e.actions)
		}
	}
}

func TestLimitChanged(t *testing.T) {
	for _, l := range []string{"max yes", memoryLimit + " no"} {
		res := checkSlowRecs(context.Background(), &fakeEnv{stats: calm, limit: l})
		if res.Pass || res.Hint != hintLimit {
			t.Errorf("%s: %+v", l, res)
		}
	}
	// Процесса нет и после ожидания: подсказка про лимит увела бы не туда.
	if res := checkSlowRecs(context.Background(), &fakeEnv{stats: calm, limit: memoryLimit + " none"}); res.Pass || res.Hint == hintLimit {
		t.Errorf("none: %+v", res)
	}
}

func TestGoroutinesHint(t *testing.T) {
	for _, c := range []struct {
		name          string
		restarts, oom []int
		goroutines    int
		hint          string
		pass          bool
	}{
		{"завершились", []int{0, 0}, []int{0, 0}, maxStuck, "", true},
		{"копятся", []int{0, 0}, []int{0, 0}, maxStuck + 1, hintGoroutines, false},
		// Перезапуск обнуляет горутины: верить им тогда нельзя.
		{"сброшены перезапуском", []int{0, 1}, []int{0, 1}, 0, hintRestarts, false},
	} {
		e := &fakeEnv{stats: calm, restarts: c.restarts, oom: c.oom, goroutines: []int{c.goroutines}}
		res := checkGoroutines(context.Background(), e)
		if res.Pass != c.pass || res.Hint != c.hint {
			t.Errorf("%s: %+v", c.name, res)
		}
		// Счётчики перечитываются после горутин: перезапуск во время паузы тоже виден.
		g := slices.Index(e.actions, "goroutines")
		if g < 0 || !slices.Contains(e.actions[g:], "service-restarts") {
			t.Errorf("%s: порядок чтения %v", c.name, e.actions)
		}
	}
}

// Ожидаемые рекомендации совпадают с заглушкой: лучший кандидат — i=27, так как 27*37 = 999.
func TestExpectedRecs(t *testing.T) {
	got := expectedRecs(150)
	if len(got) != 5 || got[0] != (150*13+27)%1000+1 {
		t.Fatalf("%v", got)
	}
}

// Профиль в формате debug=1: слушатели не в счёт, замыкание внутри main — в счёт.
const goroutineProfile = `goroutine profile: total 15
1 @ 0x1 0x2
#	0x10	net/http.(*Server).Serve+0x1	/usr/lib/go/src/net/http/server.go:3360
#	0x11	net/http.ListenAndServe+0x1	/usr/lib/go/src/net/http/server.go:3659
#	0x12	main.main+0x1	/srv/app/main.go:41

1 @ 0x1 0x3
#	0x11	net/http.ListenAndServe+0x1	/usr/lib/go/src/net/http/server.go:3659
#	0x13	main.main.func1+0x1	/srv/app/main.go:34

5 @ 0x1 0x4
#	0x14	runtime.chansend1+0x1	/usr/lib/go/src/runtime/chan.go:161
#	0x15	main.main.func2.1+0x1	/srv/app/main.go:50

3 @ 0x1 0x5
#	0x14	runtime.chansend1+0x1	/usr/lib/go/src/runtime/chan.go:161
#	0x16	main.(*server).getProduct.func1+0x1	/srv/app/products.go:37

4 @ 0x1 0x6
#	0x17	runtime.gopark+0x1	/usr/lib/go/src/runtime/proc.go:435
`

func TestGoroutinesAction(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\ncat <<'END'\n" + goroutineProfile + "END\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "image/actions/goroutines")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "8" {
		t.Fatalf("горутин в коде сервиса %s, ждали 8", got)
	}
}

// Лимит в проверке и подсказке — тот же, что MEMORY_MAX в service.env.
func TestMemoryLimitMatchesEnv(t *testing.T) {
	env, err := os.ReadFile("image/service.env")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^MEMORY_MAX=(\d+)M$`).FindSubmatch(env)
	if m == nil {
		t.Fatal("в service.env нет MEMORY_MAX=<N>M")
	}
	mib, _ := strconv.Atoi(string(m[1]))
	if want := strconv.Itoa(mib << 20); memoryLimit != want {
		t.Fatalf("memoryLimit %s, а MEMORY_MAX=%sM — это %s", memoryLimit, m[1], want)
	}
	if !strings.Contains(hintsText, memoryLimit) || !strings.Contains(hintsText, string(m[1])+" МиБ") {
		t.Fatalf("hints.md не называет лимит %s МиБ = %s", m[1], memoryLimit)
	}
}
