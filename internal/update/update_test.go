package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withFakeGitHub(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	prev := releasesURL
	releasesURL = srv.URL
	t.Cleanup(func() { releasesURL = prev })
}

// release renders one entry of GitHub's release list, with the given assets.
func release(tag string, assets ...string) string {
	names := make([]string, len(assets))
	for i, a := range assets {
		names[i] = fmt.Sprintf(`{"name": %q}`, a)
	}
	return fmt.Sprintf(`{"tag_name": %q, "draft": false, "prerelease": false, "assets": [%s]}`,
		tag, strings.Join(names, ", "))
}

// releases renders GitHub's release list, newest first.
func releases(entries ...string) string {
	return "[" + strings.Join(entries, ", ") + "]"
}

func TestCheckNewerVersionAvailable(t *testing.T) {
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, releases(release("v1.2.0", tarballName("1.2.0"), "SHA256SUMS")))
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
		fmt.Fprint(w, releases(release("1.1.0", tarballName("1.1.0"))))
	})

	info, err := Check(context.Background(), "1.1.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info != nil {
		t.Fatalf("expected no update, got %+v", info)
	}
}

func TestCheckSkipsReleasesWithoutThisPlatform(t *testing.T) {
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, releases(
			release("1.3.0", "Webbite-Brick-1.3.0-macos-universal.dmg", "SHA256SUMS-macos"),
			release("1.2.0", tarballName("1.2.0")),
			release("1.1.0", tarballName("1.1.0")),
		))
	})

	info, err := Check(context.Background(), "1.1.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info == nil || info.Latest != "1.2.0" {
		t.Fatalf("expected 1.2.0, the newest release with this platform's tarball, got %+v", info)
	}
}

func TestCheckIgnoresAMacOnlyLatestRelease(t *testing.T) {
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, releases(
			release("1.2.0", "Webbite-Brick-1.2.0-macos-universal.dmg"),
			release("1.1.0", tarballName("1.1.0")),
		))
	})

	info, err := Check(context.Background(), "1.1.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if info != nil {
		t.Fatalf("expected no update, got %+v", info)
	}
}

func TestCheckSkipsDraftsAndPrereleases(t *testing.T) {
	withFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, releases(
			fmt.Sprintf(`{"tag_name": "1.3.0", "draft": true, "assets": [{"name": %q}]}`, tarballName("1.3.0")),
			fmt.Sprintf(`{"tag_name": "1.2.0-rc.1", "prerelease": true, "assets": [{"name": %q}]}`, tarballName("1.2.0-rc.1")),
			release("1.1.0", tarballName("1.1.0")),
		))
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
