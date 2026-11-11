// Package lab управляет стендами: один запуск сценария для одного пользователя.
package lab

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"incident-lab/platform/internal/agentclient"
	"incident-lab/platform/internal/agentproto"
	"incident-lab/platform/internal/load"
	"incident-lab/platform/internal/ports"
	"incident-lab/platform/internal/verifier"
	"incident-lab/platform/scenario"
)

// Состояния стенда.
const (
	StateBooting   = "booting"
	StateHealthy   = "healthy"
	StateAlert     = "alert"
	StateVerifying = "verifying"
	StateResolved  = "resolved"
	StateEnded     = "ended"
)

const (
	bootTimeout = 90 * time.Second
	alertWindow = 10 // секунд
	maxResponse = 64 << 10
	maxEvents   = 500
	lagTimeout  = 3 * time.Second // предел одного опроса
	maxLag      = 24 * time.Hour  // больше — вывод из песочницы не принимается
)

// ErrLimit — достигнут предел одновременных стендов.
var ErrLimit = errors.New("lab: все места заняты")

// ErrNotFound — нет такого стенда.
var ErrNotFound = errors.New("lab: стенд не найден")

// ErrBusy — стенд сейчас не может выполнить действие.
var ErrBusy = errors.New("lab: стенд занят")

var errNoAnswer = errors.New("сервис не ответил")

// Limits — ограничения каркаса.
type Limits struct {
	MaxLabs     int
	MaxLifetime time.Duration
	IdleTimeout time.Duration
}

// Event — запись ленты стенда. Текст может содержать путь от пользователя: только показывать.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// Hint — открытая подсказка.
type Hint struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// View — снимок стенда для API. Неоткрытые подсказки сюда не попадают.
type View struct {
	ID       string           `json:"id"`
	Scenario string           `json:"scenario"`
	Title    string           `json:"title"`
	Briefing string           `json:"briefing"`
	Debrief  string           `json:"debrief,omitempty"`
	Early    bool             `json:"debriefEarly"`     // разбор открыт до успешной проверки
	Solved   *time.Time       `json:"solved,omitempty"` // первая успешная проверка
	State    string           `json:"state"`
	Seed     uint64           `json:"seed,string"`
	Created  time.Time        `json:"created"`
	Events   []Event          `json:"events"`
	Report   *verifier.Report `json:"report,omitempty"`
	Hints    []Hint           `json:"hints"`
	HintsAll int              `json:"hintsTotal"`
	Alert    string           `json:"alertText"`
	FixHow   []string         `json:"fixHow"`
	LagTitle string           `json:"lagTitle,omitempty"` // пусто — графика отставания нет
}

// Lab — запущенный стенд.
type Lab struct {
	id   string
	sc   scenario.Scenario
	seed uint64
	box  ports.Sandbox
	api  *agentclient.Client
	rt   *http.Transport
	rec  *load.Recorder

	mu       sync.Mutex
	state    string
	created  time.Time
	touched  time.Time
	events   []Event
	report   *verifier.Report
	attempts uint64
	hints    int       // сколько подсказок открыто
	debrief  bool      // разбор открыт; обратно не закрывается
	early    bool      // разбор открыт подсказками до успеха
	solved   time.Time // первая успешная проверка
	stopLoad func()
	ctx      context.Context // живёт, пока жив стенд
	cancel   context.CancelFunc
}

// Manager владеет всеми стендами.
type Manager struct {
	provider  ports.SandboxProvider
	scenarios map[string]scenario.Scenario
	limits    Limits

	mu   sync.Mutex
	labs map[string]*Lab
}

// NewManager создаёт менеджер.
func NewManager(p ports.SandboxProvider, list []scenario.Scenario, l Limits) *Manager {
	m := &Manager{provider: p, scenarios: map[string]scenario.Scenario{}, limits: l, labs: map[string]*Lab{}}
	for _, s := range list {
		m.scenarios[s.ID] = s
	}
	return m
}

// ScenarioInfo описывает задачу в списке на главной.
type ScenarioInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Stack   string `json:"stack"`
}

