package lab

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"incident-lab/platform/internal/agentproto"
	"incident-lab/platform/internal/load"
	"incident-lab/platform/internal/ports"
	"incident-lab/platform/scenario"
)

// fakeBox — песочница в памяти: агент и сервис отвечают обычным HTTP.
type fakeBox struct {
	done     chan struct{}
	once     sync.Once
	requests atomic.Int64
	raised   atomic.Int64 // запросы поднятой нагрузки
	srv      map[uint32]*http.Server
	ln       map[uint32]*memListener
}

type memListener struct {
	ch   chan net.Conn
	done chan struct{}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *memListener) Close() error   { return nil }
func (l *memListener) Addr() net.Addr { return &net.UnixAddr{} }

func newFakeBox() *fakeBox {
	b := &fakeBox{done: make(chan struct{}), srv: map[uint32]*http.Server{}, ln: map[uint32]*memListener{}}
	handlers := map[uint32]http.HandlerFunc{
		agentproto.PortControl: func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) },
		agentproto.PortService: func(w http.ResponseWriter, r *http.Request) {
			b.requests.Add(1)
			if r.URL.Path == "/raised" {
				b.raised.Add(1)
			}
		},
	}
	for port, h := range handlers {
		l := &memListener{ch: make(chan net.Conn), done: b.done}
		s := &http.Server{Handler: h}
		b.srv[port], b.ln[port] = s, l
		go s.Serve(l)
	}
	return b
}

func (b *fakeBox) ID() string            { return "fake" }
func (b *fakeBox) Done() <-chan struct{} { return b.done }
func (b *fakeBox) Close() error {
	b.once.Do(func() { close(b.done) })
	return nil
}
func (b *fakeBox) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	l, ok := b.ln[port]
	if !ok {
		return nil, errors.New("нет порта")
	}
	a, c := net.Pipe()
	select {
	case l.ch <- a:
		return c, nil
	case <-b.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type fakeProvider struct {
	mu    sync.Mutex
	boxes []*fakeBox
}

func (p *fakeProvider) Create(context.Context, string) (ports.Sandbox, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := newFakeBox()
	p.boxes = append(p.boxes, b)
	return b, nil
}

func testScenario(check scenario.Check) scenario.Scenario {
	return scenario.Scenario{ID: "s", Debrief: "разбор", Checks: []scenario.Check{check},
		Load: scenario.LoadProfile{RatePerSec: 100, Requests: []scenario.Request{{Weight: 1, Method: "GET", Path: "/"}}}}
}

func pass(context.Context, scenario.Env) scenario.Result { return scenario.Result{Pass: true} }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("не дождались: %s", what)
}

func state(l *Lab, s string) func() bool { return func() bool { return l.View().State == s } }

func newManager(p *fakeProvider, c scenario.Check, maxLabs int) *Manager {
	return NewManager(p, []scenario.Scenario{testScenario(c)}, Limits{MaxLabs: maxLabs, MaxLifetime: time.Minute, IdleTimeout: time.Minute})
}

