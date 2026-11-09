package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPA(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":       {Data: []byte("<html>app</html>")},
		"assets/app-1.js":  {Data: []byte("js")},
		"favicon.ico":      {Data: []byte("ico")},
		"assets/style.css": {Data: []byte("css")},
	})
	cases := []struct {
		path, body, cache string
		status            int
	}{
		{"/", "<html>app</html>", "no-cache", 200},
		{"/rules/sign-in", "<html>app</html>", "no-cache", 200}, // client-side route
		{"/assets/app-1.js", "js", "public, max-age=31536000, immutable", 200},
		{"/favicon.ico", "ico", "", 200},
		{"/missing.js", "", "", 404},
		{"/../../etc/passwd", "", "", 400}, // rejected before touching the FS
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app"+c.path, nil))
		if rec.Code != c.status || (c.body != "" && rec.Body.String() != c.body) || rec.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s: got %d %q cache %q", c.path, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
}
