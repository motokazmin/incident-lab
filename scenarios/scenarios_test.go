package scenarios

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"incident-lab/platform/scenario"
)

// Контракт сюжета, проверяется без виртуалок. Пустая нагрузка уронила бы сервер со
// всеми стендами, а сюжет без ложных починок не доказывает, что проверку нельзя обойти.
func TestContract(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range All() {
		t.Run(sc.ID, func(t *testing.T) {
			if sc.ID == "" || seen[sc.ID] {
				t.Fatalf("ID пуст или повторяется: %q", sc.ID)
			}
			seen[sc.ID] = true
			checkFiles(t, sc.ID)
			checkTexts(t, sc)
			checkLoad(t, sc.Load)
			checkAlert(t, sc.Alert)
			checkChecks(t, sc)
			checkRepairs(t, sc)
		})
	}
}

func checkFiles(t *testing.T, id string) {
	t.Helper()
	for _, f := range []string{"image/Dockerfile", "image/boot", "image/service.env", "service/go.mod", "service/vendor"} {
		if _, err := os.Stat(filepath.Join(id, f)); err != nil {
			t.Errorf("нет файла: %v", err)
		}
	}
}

func checkTexts(t *testing.T, sc scenario.Scenario) {
	t.Helper()
	for name, v := range map[string]string{"Title": sc.Title, "Summary": sc.Summary, "Stack": sc.Stack, "Briefing": sc.Briefing, "Debrief": sc.Debrief} {
		if strings.TrimSpace(v) == "" {
			t.Errorf("пусто поле %s", name)
		}
	}
	if len(sc.FixHow) == 0 {
		t.Error("нет FixHow")
	}
	if len(sc.Hints) == 0 {
		t.Error("нет подсказок")
	}
	for i, h := range sc.Hints {
		if h.Title == "" || h.Text == "" {
			t.Errorf("подсказка %d пуста", i+1)
		}
	}
}

func checkLoad(t *testing.T, l scenario.LoadProfile) {
	t.Helper()
	if l.RatePerSec <= 0 || len(l.Requests) == 0 {
		t.Fatalf("нагрузка пуста: %+v", l)
	}
	for i, r := range l.Requests {
		if r.Weight <= 0 || r.Method == "" || !strings.HasPrefix(r.Path, "/") {
			t.Errorf("запрос %d: %+v", i+1, r)
		}
	}
}

func checkAlert(t *testing.T, a scenario.Alert) {
	t.Helper()
	if a.MinErrorRate <= 0 && a.MaxP95 <= 0 {
		t.Error("у алерта нет порога: инцидент никогда не начнётся")
	}
	if a.Text == "" {
		t.Error("у алерта нет текста")
	}
}

func checkChecks(t *testing.T, sc scenario.Scenario) {
	t.Helper()
	if len(sc.Checks) == 0 {
		t.Error("нет проверок")
	}
	for i, c := range sc.Checks {
		if c.Name == "" || c.Run == nil {
			t.Errorf("проверка %d неполна", i+1)
		}
	}
}

func checkRepairs(t *testing.T, sc scenario.Scenario) {
	t.Helper()
	good, bad, restart := 0, 0, false
	for _, r := range sc.Repairs {
		if len(r.Edits) == 0 && r.Shell == "" {
			t.Errorf("починка %q ничего не делает", r.Name)
		}
		checkEdits(t, sc.ID, r)
		if r.Pass {
			good++
			if r.FailAt != "" || r.Hint != "" {
				t.Errorf("верная починка %q описывает провал", r.Name)
			}
			continue
		}
		bad++
		restart = restart || len(r.Edits) == 0 && strings.Contains(r.Shell, "svc restart")
	}
	if good < 1 || bad < 3 || !restart {
		t.Errorf("нужна верная починка и три ложных, среди них рестарт: верных %d, ложных %d, рестарт %v", good, bad, restart)
	}
}

// checkEdits прикладывает правки к файлам сервиса: устаревшая правка видна сразу, а не через час e2e.
func checkEdits(t *testing.T, id string, r scenario.Repair) {
	t.Helper()
	files := map[string]string{}
	for _, e := range r.Edits {
		src, ok := files[e.File]
		if !ok {
			b, err := os.ReadFile(filepath.Join(id, "service", e.File))
			if err != nil {
				t.Errorf("починка %q: %v", r.Name, err)
				continue
			}
			src = string(b)
		}
		if !strings.Contains(src, e.From) {
			t.Errorf("починка %q: в %s нет %q", r.Name, e.File, e.From)
		}
		files[e.File] = strings.Replace(src, e.From, e.To, 1)
	}
}
