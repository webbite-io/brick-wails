package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/webbite-io/brick-wails/internal/storage"
)

// --- fake Storage API ---

// fakeNode is a fakeStorageAPI-internal node: the JSON-facing storage.Node
// plus the raw bytes for a file.
type fakeNode struct {
	storage.Node
	data []byte
}

// fakeStorageAPI is a minimal in-memory stand-in for the real Storage API,
// covering just enough of the surface the storage.Client
// methods the agent uses talk to: resolve, nodes (get/create/list-children), and files
// (create/replace/download). Good enough to exercise the agent's transfer
// logic end-to-end without a live server.
type fakeStorageAPI struct {
	mu     sync.Mutex
	nodes  map[string]*fakeNode
	nextID int
	// failNextDownload makes that many subsequent file downloads fail with a
	// 500, for exercising a transfer that fails and has to be retried.
	failNextDownload int
}

func newFakeStorageAPI() *fakeStorageAPI {
	fs := &fakeStorageAPI{nodes: map[string]*fakeNode{}}
	fs.nodes["root"] = &fakeNode{Node: storage.Node{ID: "root", NodeType: "root"}}
	return fs
}

func (fs *fakeStorageAPI) newID() string {
	fs.nextID++
	return fmt.Sprintf("n%d", fs.nextID)
}

// children must be called with fs.mu held.
func (fs *fakeStorageAPI) children(parentID string) []storage.Node {
	var out []storage.Node
	for _, n := range fs.nodes {
		if n.ParentID == parentID {
			out = append(out, n.Node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolvePath must be called with fs.mu held.
func (fs *fakeStorageAPI) resolvePath(path string) (*storage.Node, bool) {
	cur := fs.nodes["root"]
	for _, seg := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' }) {
		found := (*fakeNode)(nil)
		for _, n := range fs.nodes {
			if n.ParentID == cur.ID && n.Name == seg {
				found = n
				break
			}
		}
		if found == nil {
			return nil, false
		}
		cur = found
	}
	node := cur.Node
	return &node, true
}

func (fs *fakeStorageAPI) mux(accountID string) http.Handler {
	base := "/v1/accounts/" + accountID
	mux := http.NewServeMux()

	mux.HandleFunc(base+"/resolve", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		node, ok := fs.resolvePath(r.URL.Query().Get("path"))
		fs.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, node)
	})

	mux.HandleFunc(base+"/nodes", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ParentID string `json:"parentId"`
			Name     string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		for _, n := range fs.nodes {
			if n.ParentID == body.ParentID && n.Name == body.Name && n.NodeType == "folder" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
				return
			}
		}
		id := fs.newID()
		node := storage.Node{ID: id, ParentID: body.ParentID, Name: body.Name, NodeType: "folder"}
		fs.nodes[id] = &fakeNode{Node: node}
		writeJSON(w, http.StatusCreated, node)
	})

	mux.HandleFunc(base+"/nodes/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, base+"/nodes/")
		if strings.HasSuffix(rest, "/children") {
			id := strings.TrimSuffix(rest, "/children")
			fs.mu.Lock()
			kids := fs.children(id)
			fs.mu.Unlock()
			writeJSON(w, http.StatusOK, storage.NodeList{Data: kids, Count: int64(len(kids))})
			return
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		n, ok := fs.nodes[rest]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		if r.Method == http.MethodDelete {
			n.IsDeleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, n.Node)
	})

	mux.HandleFunc(base+"/files", func(w http.ResponseWriter, r *http.Request) {
		parentID := r.Header.Get("X-Parent-ID")
		name := r.Header.Get("X-Filename")
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		fs.mu.Lock()
		id := fs.newID()
		node := storage.Node{ID: id, ParentID: parentID, Name: name, NodeType: "file", SizeBytes: int64(len(data))}
		fs.nodes[id] = &fakeNode{Node: node, data: data}
		fs.mu.Unlock()
		writeJSON(w, http.StatusCreated, storage.UploadResult{Node: node})
	})

	mux.HandleFunc(base+"/files/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, base+"/files/")
		fs.mu.Lock()
		defer fs.mu.Unlock()
		n, ok := fs.nodes[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			if fs.failNextDownload > 0 {
				fs.failNextDownload--
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "injected failure"})
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(n.data)
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			n.data = data
			n.SizeBytes = int64(len(data))
			writeJSON(w, http.StatusOK, storage.UploadResult{Node: n.Node})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	return mux
}

func newTestTransferClient(t *testing.T) (*storage.Client, *fakeStorageAPI) {
	t.Helper()
	fs := newFakeStorageAPI()
	server := httptest.NewServer(fs.mux("acct-1"))
	t.Cleanup(server.Close)
	return testStorageClient(t, server.URL), fs
}
