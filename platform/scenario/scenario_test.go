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
