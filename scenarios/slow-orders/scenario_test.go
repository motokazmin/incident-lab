package sloworders

import (
	"context"
	"testing"
	"time"

	"incident-lab/platform/scenario"
)

func TestExpectedLists(t *testing.T) {
	got := wantRegular(15000)
	if len(got) != 20 || got[0] != 3994999 || got[1] != 3974999 || got[0]%20000+1 != 15000 {
		t.Fatalf("список: %v", got)
	}
	if r := wantRare(rareFirst); r[0] != 3 || r[2] != 1 {
		t.Fatalf("редкий: %v", r)
	}
	if r := wantRare(rareLast); r[0] != 600 {
		t.Fatalf("редкий: %v", r)
	}
}

type fakeEnv struct {
	stats  scenario.Stats
	status int
	body   string
}

func (e fakeEnv) Do(context.Context, string, string, string) (int, []byte, error) {
	return e.status, []byte(e.body), nil
}

func (e fakeEnv) RunLoad(context.Context, scenario.LoadProfile, time.Duration) (scenario.Stats, error) {
	return e.stats, nil
}

func (e fakeEnv) Action(context.Context, string) (string, error) { return "", nil }

// Быстрый пустой ответ нагрузке не виден: успех решает сверка.
func TestSurgeRejectsWrongLists(t *testing.T) {
	fast := scenario.Stats{Sent: 100, OK: 100, P95: time.Millisecond}
	res := checkSurge(context.Background(), fakeEnv{stats: fast, status: 200, body: `{"orders":[]}`})
	if res.Pass || res.Hint != hintLists {
		t.Fatalf("%+v", res)
	}
}

// Отказ под нагрузкой не считается неверным списком, иначе подсказка уводит от причины.
func TestSurgeOverloadIsNotWrongList(t *testing.T) {
	slow := scenario.Stats{Sent: 100, Failed: 40, P95: time.Second}
	res := checkSurge(context.Background(), fakeEnv{stats: slow, status: 503})
	if res.Pass || res.Hint != hintSlow {
		t.Fatalf("%+v", res)
	}
}

// Заглушка мимо сверки не знает заранее, кого спросят: обход покрывает диапазоны.
func TestProbesCoverRanges(t *testing.T) {
	seen := map[int]bool{}
	for i := range 250 {
		c, want := probeAt(i)
		if c >= buyerFirst && c <= buyerLast {
			t.Fatalf("покупатель %d меняется нагрузкой", c)
		}
		if len(want) == 0 {
			t.Fatalf("покупатель %d: пустой список", c)
		}
		seen[c] = true
	}
	if len(seen) < 200 {
		t.Fatalf("обошли %d покупателей", len(seen))
	}
}
