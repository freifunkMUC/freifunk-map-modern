package httpclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUserAgentSent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	resp, err := New(time.Second, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != UserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, UserAgent)
	}
}
