package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func withFakeGitHub(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	prev := releaseURL
	releaseURL = srv.URL
	t.Cleanup(func() { releaseURL = prev })
}

func TestCheckNewerVersionAvailable(t *testing.T) {
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name": "v1.2.0"}`)
	})

	info, err := Check(context.Background(), "1.1.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info == nil {
		t.Fatal("expected an update to be reported, got nil")
	}
	if info.Current != "1.1.0" || info.Latest != "1.2.0" {
		t.Fatalf("got %+v", info)
	}
}

func TestCheckAlreadyUpToDate(t *testing.T) {
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name": "v1.1.0"}`)
	})

	info, err := Check(context.Background(), "1.1.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info != nil {
		t.Fatalf("expected no update, got %+v", info)
	}
}

func TestCheckServerError(t *testing.T) {
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	info, err := Check(context.Background(), "1.1.0")
	if err == nil {
		t.Fatal("expected an error")
	}
	if info != nil {
		t.Fatalf("expected no update on error, got %+v", info)
	}
}

func TestCheckRespectsTimeout(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	start := time.Now()
	_, err := Check(context.Background(), "1.1.0")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Check took %s, expected it to give up around %s", elapsed, checkTimeout)
	}
}