// Scenarios возвращает список задач в постоянном порядке.
func (m *Manager) Scenarios() []ScenarioInfo {
	out := make([]ScenarioInfo, 0, len(m.scenarios))
	for _, s := range m.scenarios {
		out = append(out, ScenarioInfo{ID: s.ID, Title: s.Title, Summary: s.Summary, Stack: s.Stack})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

func randomID() (string, uint64, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(b[:16]), binary.LittleEndian.Uint64(b[16:]), nil
}

// Start резервирует место и запускает стенд в фоне. Идентификатор — случайная
// строка на 128 бит: он же служит ключом доступа к стенду. seed == 0 — случайный сид.
func (m *Manager) Start(scenarioID string, seed uint64) (*Lab, error) {
	sc, ok := m.scenarios[scenarioID]
	if !ok {
		return nil, ErrNotFound
	}
	id, rnd, err := randomID()
	if err != nil {
		return nil, err
	}
	if seed == 0 {
		seed = rnd
	}
	m.mu.Lock()
	if m.activeLocked() >= m.limits.MaxLabs {
		m.mu.Unlock()
		return nil, ErrLimit
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.limits.MaxLifetime)
	now := time.Now()
	l := &Lab{id: id, sc: sc, seed: seed, rec: &load.Recorder{}, state: StateBooting, created: now, touched: now, ctx: ctx, cancel: cancel}
	m.labs[id] = l
	m.mu.Unlock()
	l.event("start", "Стенд создаётся")
	go m.run(ctx, l)
	return l, nil
}

// activeLocked считает стенды, чья машина ещё существует: состояние ended ставится после уничтожения.
// Записи завершённых доживают свою минуту, но места не занимают.
func (m *Manager) activeLocked() int {
	n := 0
	for _, l := range m.labs {
		if l.View().State != StateEnded {
			n++
		}
	}
	return n
}

func (m *Manager) run(ctx context.Context, l *Lab) {
	defer m.remove(l)
	if err := l.boot(ctx, m.provider); err != nil {
		// Текст ошибки содержит пути хоста и данные из песочницы.
		slog.Error("стенд не запустился", "lab", l.id, "err", err)
		l.event("error", "Стенд не запустился")
		return
	}
	defer l.box.Close()
	// Сначала нагрузка: в состоянии healthy проверка уже может её остановить.
	l.startLoad(ctx, l.seed, true)
	l.setState(StateHealthy)
	l.event("ready", "Сервис запущен, нагрузка пошла")

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			l.event("end", "Стенд остановлен")
			return
		case <-l.box.Done():
			l.event("end", "Песочница завершилась")
			return
		case now := <-tick.C:
			l.watch(now)
			if now.Sub(l.lastTouched()) > m.limits.IdleTimeout {
				l.event("end", "Стенд остановлен: никто не смотрит")
				return
			}
		}
	}
}

func (l *Lab) boot(ctx context.Context, p ports.SandboxProvider) error {
	ctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	box, err := p.Create(ctx, l.sc.ID)
	if err != nil {
		return err
	}
	api := agentclient.New(box)
	if err := api.Ready(ctx); err != nil {
		box.Close()
		return err
	}
	l.mu.Lock()
	l.box, l.api, l.rt = box, api, agentclient.Transport(box, agentproto.PortService)
	l.mu.Unlock()
	return nil
}

// startLoad гонит нагрузку сценария. С quiet её начало идёт тихой частью из Rise,
// без него сразу поднятая нагрузка: после проверки инцидент не должен затихать.
func (l *Lab) startLoad(ctx context.Context, seed uint64, quiet bool) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p, r := l.sc.Load, l.sc.Rise
		if r != nil {
			if quiet {
				calm, stop := context.WithTimeout(ctx, r.After)
				load.Run(calm, l.rt, load.NewPlan(seed, p), p.RatePerSec, l.rec)
				stop()
			}
			p = r.Load
		}
		load.Run(ctx, l.rt, load.NewPlan(seed, p), p.RatePerSec, l.rec)
	}()
	lagDone := make(chan struct{})
	go func() {
		defer close(lagDone)
		if l.sc.Lag != nil {
			pollLag(ctx, l.sc.Lag.Action, l.api.Action, l.rec)
		}
	}()
	l.mu.Lock()
	l.stopLoad = func() { cancel(); <-done; <-lagDone }
	l.mu.Unlock()
}

// lagEvery — как часто опрашивать отставание; переменная ради теста.
var lagEvery = 2 * time.Second

// pollLag опрашивает отставание, пока идёт нагрузка: опросы идут по одному, каждый
// ограничен lagTimeout. Вывод приходит из песочницы, поэтому принимается только
// одно конечное число секунд от 0 до maxLag, остальное пропускается.
func pollLag(ctx context.Context, name string, action func(context.Context, string) (string, error), rec *load.Recorder) {
	t := time.NewTicker(lagEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, lagTimeout)
		out, err := action(c, name)
		cancel()
		if lag, ok := parseLag(out); err == nil && ctx.Err() == nil && ok {
			rec.AddLag(time.Now(), lag)
		}
	}
}

func parseLag(out string) (time.Duration, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil || math.IsNaN(v) || v < 0 || v > maxLag.Seconds() {
		return 0, false
	}
	return time.Duration(v * float64(time.Second)), true
}

