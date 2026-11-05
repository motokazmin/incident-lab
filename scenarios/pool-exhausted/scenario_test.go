package poolexhausted

import (
	"context"
	"testing"
	"time"

	"incident-lab/platform/scenario"
)

type fakeEnv struct {
	stats  scenario.Stats
	leaked string
}

func (e fakeEnv) Do(context.Context, string, string, string) (int, []byte, error) {
	return 0, nil, nil
}

func (e fakeEnv) RunLoad(context.Context, scenario.LoadProfile, time.Duration) (scenario.Stats, error) {
	return e.stats, nil
}

func (e fakeEnv) Action(_ context.Context, name string) (string, error) {
	if name == "leaked-tx" {
		return e.leaked, nil
	}
	return "0", nil
}

// Провал по ошибкам на проверке под нагрузкой: подсказка про пул только при живой утечке.
func TestSurgeHintFollowsLeak(t *testing.T) {
	over := scenario.Stats{Sent: 1000, Failed: 6, P95: time.Millisecond}
	for _, c := range []struct {
		leaked, hint string
	}{{"3", hintBigPool}, {"0", ""}} {
		res := checkSurge(context.Background(), fakeEnv{stats: over, leaked: c.leaked})
		if res.Pass || res.Hint != c.hint {
			t.Fatalf("зависло %s: %+v", c.leaked, res)
		}
	}
}