// Завершённый стенд не занимает место, пока его запись доживает до показа в браузере.
func TestEndedLabFreesSlot(t *testing.T) {
	m := newManager(&fakeProvider{}, scenario.Check{Name: "c", Run: pass}, 1)
	l, err := m.Start("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "healthy", state(l, StateHealthy))
	if _, err := m.Start("s", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("лимит не сработал: %v", err)
	}
	if err := m.Stop(l.View().ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ended", state(l, StateEnded))
	if _, err := m.Start("s", 1); err != nil {
		t.Fatalf("место не освободилось: %v", err)
	}
}

// Остановка стенда во время проверки не оставляет работающую нагрузку.
func TestStopDuringVerifyStopsLoad(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	check := scenario.Check{Name: "c", Run: func(context.Context, scenario.Env) scenario.Result {
		close(started)
		<-release
		return scenario.Result{}
	}}
	p := &fakeProvider{}
	m := newManager(p, check, 1)
	l, err := m.Start("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "healthy", state(l, StateHealthy))
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	<-started
	m.Stop(l.View().ID)
	waitFor(t, "ended", state(l, StateEnded))
	close(release)
	box := p.boxes[0]
	time.Sleep(50 * time.Millisecond)
	before := box.requests.Load()
	time.Sleep(200 * time.Millisecond)
	if after := box.requests.Load(); after != before {
		t.Fatalf("нагрузка идёт после остановки: %d → %d", before, after)
	}
	if got := l.View().State; got != StateEnded {
		t.Fatalf("состояние %s после конца проверки на мёртвом стенде", got)
	}
}

// Проверка, вызванная сразу после перехода в healthy, не роняет сервер.
func TestVerifyRightAfterBootDoesNotPanic(t *testing.T) {
	for range 200 {
		m := newManager(&fakeProvider{}, scenario.Check{Name: "c", Run: pass}, 1)
		l, err := m.Start("s", 1)
		if err != nil {
			t.Fatal(err)
		}
		for l.Verify() != nil {
		}
		m.Stop(l.View().ID)
		waitFor(t, "ended", state(l, StateEnded))
	}
}

// Лента не растёт без предела: текст событий частично задаёт пользователь.
func TestEventsAreBounded(t *testing.T) {
	l := &Lab{}
	for i := range maxEvents * 3 {
		l.event("edit", strconv.Itoa(i))
	}
	if len(l.events) != maxEvents {
		t.Fatalf("событий %d", len(l.events))
	}
	if last := l.events[len(l.events)-1].Text; last != strconv.Itoa(maxEvents*3-1) {
		t.Fatalf("последнее событие %q", last)
	}
}

func TestVerifyResolvesAndShowsDebrief(t *testing.T) {
	m := newManager(&fakeProvider{}, scenario.Check{Name: "c", Run: pass}, 1)
	l, err := m.Start("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); !errors.Is(err, ErrBusy) {
		t.Fatalf("проверка на загружающемся стенде: %v", err)
	}
	waitFor(t, "healthy", state(l, StateHealthy))
	if l.View().Debrief != "" {
		t.Fatal("разбор виден до решения")
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resolved", state(l, StateResolved))
	if l.View().Debrief == "" {
		t.Fatal("нет разбора")
	}
}

func raisedScenario(after time.Duration) scenario.Scenario {
	sc := testScenario(scenario.Check{Name: "c", Run: pass})
	sc.Rise = &scenario.Rise{After: after, Load: scenario.LoadProfile{RatePerSec: 100,
		Requests: []scenario.Request{{Weight: 1, Method: "GET", Path: "/raised"}}}}
	return sc
}

func startRaised(t *testing.T, after time.Duration) (*Lab, *fakeBox) {
	t.Helper()
	p := &fakeProvider{}
	m := NewManager(p, []scenario.Scenario{raisedScenario(after)}, Limits{MaxLabs: 1, MaxLifetime: time.Minute, IdleTimeout: time.Minute})
	l, err := m.Start("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Stop(l.View().ID) })
	waitFor(t, "healthy", state(l, StateHealthy))
	return l, p.boxes[0]
}

func TestRiseReplacesLoad(t *testing.T) {
	_, b := startRaised(t, 50*time.Millisecond)
	waitFor(t, "поднятая нагрузка", func() bool { return b.raised.Load() > 0 })
}

// После проверки нагрузка не затихает заново: иначе провал выглядел бы как улучшение.
func TestVerifyResumesRaisedLoad(t *testing.T) {
	l, b := startRaised(t, time.Hour)
	waitFor(t, "тихая нагрузка", func() bool { return b.requests.Load() > 0 })
	if b.raised.Load() != 0 {
		t.Fatal("нагрузка поднялась раньше срока")
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "поднятая нагрузка", func() bool { return b.raised.Load() > 0 })
}

// Кончившийся бюджет проверки — ошибка: укороченный прогон выглядел бы успехом.
func TestRunLoadReportsExpiredBudget(t *testing.T) {
	l, _ := startRaised(t, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	e := &env{l: l, seed: 1}
	p := scenario.LoadProfile{RatePerSec: 10, Requests: []scenario.Request{{Weight: 1, Method: "GET", Path: "/"}}}
	if _, err := e.RunLoad(ctx, p, time.Hour); err == nil {
		t.Fatal("нет ошибки")
	}
}

// Список задач идёт на главную с описанием и в постоянном порядке.
func TestScenariosListed(t *testing.T) {
	m := NewManager(nil, []scenario.Scenario{
		{ID: "b", Title: "Второй", Summary: "что случилось", Stack: "Go"},
		{ID: "a", Title: "Первый"},
	}, Limits{})
	got := m.Scenarios()
	if len(got) != 2 || got[0].ID != "a" || got[1] != (ScenarioInfo{ID: "b", Title: "Второй", Summary: "что случилось", Stack: "Go"}) {
		t.Fatalf("%+v", got)
	}
}

// Подсказки открываются строго по порядку: следующие не видны, повтор не открывает лишнюю.
func TestHintsOpenInOrder(t *testing.T) {
	var runs atomic.Int32
	sc := testScenario(scenario.Check{Name: "c", Run: func(context.Context, scenario.Env) scenario.Result {
		return scenario.Result{Pass: runs.Add(1) > 1}
	}})
	sc.Hints = []scenario.Hint{{Title: "1", Text: "первая"}, {Title: "2", Text: "вторая"}}
	m := NewManager(&fakeProvider{}, []scenario.Scenario{sc}, Limits{MaxLabs: 1, MaxLifetime: time.Minute, IdleTimeout: time.Minute})
	l, err := m.Start("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.OpenHint(1); !errors.Is(err, ErrBusy) {
		t.Fatalf("подсказка на загружающемся стенде: %v", err)
	}
	waitFor(t, "healthy", state(l, StateHealthy))
	if err := l.OpenHint(1); !errors.Is(err, ErrBusy) {
		t.Fatalf("подсказка до сбоя: %v", err)
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "неудачная проверка", func() bool { return l.View().Report != nil })
	opened := func() int { return len(l.View().Hints) }
	if v := l.View(); opened() != 0 || v.HintsAll != 2 {
		t.Fatalf("до открытия: %+v", v.Hints)
	}
	for _, c := range []struct{ n, want int }{{2, 0}, {0, 0}, {1, 1}, {1, 1}, {3, 1}, {2, 2}, {3, 2}} {
		if err := l.OpenHint(c.n); err != nil {
			t.Fatal(err)
		}
		if opened() != c.want {
			t.Fatalf("OpenHint(%d): открыто %d, ждали %d", c.n, opened(), c.want)
		}
	}
	if got := l.View().Hints[1]; got != (Hint{Title: "2", Text: "вторая"}) {
		t.Fatalf("%+v", got)
	}
	n := 0
	for _, e := range l.View().Events {
		if e.Kind == "hint" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("в журнале %d записей о подсказках", n)
	}
	// После закрытия инцидента счётчик в разборе не меняется.
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resolved", state(l, StateResolved))
	if err := l.OpenHint(3); !errors.Is(err, ErrBusy) {
		t.Fatalf("подсказка после закрытия: %v", err)
	}
}

// Последняя подсказка открывает разбор до успеха, и это видно в снимке и журнале.
func TestLastHintOpensDebrief(t *testing.T) {
	var runs atomic.Int32
	sc := testScenario(scenario.Check{Name: "c", Run: func(context.Context, scenario.Env) scenario.Result {
		return scenario.Result{Pass: runs.Add(1) > 1}
	}})
	sc.Hints = []scenario.Hint{{Title: "1", Text: "первая"}, {Title: "2", Text: "вторая"}}
	m := NewManager(&fakeProvider{}, []scenario.Scenario{sc}, Limits{MaxLabs: 1, MaxLifetime: time.Minute, IdleTimeout: time.Minute})
	l, err := m.Start("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "healthy", state(l, StateHealthy))
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "неудачная проверка", func() bool { return l.View().Report != nil && l.View().State == StateHealthy })
	if err := l.OpenHint(1); err != nil {
		t.Fatal(err)
	}
	if v := l.View(); v.Debrief != "" || v.Early {
		t.Fatalf("разбор открыт не последней подсказкой: %+v", v)
	}
	if err := l.OpenHint(2); err != nil {
		t.Fatal(err)
	}
	v := l.View()
	if v.Debrief == "" || !v.Early || v.Solved != nil {
		t.Fatalf("после последней подсказки: debrief=%q early=%v solved=%v", v.Debrief, v.Early, v.Solved)
	}
	n := 0
	for _, e := range v.Events {
		if e.Kind == "debrief" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("в журнале %d записей об открытии разбора", n)
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resolved", state(l, StateResolved))
	if v := l.View(); !v.Early || v.Solved == nil {
		t.Fatalf("после успеха отметка пропала: early=%v solved=%v", v.Early, v.Solved)
	}
}

// Заработанный разбор не закрывается проваленной повторной проверкой, время успеха не сдвигается.
func TestDebriefStaysAfterFailedRecheck(t *testing.T) {
	var runs atomic.Int32
	m := newManager(&fakeProvider{}, scenario.Check{Name: "c", Run: func(context.Context, scenario.Env) scenario.Result {
		return scenario.Result{Pass: runs.Add(1) == 1}
	}}, 1)
	l, err := m.Start("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "healthy", state(l, StateHealthy))
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resolved", state(l, StateResolved))
	first := l.View()
	if first.Solved == nil || first.Early {
		t.Fatalf("после успеха: solved=%v early=%v", first.Solved, first.Early)
	}
	if err := l.Verify(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "повтор провален", state(l, StateAlert))
	v := l.View()
	if v.Debrief == "" {
		t.Fatal("разбор закрылся после проваленного повтора")
	}
	if !v.Solved.Equal(*first.Solved) {
		t.Fatalf("время успеха сдвинулось: %v → %v", first.Solved, v.Solved)
	}
}

func TestAlertReason(t *testing.T) {
	second := func(sent, failed, p95 int) load.Sample { return load.Sample{Sent: sent, Failed: failed, P95Ms: p95} }
	lag := func(sent, ms int) load.Sample { return load.Sample{Sent: sent, LagMs: &ms} }
	both := scenario.Alert{MinErrorRate: 0.2, MaxP95: 500 * time.Millisecond}
	lagged := scenario.Alert{MinErrorRate: 0.2, MaxLag: 30 * time.Second}
	for _, c := range []struct {
		name    string
		alert   scenario.Alert
		samples []load.Sample
		want    string // часть записи, пусто — алерта нет
	}{
		{"тишина", both, []load.Sample{second(10, 0, 3), second(10, 0, 3)}, ""},
		{"нет запросов", both, nil, ""},
		{"ошибки", both, []load.Sample{second(10, 2, 3)}, "2 из 10"},
		{"ошибки ниже порога", both, []load.Sample{second(10, 1, 3)}, ""},
		{"медленно", both, []load.Sample{second(10, 0, 900), second(10, 0, 800), second(10, 0, 3)}, "800 мс"},
		{"одна медленная секунда", both, []load.Sample{second(10, 0, 900), second(10, 0, 3), second(10, 0, 3)}, ""},
		{"порог ошибок выключен", scenario.Alert{MaxP95: time.Second}, []load.Sample{second(10, 10, 3)}, ""},
		{"порог времени выключен", scenario.Alert{MinErrorRate: 0.2}, []load.Sample{second(10, 0, 9000)}, ""},
		{"отставание", lagged, []load.Sample{lag(10, 40000), second(10, 0, 3), lag(10, 45000), lag(10, 1000)}, "ждёт 40 с"},
		{"отставание без запросов", lagged, []load.Sample{lag(0, 40000), lag(0, 50000), lag(0, 45000)}, "ждёт"},
		{"мало замеров отставания", lagged, []load.Sample{lag(10, 90000), lag(10, 90000)}, ""},
		{"чётное число замеров, выброс сверху", lagged, []load.Sample{lag(10, 1000), lag(10, 2000), lag(10, 40000), lag(10, 90000)}, ""},
		{"одно большое отставание", lagged, []load.Sample{lag(10, 90000), lag(10, 1000), lag(10, 2000)}, ""},
		{"отставание без замеров", lagged, []load.Sample{second(10, 0, 3)}, ""},
		{"порог отставания выключен", both, []load.Sample{lag(10, 90000), lag(10, 90000)}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := alertReason(c.alert, c.samples)
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Fatalf("%q, ждали %q", got, c.want)
			}
		})
	}
}

func TestParseLag(t *testing.T) {
	for in, want := range map[string]time.Duration{"12": 12 * time.Second, " 0.5\n": 500 * time.Millisecond, "0": 0} {
		if got, ok := parseLag(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	// Вывод из песочницы: всё, кроме одного разумного числа, отбрасывается.
	for _, in := range []string{"", "abc", "-1", "NaN", "Inf", "1e9", "12 13", "12s"} {
		if _, ok := parseLag(in); ok {
			t.Errorf("%q принят", in)
		}
	}
}

// Опрос пишет замеры, пропускает мусор и ошибки и завершается с контекстом: иначе
// stopLoad перед проверкой починки ждал бы его вечно.
func TestPollLag(t *testing.T) {
	var n atomic.Int32
	action := func(ctx context.Context, name string) (string, error) {
		if name != "queue-lag" {
			t.Errorf("действие %q", name)
		}
		switch n.Add(1) % 3 {
		case 0:
			return "", errors.New("нет ответа")
		case 1:
			return "мусор", nil
		}
		return "7", nil
	}
	rec := &load.Recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { pollLag(ctx, 5*time.Millisecond, "queue-lag", action, rec); close(done) }()
	for n.Load() < 9 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("опрос не остановился")
	}
	got := 0
	for _, s := range rec.Since(0, time.Now().Add(time.Second)) {
		if s.LagMs != nil {
			got++
			if *s.LagMs != 7000 {
				t.Fatalf("%d", *s.LagMs)
			}
		}
	}
	if got == 0 {
		t.Fatal("нет замеров")
	}
}