// watch поднимает алерт по тому, что нагрузка видит снаружи.
func (l *Lab) watch(now time.Time) {
	text := alertReason(l.sc.Alert, l.rec.Since(now.Unix()-alertWindow-1, now))
	if text == "" {
		return
	}
	l.mu.Lock()
	raise := l.state == StateHealthy
	if raise {
		l.state = StateAlert
	}
	l.mu.Unlock()
	if raise {
		l.event("alert", text)
	}
}

// alertReason возвращает запись для ленты, если сработал порог. Время ответа берётся
// как медиана посекундных p95: одна медленная секунда не поднимает алерт.
func alertReason(a scenario.Alert, samples []load.Sample) string {
	sent, failed := 0, 0
	p95 := make([]int, 0, len(samples))
	var lag []int
	for _, s := range samples {
		sent += s.Sent
		failed += s.Failed
		if s.Sent > 0 {
			p95 = append(p95, s.P95Ms)
		}
		if s.LagMs != nil {
			lag = append(lag, *s.LagMs)
		}
	}
	// Отставание проверяется первым: при вставшей фоновой работе запросы могут идти без ошибок.
	if len(lag) > 0 && a.MaxLag > 0 {
		sort.Ints(lag)
		if mid := lag[len(lag)/2]; time.Duration(mid)*time.Millisecond > a.MaxLag {
			return fmt.Sprintf("АЛЕРТ: отставание %d с за %d с", mid/1000, alertWindow)
		}
	}
	if sent == 0 {
		return ""
	}
	if a.MinErrorRate > 0 && float64(failed)/float64(sent) >= a.MinErrorRate {
		return fmt.Sprintf("АЛЕРТ: %d из %d запросов с ошибкой за %d с", failed, sent, alertWindow)
	}
	sort.Ints(p95)
	if mid := p95[len(p95)/2]; a.MaxP95 > 0 && time.Duration(mid)*time.Millisecond > a.MaxP95 {
		return fmt.Sprintf("АЛЕРТ: время ответа %d мс за %d с", mid, alertWindow)
	}
	return ""
}

func (l *Lab) setState(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state = s
}

func (l *Lab) event(kind, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, Event{At: time.Now(), Kind: kind, Text: text})
	if len(l.events) > maxEvents {
		l.events = append(l.events[:0], l.events[len(l.events)-maxEvents:]...)
	}
}

func (l *Lab) lastTouched() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.touched
}

func (m *Manager) remove(l *Lab) {
	l.cancel()
	l.setState(StateEnded)
	// Запись держим недолго, чтобы браузер успел показать причину остановки.
	time.AfterFunc(time.Minute, func() {
		m.mu.Lock()
		delete(m.labs, l.id)
		m.mu.Unlock()
	})
}

// Get находит стенд и отмечает, что им пользуются.
func (m *Manager) Get(id string) (*Lab, error) {
	m.mu.Lock()
	l, ok := m.labs[id]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	l.mu.Lock()
	l.touched = time.Now()
	l.mu.Unlock()
	return l, nil
}

// Stop уничтожает стенд.
func (m *Manager) Stop(id string) error {
	l, err := m.Get(id)
	if err != nil {
		return err
	}
	l.cancel()
	return nil
}

// View возвращает снимок стенда. Разбор виден после успешной проверки или когда открыты
// все подсказки, и с тех пор не закрывается.
func (l *Lab) View() View {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := View{ID: l.id, Scenario: l.sc.ID, Title: l.sc.Title, Briefing: l.sc.Briefing, State: l.state,
		Seed: l.seed, Created: l.created, Events: append([]Event{}, l.events...), Report: l.report,
		Hints: []Hint{}, HintsAll: len(l.sc.Hints), Alert: l.sc.Alert.Text, FixHow: l.sc.FixHow}
	if l.sc.Lag != nil {
		v.LagTitle = l.sc.Lag.Title
	}
	for _, h := range l.sc.Hints[:l.hints] {
		v.Hints = append(v.Hints, Hint{Title: h.Title, Text: h.Text})
	}
	if l.debrief {
		v.Debrief, v.Early = l.sc.Debrief, l.early
	}
	if !l.solved.IsZero() {
		at := l.solved
		v.Solved = &at
	}
	return v
}

// OpenHint открывает подсказку с номером n, считая с единицы. Открыть можно только
// следующую: повторный клик ничего не меняет и не открывает лишнюю. Подсказки говорят
// о сбое, поэтому до алерта или неудачной проверки они закрыты.
func (l *Lab) OpenHint(n int) error {
	l.mu.Lock()
	if l.state == StateResolved || l.state == StateEnded || (l.state != StateAlert && l.report == nil) {
		l.mu.Unlock()
		return ErrBusy
	}
	opened := n == l.hints+1 && n <= len(l.sc.Hints)
	total := len(l.sc.Hints)
	debrief := opened && n == total && !l.debrief
	if opened {
		l.hints = n
	}
	if debrief {
		l.debrief, l.early = true, true
	}
	l.mu.Unlock()
	if opened {
		l.event("hint", fmt.Sprintf("Открыта подсказка %d из %d", n, total))
	}
	if debrief {
		l.event("debrief", "Открыт разбор полётов до успешной проверки")
	}
	return nil
}

