package scenario

import (
	"reflect"
	"testing"
)

func TestParseHints(t *testing.T) {
	md := "## Первая\n\nТекст.\n\n```\n# не заголовок\n```\n\n## Вторая\n\nЕщё.\n"
	want := []Hint{{Title: "Первая", Text: "Текст.\n\n```\n# не заголовок\n```"}, {Title: "Вторая", Text: "Ещё."}}
	if got := ParseHints(md); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if got := ParseHints("текст без заголовков\n"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestIDs(t *testing.T) {
	if got := IDs(3, 5); !reflect.DeepEqual(got, []int{3, 4, 5}) {
		t.Fatalf("%v", got)
	}
	if got := IDs(7, 7); !reflect.DeepEqual(got, []int{7}) {
		t.Fatalf("%v", got)
	}
	if got := IDs(5, 3); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
