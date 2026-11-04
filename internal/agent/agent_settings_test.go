package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
)

func TestAgentReadOnlyRootRejectsWrites(t *testing.T) {
	sc, fake := newTestTransferClient(t)
	ro := t.TempDir()
	rw := filepath.Join(ro, "inbox") // a writable root nested inside the read-only one
	if err := os.MkdirAll(rw, 0o755); err != nil {
		t.Fatal(err)
	}
	ta := newTestAgentWithRoots(t, sc, fake, true, []agentRoot{{path: ro, readOnly: true}, {path: rw}})

	file := filepath.Join(ro, "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	addFakeFile(fake, "f1", "remote.txt", []byte("remote"))

	rejected := []struct {
		name, method, path string
		body               interface{}
	}{
		{"write", "PUT", "/fs/write?path=" + filepath.Join(ro, "new.txt"), nil},
		{"mkdir", "POST", "/fs/mkdir?path=" + filepath.Join(ro, "dir"), nil},
		{"delete", "DELETE", "/fs/delete?path=" + file, nil},
		{"move out of read-only", "POST", "/fs/move", fromToReq{From: file, To: filepath.Join(rw, "notes.txt")}},
		{"move into read-only", "POST", "/fs/move", fromToReq{From: filepath.Join(rw, "x"), To: filepath.Join(ro, "x")}},
		{"copy into read-only", "POST", "/fs/copy", fromToReq{From: file, To: filepath.Join(ro, "copy.txt")}},
		{"download into read-only", "POST", "/transfer/download", downloadReq{NodeID: "f1", LocalPath: filepath.Join(ro, "remote.txt")}},
	}
	for _, tc := range rejected {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if status := ta.do(tc.method, tc.path, tc.body, &errResp); status != http.StatusForbidden || errResp.Error.Code != "read_only_root" {
			t.Errorf("%s: status %d code %q, want 403 read_only_root", tc.name, status, errResp.Error.Code)
		}
	}
	if got, _ := os.ReadFile(file); string(got) != "hello" {
		t.Errorf("read-only file changed to %q", got)
	}

	// Reading from a read-only root still works, including copying or
	// uploading out of it.
	allowed := []struct {
		name, method, path string
		body               interface{}
	}{
		{"list", "GET", "/fs/list?path=" + ro, nil},
		{"stat", "GET", "/fs/stat?path=" + file, nil},
		{"copy out of read-only", "POST", "/fs/copy", fromToReq{From: file, To: filepath.Join(rw, "copy.txt")}},
		{"upload from read-only", "POST", "/transfer/upload", uploadReq{LocalPath: file, ParentID: "root"}},
		{"download into nested writable root", "POST", "/transfer/download", downloadReq{NodeID: "f1", LocalPath: filepath.Join(rw, "remote.txt")}},
	}
	for _, tc := range allowed {
		if status := ta.do(tc.method, tc.path, tc.body, nil); status != http.StatusOK {
			t.Errorf("%s: status %d, want 200", tc.name, status)
		}
	}

	var roots struct {
		Roots []rootEntry `json:"roots"`
	}
	ta.do("GET", "/fs/roots", nil, &roots)
	if len(roots.Roots) != 2 || !roots.Roots[0].ReadOnly || roots.Roots[1].ReadOnly {
		t.Errorf("GET /fs/roots = %+v, want the first root read-only and the second writable", roots.Roots)
	}
}

func TestResolveRootsReadOnlyWins(t *testing.T) {
	no := false
	roots := ResolveRoots([]brickcfg.AgentRoot{
		{Path: "/shared"}, // read-only
		{Path: "/shared", ReadOnly: &no},
		{Path: "/writable", ReadOnly: &no},
		{Path: "/writable/../writable/"},
	})
	want := []agentRoot{{path: "/shared", readOnly: true}, {path: "/writable", readOnly: true}}
	if !slices.Equal(roots, want) {
		t.Errorf("ResolveRoots = %+v, want %+v", roots, want)
	}
}

// stubExcludeDirs is an in-memory ExcludeDirsSettings.
type stubExcludeDirs struct{ dirs []string }

func (s *stubExcludeDirs) ExcludeDirs() []string { return s.dirs }
func (s *stubExcludeDirs) SetExcludeDirs(dirs []string) error {
	s.dirs = dirs
	return nil
}

func TestAgentSettingsEndpoints(t *testing.T) {
	sc, fake := newTestTransferClient(t)
	ta := newTestAgent(t, sc, fake, true)

	var errResp struct {
		Error apiError `json:"error"`
	}
	if status := ta.do("GET", "/settings", nil, &errResp); status != http.StatusServiceUnavailable {
		t.Errorf("GET with no sync running: status %d, want 503", status)
	}

	stub := &stubExcludeDirs{}
	ta.server.settings = stub

	var got settingsResponse
	if status := ta.do("GET", "/settings", nil, &got); status != http.StatusOK || got.SelectiveSync.ExcludeDirs == nil || len(got.SelectiveSync.ExcludeDirs) != 0 {
		t.Errorf("GET with nothing excluded: status %d, excludeDirs %#v, want 200 and []", status, got.SelectiveSync.ExcludeDirs)
	}
	if len(got.Roots) != 1 || got.Roots[0].Path != ta.root {
		t.Errorf("GET roots = %+v, want just %s", got.Roots, ta.root)
	}

	body := map[string]any{"selectiveSync": map[string][]string{"excludeDirs": {"/Photos/", "Work//Archive/", "Photos", " Music "}}}
	if status := ta.do("PATCH", "/settings", body, &got); status != http.StatusOK {
		t.Fatalf("PATCH: status %d, want 200", status)
	}
	want := []string{"Photos", "Work/Archive", "Music"}
	if !slices.Equal(got.SelectiveSync.ExcludeDirs, want) || !slices.Equal(stub.dirs, want) {
		t.Errorf("PATCH stored %v and answered %v, want both %v", stub.dirs, got.SelectiveSync.ExcludeDirs, want)
	}
	if len(got.Roots) != 1 {
		t.Errorf("PATCH answered roots %+v, want the one root", got.Roots)
	}

	for _, bad := range []any{
		map[string]any{"selectiveSync": map[string][]string{"excludeDirs": {"../etc"}}},
		map[string]any{"selectiveSync": map[string][]string{"excludeDirs": {"Photos/../.."}}},
		map[string]any{"selectiveSync": map[string][]string{"excludeDirs": {"/"}}},
		map[string]any{"selectiveSync": map[string]any{}},
		map[string]any{"selectiveSync": map[string]any{"excludeDirs": []string{}, "other": 1}},
		map[string][]string{"excludeDirs": {"Photos"}},
		map[string]string{"something": "else"},
	} {
		if status := ta.do("PATCH", "/settings", bad, nil); status != http.StatusBadRequest {
			t.Errorf("PATCH %v: status %d, want 400", bad, status)
		}
	}

	// Roots, and whether they're read-only, can't be changed remotely, even
	// alongside a valid selectiveSync change.
	readOnly := map[string]any{
		"selectiveSync": map[string][]string{"excludeDirs": {}},
		"roots":         []map[string]any{{"path": ta.root, "readOnly": true}},
	}
	if status := ta.do("PATCH", "/settings", readOnly, &errResp); status != http.StatusForbidden || errResp.Error.Code != "setting_read_only" {
		t.Errorf("PATCH roots: status %d code %q, want 403 setting_read_only", status, errResp.Error.Code)
	}
	if !slices.Equal(stub.dirs, want) {
		t.Errorf("a rejected PATCH changed the list to %v", stub.dirs)
	}
	if ta.server.roots[0].readOnly {
		t.Error("a rejected PATCH made the root read-only")
	}
}

func TestAgentSettingsReadOnlyDeviceRefusesChanges(t *testing.T) {
	sc, fake := newTestTransferClient(t)
	ta := newTestAgentWithRoots(t, sc, fake, true, []agentRoot{{path: t.TempDir(), readOnly: true}})
	stub := &stubExcludeDirs{dirs: []string{"Photos"}}
	ta.server.settings = stub

	if status := ta.do("GET", "/settings", nil, nil); status != http.StatusOK {
		t.Errorf("GET: status %d, want 200", status)
	}
	var errResp struct {
		Error apiError `json:"error"`
	}
	body := map[string]any{"selectiveSync": map[string][]string{"excludeDirs": {}}}
	if status := ta.do("PATCH", "/settings", body, &errResp); status != http.StatusForbidden || errResp.Error.Code != "read_only_device" {
		t.Errorf("PATCH: status %d code %q, want 403 read_only_device", status, errResp.Error.Code)
	}
	if !slices.Equal(stub.dirs, []string{"Photos"}) {
		t.Errorf("a refused PATCH changed the list to %v", stub.dirs)
	}
}
