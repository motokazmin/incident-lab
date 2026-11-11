package load

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"incident-lab/platform/scenario"
)

var profile = scenario.LoadProfile{RatePerSec: 200, Requests: []scenario.Request{
	{Weight: 3, Method: "GET", Path: "/a/{id}", IDs: []int{1, 2, 3}},
	{Weight: 1, Method: "POST", Path: "/b", Body: `{"x":{id}}`, IDs: []int{7, 8}, Expected: []int{409}},
}}

func seq(seed uint64, n int) []string {
	p := NewPlan(seed, profile)
	out := make([]string, n)
	for i := range out {
		r := p.Next()
		out[i] = r.Method + r.Path + r.Body
	}
	return out
}

func TestPlanDeterministic(t *testing.T) {
	a, b, c := seq(1, 500), seq(1, 500), seq(2, 500)
	diff := false
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("сид 1 разошёлся на %d: %s != %s", i, a[i], b[i])
		}
		diff = diff || a[i] != c[i]
	}
	if !diff {
		t.Fatal("разные сиды дали одну последовательность")
	}
}

func TestSuccess(t *testing.T) {
	for _, c := range []struct {
		st   int
		exp  []int
		want bool
	}{{200, nil, true}, {201, nil, true}, {409, nil, false}, {409, []int{409}, true}, {500, []int{409}, false}} {
		if Success(c.st, c.exp) != c.want {
			t.Errorf("%d %v", c.st, c.exp)
		}
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Зависший сервис не замедляет нагрузку и считается ошибкой.
func TestHungServiceCountsAsFailure(t *testing.T) {
	var calls atomic.Int64
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	rec := &Recorder{}
	st := Run(ctx, rt, NewPlan(1, profile), 200, rec)
	if st.Sent < 40 || st.OK != 0 || st.Failed != st.Sent {
		t.Fatalf("%+v", st)
	}
	if got := int(calls.Load()); got != st.Sent {
		t.Fatalf("вызовов %d, отправлено %d", got, st.Sent)
	}
	samples := rec.Since(0, time.Now().Add(2*time.Second))
	sum := 0
	for _, s := range samples {
		sum += s.Failed
	}
	if sum != st.Failed {
		t.Fatalf("в графиках %d ошибок, в итоге %d", sum, st.Failed)
	}
}

func withBurst(b scenario.Burst) scenario.LoadProfile {
	p := profile
	p.Burst = &b
	return p
}

var burstReqs = []scenario.Request{{Weight: 1, Method: "POST", Path: "/burst/{id}", IDs: []int{1, 2, 3, 4}}}

// Залпы берут случайность из своего потока: фон сюжета с залпами и без них один и тот же,
// а состав залпов задан сидом.
func TestBurstKeepsBackgroundPlan(t *testing.T) {
	bursty := withBurst(scenario.Burst{Every: time.Second, Size: 4, Requests: burstReqs})
	plain, a, b, c := NewPlan(7, profile), NewPlan(7, bursty), NewPlan(7, bursty), NewPlan(8, bursty)
	diff := false
	for i := range 300 {
		x, y, z := a.NextBurst(), b.NextBurst(), c.NextBurst()
		if x.Path != y.Path {
			t.Fatalf("залп с одним сидом разошёлся на %d: %s != %s", i, x.Path, y.Path)
		}
		diff = diff || x.Path != z.Path
		if p, q := plain.Next(), a.Next(); p.Path != q.Path || p.Body != q.Body {
			t.Fatalf("фон разошёлся на %d: %v != %v", i, p, q)
		}
	}
	if !diff {
		t.Fatal("разные сиды дали одинаковые залпы")
	}
}

// Сюжет не ослабит пределы каркаса: размер залпа и частота обрезаются.
func TestBurstLimits(t *testing.T) {
	for _, c := range []struct {
		b     *scenario.Burst
		size  int
		every time.Duration
	}{
		{nil, 0, 0},
		{&scenario.Burst{Every: time.Millisecond, Size: 10000, Requests: burstReqs}, maxBurst, minBurstEvery},
		{&scenario.Burst{Every: 5 * time.Second, Size: 16, Requests: burstReqs}, 16, 5 * time.Second},
		{&scenario.Burst{Every: 5 * time.Second, Size: 16}, 0, 0},
		{&scenario.Burst{Every: 5 * time.Second, Size: 0, Requests: burstReqs}, 0, 0},
	} {
		p := profile
		p.Burst = c.b
		if size, every := NewPlan(1, p).burst(); size != c.size || every != c.every {
			t.Errorf("%+v: размер %d, раз в %s", c.b, size, every)
		}
	}
}

// Залп уходит одновременно и попадает в итог и графики, как фон.
func TestBurstIsConcurrent(t *testing.T) {
	old := minBurstEvery
	minBurstEvery = time.Millisecond
	defer func() { minBurstEvery = old }()
	var inflight, peak atomic.Int64
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.URL.Path, "/burst/") {
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		}
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-r.Context().Done() // висит до таймаута запроса: все залпы в полёте одновременно
		inflight.Add(-1)
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	rec := &Recorder{}
	st := Run(ctx, rt, NewPlan(1, withBurst(scenario.Burst{Every: 100 * time.Millisecond, Size: 8, Requests: burstReqs})), 1, rec)
	if peak.Load() != 8 {
		t.Fatalf("одновременно в полёте %d запросов залпа, ждали 8", peak.Load())
	}
	if st.Failed != 8 {
		t.Fatalf("%+v", st)
	}
	sum := 0
	for _, s := range rec.Since(0, time.Now().Add(2*time.Second)) {
		sum += s.Failed
	}
	if sum != st.Failed {
		t.Fatalf("в графиках %d ошибок, в итоге %d", sum, st.Failed)
	}
}

// Частоту ограничивает каркас: ноль и запредельное значение не роняют сервер со всеми стендами.
func TestRateIsCapped(t *testing.T) {
	ok := rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	for _, rate := range []int{0, -5, 2_000_000_000} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		st := Run(ctx, ok, NewPlan(1, profile), rate, &Recorder{})
		cancel()
		if st.Sent > maxRate/10+5 {
			t.Errorf("частота %d: за 100 мс %d запросов", rate, st.Sent)
		}
	}
}

// Замер отставания попадает в свою секунду и не считается запросом: иначе он испортил бы
// график ошибок и порог по доле ошибок.
func TestLagIsNotARequest(t *testing.T) {
	rec := &Recorder{}
	at := time.Unix(1000, 0)
	rec.AddLag(at, 1500*time.Millisecond)
	rec.add(at.Add(time.Second), 10*time.Millisecond, true)
	got := rec.Since(0, at.Add(5*time.Second))
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].Sent != 0 || got[0].LagMs == nil || *got[0].LagMs != 1500 {
		t.Fatalf("секунда замера: %+v", got[0])
	}
	if got[1].Sent != 1 || got[1].LagMs != nil {
		t.Fatalf("секунда запроса без замера: %+v", got[1])
	}
}
