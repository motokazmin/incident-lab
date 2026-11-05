package lab

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"incident-lab/platform/internal/agentproto"
	"incident-lab/platform/internal/ports"
	"incident-lab/platform/scenario"
)

// fakeBox — песочница в памяти: агент и сервис отвечают обычным HTTP.
type fakeBox struct {
	done     chan struct{}
	once     sync.Once
	requests atomic.Int64
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
		agentproto.PortService: func(w http.ResponseWriter, _ *http.Request) { b.requests.Add(1) },
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
	return scenario.Scenario{ID: "s", Image: "s", Debrief: "разбор", Checks: []scenario.Check{check},
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
