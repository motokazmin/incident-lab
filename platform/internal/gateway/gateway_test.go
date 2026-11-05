package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"incident-lab/platform/internal/lab"
)

func TestGuard(t *testing.T) {
	h := New(lab.NewManager(nil, nil, lab.Limits{}), t.TempDir())
	for _, c := range []struct {
		name, host, origin string
		want               int
	}{
		{"свой", "127.0.0.1:8090", "", http.StatusOK},
		{"свой origin", "localhost:8090", "http://localhost:8090", http.StatusOK},
		{"dns rebinding", "evil.example:8090", "", http.StatusForbidden},
		{"чужая страница", "127.0.0.1:8090", "https://evil.example", http.StatusForbidden},
		{"порт в origin другой", "127.0.0.1:8090", "http://127.0.0.1:9999", http.StatusForbidden},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/scenarios", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, ждали %d", c.name, w.Code, c.want)
		}
	}
}

// Простая форма с чужого сайта не содержит JSON и не должна создавать стенд.
func TestStartRequiresJSON(t *testing.T) {
	h := New(lab.NewManager(nil, nil, lab.Limits{}), t.TempDir())
	r := httptest.NewRequest(http.MethodPost, "/api/labs", strings.NewReader("scenario=x"))
	r.Host = "127.0.0.1:8090"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatal(w.Code)
	}
}

func TestLongFilePathRejected(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/?path="+strings.Repeat("a", maxPath+1), nil)
	if _, ok := filePath(w, r); ok || w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
}
