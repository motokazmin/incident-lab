package load

import (
	"context"
	"net/http"
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
