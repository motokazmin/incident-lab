// Package load — генератор нагрузки снаружи песочницы. Время и статусы измеряет
// каркас, но отвечает на запросы машина пользователя, поэтому подлинность ответов
// это не доказывает, см. ADR 0004. План запросов задан сидом, порядок прихода — нет.
package load

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"incident-lab/platform/scenario"
)

const (
	requestTimeout = 1500 * time.Millisecond
	maxInflight    = 256
	maxBuckets     = 7200
	maxRespBody    = 64 << 10
	maxRate        = 1000
	maxBurst       = 64
)

// minBurstEvery — залпы не чаще: сюжет не превратит их в постоянную нагрузку. Тест укорачивает.
var minBurstEvery = time.Second

// Req — конкретный запрос последовательности.
type Req struct {
	Method, Path, Body string
	Expected           []int
}

// Plan выдаёт последовательность запросов, полностью заданную сидом. У залпов свой
// поток случайных чисел: фон не зависит от того, есть ли залпы.
type Plan struct {
	rng, burstRng     *rand.Rand
	profile           scenario.LoadProfile
	total, burstTotal int
}

// NewPlan создаёт последовательность.
func NewPlan(seed uint64, p scenario.LoadProfile) *Plan {
	pl := &Plan{rng: rand.New(rand.NewPCG(seed, 0x1ab)), burstRng: rand.New(rand.NewPCG(seed, 0xb5)),
		profile: p, total: weight(p.Requests)}
	if p.Burst != nil {
		pl.burstTotal = weight(p.Burst.Requests)
	}
	return pl
}

func weight(reqs []scenario.Request) int {
	total := 0
	for _, r := range reqs {
		total += r.Weight
	}
	return total
}

// Next возвращает следующий запрос фона.
func (p *Plan) Next() Req {
	return pick(p.rng, p.profile.Requests, p.total)
}

// NextBurst возвращает следующий запрос залпа.
func (p *Plan) NextBurst() Req {
	return pick(p.burstRng, p.profile.Burst.Requests, p.burstTotal)
}

// burst возвращает размер залпа и промежуток между залпами в пределах каркаса; 0 — залпов нет.
func (p *Plan) burst() (int, time.Duration) {
	b := p.profile.Burst
	if b == nil || p.burstTotal <= 0 || b.Size <= 0 {
		return 0, 0
	}
	return min(b.Size, maxBurst), max(b.Every, minBurstEvery)
}

func pick(rng *rand.Rand, reqs []scenario.Request, total int) Req {
	n := rng.IntN(total)
	for _, r := range reqs {
		if n >= r.Weight {
			n -= r.Weight
			continue
		}
		out := Req{Method: r.Method, Path: r.Path, Body: r.Body, Expected: r.Expected}
		if len(r.IDs) > 0 {
			id := strconv.Itoa(r.IDs[rng.IntN(len(r.IDs))])
			out.Path = strings.ReplaceAll(out.Path, "{id}", id)
			out.Body = strings.ReplaceAll(out.Body, "{id}", id)
		}
		return out
	}
	panic("load: пустой профиль")
}

// Sample — одна секунда наблюдений.
type Sample struct {
	T      int64 `json:"t"`
	Sent   int   `json:"sent"`
	Failed int   `json:"failed"`
	P50Ms  int   `json:"p50ms"`
	P95Ms  int   `json:"p95ms"`
	LagMs  *int  `json:"lagms,omitempty"` // пусто — в эту секунду отставание не замеряли
}

type bucket struct {
	t      int64
	sent   int
	failed int
	lat    []time.Duration
	lag    *int
}

// Recorder копит посекундные наблюдения.
type Recorder struct {
	mu      sync.Mutex
	buckets []*bucket
}

func (r *Recorder) add(at time.Time, lat time.Duration, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.at(at.Unix())
	b.sent++
	if !ok {
		b.failed++
	}
	b.lat = append(b.lat, lat)
}

