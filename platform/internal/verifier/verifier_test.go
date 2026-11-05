package verifier

import (
	"context"
	"testing"

	"incident-lab/platform/scenario"
)

func check(pass bool, ran *int) scenario.Check {
	return scenario.Check{Name: "c", Run: func(context.Context, scenario.Env) scenario.Result {
		*ran++
		return scenario.Result{Pass: pass}
	}}
}

func TestStopsAtFirstFailure(t *testing.T) {
	ran := 0
	r := Run(context.Background(), []scenario.Check{check(true, &ran), check(false, &ran), check(true, &ran)}, nil)
	if r.Passed || ran != 2 || len(r.Items) != 2 {
		t.Fatalf("%+v ran=%d", r, ran)
	}
}

func TestAllPass(t *testing.T) {
	ran := 0
	if r := Run(context.Background(), []scenario.Check{check(true, &ran)}, nil); !r.Passed {
		t.Fatalf("%+v", r)
	}
}

// Сценарий без проверок не должен засчитываться как починенный.
func TestNoChecksIsNotPass(t *testing.T) {
	if Run(context.Background(), nil, nil).Passed {
		t.Fatal("пустой список проверок засчитан")
	}
}

func TestPanicInCheckIsFailure(t *testing.T) {
	c := scenario.Check{Name: "c", Run: func(context.Context, scenario.Env) scenario.Result { panic("сбой") }}
	if r := Run(context.Background(), []scenario.Check{c}, nil); r.Passed || len(r.Items) != 1 {
		t.Fatalf("%+v", r)
	}
}