// Metrics возвращает наблюдения после момента t.
func (l *Lab) Metrics(t int64) []load.Sample { return l.rec.Since(t, time.Now()) }

func (l *Lab) running() (*agentclient.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.api == nil || l.state == StateEnded {
		return nil, ErrBusy
	}
	return l.api, nil
}

// Files возвращает файлы учебного сервиса.
func (l *Lab) Files(ctx context.Context) ([]string, error) {
	c, err := l.running()
	if err != nil {
		return nil, err
	}
	return c.Files(ctx)
}

// ReadFile читает файл учебного сервиса.
func (l *Lab) ReadFile(ctx context.Context, path string) ([]byte, error) {
	c, err := l.running()
	if err != nil {
		return nil, err
	}
	return c.ReadFile(ctx, path)
}

// WriteFile сохраняет файл учебного сервиса.
func (l *Lab) WriteFile(ctx context.Context, path string, data []byte) error {
	c, err := l.running()
	if err != nil {
		return err
	}
	if err := c.WriteFile(ctx, path, data); err != nil {
		return err
	}
	l.event("edit", "Сохранён файл "+path)
	return nil
}

// Terminal открывает поток к терминалу песочницы.
func (l *Lab) Terminal(ctx context.Context) (io.ReadWriteCloser, error) {
	l.mu.Lock()
	box := l.box
	l.mu.Unlock()
	if box == nil {
		return nil, ErrBusy
	}
	return box.Dial(ctx, agentproto.PortPTY)
}

// Verify запускает проверку починки в фоне. Отчёт появится в View.
func (l *Lab) Verify() error {
	l.mu.Lock()
	if l.stopLoad == nil || (l.state != StateHealthy && l.state != StateAlert && l.state != StateResolved) {
		l.mu.Unlock()
		return ErrBusy
	}
	prev := l.state
	l.state = StateVerifying
	l.attempts++
	attempt := l.attempts
	stop := l.stopLoad
	l.mu.Unlock()
	l.event("verify", fmt.Sprintf("Проверка починки №%d начата", attempt))
	go func() {
		stop()
		e := &env{l: l, seed: l.seed + attempt}
		rep := verifier.Run(l.ctx, l.sc.Checks, e)
		rep.LoadFailed = int(e.failed.Load())
		// Стенд остановили во время проверки: нагрузку не возвращаем, состояние не трогаем.
		if l.ctx.Err() != nil {
			return
		}
		next := StateResolved
		text := "Проверка пройдена, инцидент закрыт"
		if !rep.Passed {
			next = prev
			if next == StateResolved {
				next = StateAlert
			}
			text = "Проверка не пройдена: " + firstFailure(rep)
		}
		l.startLoad(l.ctx, l.seed+attempt, false)
		l.mu.Lock()
		l.report = &rep
		l.state = next
		if rep.Passed {
			l.debrief = true
			if l.solved.IsZero() {
				l.solved = time.Now()
			}
		}
		l.mu.Unlock()
		l.event("verify", text)
	}()
	return nil
}

func firstFailure(r verifier.Report) string {
	for _, i := range r.Items {
		if !i.Pass {
			return i.Name
		}
	}
	return "нет проверок"
}

// env — среда проверки. Запросы идут через тот же канал, что и нагрузка.
type env struct {
	l      *Lab
	seed   uint64
	failed atomic.Int64
}

func (e *env) Do(ctx context.Context, method, path, body string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://service"+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	resp, err := e.l.rt.RoundTrip(req)
	if err != nil {
		// Текст транспортной ошибки содержит путь сокета на хосте, а детали видны пользователю.
		return 0, nil, errNoAnswer
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	return resp.StatusCode, data, err
}

// RunLoad сообщает об ошибке, если бюджет проверки кончился раньше срока: укороченный
// прогон иначе выглядел бы успешным.
func (e *env) RunLoad(parent context.Context, p scenario.LoadProfile, d time.Duration) (scenario.Stats, error) {
	ctx, cancel := context.WithTimeout(parent, d)
	defer cancel()
	st := load.Run(ctx, e.l.rt, load.NewPlan(e.seed, p), p.RatePerSec, e.l.rec)
	e.failed.Add(int64(st.Failed))
	return st, parent.Err()
}

func (e *env) Action(ctx context.Context, name string) (string, error) {
	return e.l.api.Action(ctx, name)
}