// AddLag записывает замер отставания. Запросом он не считается.
func (r *Recorder) AddLag(at time.Time, lag time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ms := int(lag.Milliseconds())
	r.at(at.Unix()).lag = &ms
}

// at возвращает корзину секунды t, создавая её. Вызывается под r.mu.
func (r *Recorder) at(t int64) *bucket {
	var b *bucket
	for i := len(r.buckets) - 1; i >= 0 && i >= len(r.buckets)-5; i-- {
		if r.buckets[i].t == t {
			b = r.buckets[i]
			break
		}
	}
	if b == nil {
		b = &bucket{t: t}
		r.buckets = append(r.buckets, b)
		slices.SortFunc(r.buckets, func(x, y *bucket) int { return int(x.t - y.t) })
		if len(r.buckets) > maxBuckets {
			r.buckets = r.buckets[len(r.buckets)-maxBuckets:]
		}
	}
	return b
}

// Since возвращает завершённые секунды после t.
func (r *Recorder) Since(t int64, now time.Time) []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Sample{}
	for _, b := range r.buckets {
		if b.t <= t || b.t >= now.Unix() {
			continue
		}
		lat := slices.Clone(b.lat)
		out = append(out, Sample{T: b.t, Sent: b.sent, Failed: b.failed,
			P50Ms: int(percentile(lat, 50).Milliseconds()), P95Ms: int(percentile(lat, 95).Milliseconds()), LagMs: b.lag})
	}
	return out
}

func percentile(l []time.Duration, p int) time.Duration {
	if len(l) == 0 {
		return 0
	}
	slices.Sort(l)
	return l[(len(l)-1)*p/100]
}

// Run шлёт запросы с постоянной частотой, не дожидаясь ответов: зависший сервис
// не должен замедлять нагрузку. Запрос, отправленный после отмены, не считается.
// Залпы идут поверх фона и делят с ним предел одновременных запросов.
func Run(ctx context.Context, rt http.RoundTripper, plan *Plan, rate int, rec *Recorder) scenario.Stats {
	var (
		mu  sync.Mutex
		st  scenario.Stats
		lat []time.Duration
		wg  sync.WaitGroup
	)
	note := func(at time.Time, d time.Duration, ok bool) {
		rec.add(at, d, ok)
		mu.Lock()
		defer mu.Unlock()
		st.Sent++
		if ok {
			st.OK++
		} else {
			st.Failed++
		}
		lat = append(lat, d)
	}
	sem := make(chan struct{}, maxInflight)
	fire := func(at time.Time, r Req) {
		select {
		case sem <- struct{}{}:
		default:
			note(at, requestTimeout, false)
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			ok := send(rt, r)
			note(start, time.Since(start), ok)
		}()
	}
	tick := time.NewTicker(time.Second / time.Duration(min(max(rate, 1), maxRate)))
	defer tick.Stop()
	var burstC <-chan time.Time // nil — ветка залпа не срабатывает
	size, every := plan.burst()
	if size > 0 {
		bt := time.NewTicker(every)
		defer bt.Stop()
		burstC = bt.C
	}
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case at := <-tick.C:
			fire(at, plan.Next())
		case at := <-burstC:
			for range size {
				fire(at, plan.NextBurst())
			}
		}
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	st.P95 = percentile(lat, 95)
	return st
}

// send живёт на своём таймауте, чтобы остановка нагрузки не записала отменённые
// запросы в ошибки сервиса.
func send(rt http.RoundTripper, r Req) bool {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, "http://service"+r.Path, strings.NewReader(r.Body))
	if err != nil {
		return false
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxRespBody)); err != nil {
		return false
	}
	return Success(resp.StatusCode, r.Expected)
}

// Success решает, считать ли статус успехом.
func Success(status int, expected []int) bool {
	return (status >= 200 && status < 300) || slices.Contains(expected, status)
}
