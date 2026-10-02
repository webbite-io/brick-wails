// Package fakestorage is an in-memory Storage API covering everything brick's
// sync engine and remote agent call: resolve, children (paginated), folder
// create (409 on duplicates), soft-delete (cascading), file
// download/upload/replace with ETags, check-updates with a server clock and
// cursor pagination, quota, and client deregistration. Test helpers
// manipulate the tree "server-side" (as another device or the webapp would).
package fakestorage

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Node mirrors the Storage API JSON node.
type Node struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parentId"`
	Name      string    `json:"name"`
	NodeType  string    `json:"nodeType"`
	SizeBytes int64     `json:"sizeBytes"`
	Etag      string    `json:"etag"`
	IsDeleted bool      `json:"isDeleted"`
	UpdatedAt time.Time `json:"updatedAt"`
	// ContentMD5 mirrors the real API: every write records the content's MD5
	// on the node, and it comes back with the ordinary children listing. Use
	// ClearMD5 to model a file uploaded before the server stored one.
	ContentMD5 string `json:"contentMd5,omitempty"`
	// Path is the node's full resolved path ("/docs/a.txt"), computed per
	// response rather than stored — brick-api does the same (every handler
	// calls node.ToResponse(path) with a path built from the ancestor chain).
	// It is what lets a sync client patch a cached tree from a check-updates
	// row instead of re-walking every folder to find out where the changed
	// node now lives. Set on serialization by withPathLocked; the stored node
	// never carries one, so a move only has to update ParentID/Name.
	Path string `json:"path,omitempty"`
}

type node struct {
	Node
	data      []byte
	changedAt int64
}

// Server is the fake. Use New, then point a storage.Client at URL.
type Server struct {
	*httptest.Server
	AccountID string

	// Authorize, if set, gates every request on the bearer token.
	Authorize func(token string) bool
	// OnDownload, if set, runs before each file download is served.
	OnDownload func(id string)
	// PageLimit, if > 0, caps the check-updates page size below whatever the
	// client asked for — the knob for forcing a multi-page feed from a handful
	// of changed nodes, e.g. to drive a client past its own page budget.
	PageLimit int

	mu       sync.Mutex
	nodes    map[string]*node
	nextID   int
	clock    int64
	quota    int64
	failNext map[string]int // "METHOD /suffix" prefix → remaining failures

	requests map[string]int
}

// New starts a fake for accountID with an empty root ("root").
func New(accountID string) *Server {
	s := newServer(accountID)
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// NewOn starts a fake on ln (e.g. a fixed port for manual testing).
func NewOn(ln net.Listener, accountID string) *Server {
	s := newServer(accountID)
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.Server.Listener.Close()
	s.Server.Listener = ln
	s.Server.Start()
	return s
}

func newServer(accountID string) *Server {
	s := &Server{
		AccountID: accountID,
		nodes:     map[string]*node{},
		clock:     1000,
		quota:     10 << 30,
		failNext:  map[string]int{},
		requests:  map[string]int{},
	}
	s.nodes["root"] = &node{Node: Node{ID: "root", NodeType: "root"}}
	return s
}

func (s *Server) tick() int64 { s.clock++; return s.clock }

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func (s *Server) newID() string { s.nextID++; return fmt.Sprintf("n%d", s.nextID) }

func (s *Server) newEtag() string { s.nextID++; return fmt.Sprintf("etag-%d", s.nextID) }

// --- server-side helpers (must not hold mu) ---

func (s *Server) childLocked(parentID, name string) *node {
	for _, n := range s.nodes {
		if n.ParentID == parentID && n.Name == name && !n.IsDeleted {
			return n
		}
	}
	return nil
}

func (s *Server) lookupLocked(path string) *node {
	cur := s.nodes["root"]
	for _, seg := range split(path) {
		cur = s.childLocked(cur.ID, seg)
		if cur == nil {
			return nil
		}
	}
	return cur
}

func split(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
}

// pathOfLocked returns n's full resolved path, leading slash included ("/" for
// the root). Trashed nodes keep their parent chain on the real server, so this
// resolves them too — a trashed node's check-updates row carries the path it
// was trashed from, which is how a client knows what to remove locally.
func (s *Server) pathOfLocked(n *node) string {
	var segs []string
	for cur := n; cur != nil && cur.ID != "root"; cur = s.nodes[cur.ParentID] {
		segs = append(segs, cur.Name)
	}
	var b strings.Builder
	for i := len(segs) - 1; i >= 0; i-- {
		b.WriteString("/")
		b.WriteString(segs[i])
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// withPathLocked is the node as the API would serialize it: the stored fields
// plus the resolved Path. Every handler goes through this, so no response ever
// carries an empty path the way a hand-built Node literal would.
func (s *Server) withPathLocked(n *node) Node {
	out := n.Node
	out.Path = s.pathOfLocked(n)
	return out
}

func (s *Server) mkdirAllLocked(path string) *node {
	cur := s.nodes["root"]
	for _, seg := range split(path) {
		next := s.childLocked(cur.ID, seg)
		if next == nil {
			next = &node{Node: Node{ID: s.newID(), ParentID: cur.ID, Name: seg, NodeType: "folder", UpdatedAt: time.Now()}, changedAt: s.tick()}
			s.nodes[next.ID] = next
		}
		cur = next
	}
	return cur
}

// PutFile creates or replaces a file at a slash path (creating parents) and
// returns its node ID.
func (s *Server) PutFile(path string, content string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := split(path)
	parent := s.mkdirAllLocked(strings.Join(parts[:len(parts)-1], "/"))
	n := s.childLocked(parent.ID, parts[len(parts)-1])
	if n == nil {
		n = &node{Node: Node{ID: s.newID(), ParentID: parent.ID, Name: parts[len(parts)-1], NodeType: "file"}}
		s.nodes[n.ID] = n
	}
	n.data = []byte(content)
	n.SizeBytes = int64(len(content))
	n.ContentMD5 = md5Hex(n.data)
	n.Etag = s.newEtag()
	n.UpdatedAt = time.Now()
	n.changedAt = s.tick()
	return n.ID
}

// ClearMD5 drops the stored content-MD5 of the file at path, modelling one
// uploaded before the server recorded hashes (or whose resumable chunks
// arrived out of order) — the case sync must treat as unverifiable rather
// than as proof the content differs.
func (s *Server) ClearMD5(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.lookupLocked(path); n != nil {
		n.ContentMD5 = ""
	}
}

// Mkdir creates a folder (and parents) and returns its ID.
func (s *Server) Mkdir(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mkdirAllLocked(path).ID
}

// Move renames/moves the node at from to to (same node ID, like the real API).
func (s *Server) Move(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.lookupLocked(from)
	if n == nil {
		panic("fakestorage.Move: no node at " + from)
	}
	parts := split(to)
	parent := s.mkdirAllLocked(strings.Join(parts[:len(parts)-1], "/"))
	n.ParentID = parent.ID
	n.Name = parts[len(parts)-1]
	n.changedAt = s.tick()
}

// Trash soft-deletes the node at path (and its subtree), as the webapp would.
func (s *Server) Trash(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.lookupLocked(path); n != nil {
		s.softDeleteLocked(n.ID)
	}
}

// Purge hard-deletes path without recording a change — the case the
// incremental feed can't report and the periodic full reconcile must catch.
func (s *Server) Purge(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.lookupLocked(path)
	if n == nil {
		return
	}
	var drop func(id string)
	drop = func(id string) {
		for cid, c := range s.nodes {
			if c.ParentID == id {
				drop(cid)
			}
		}
		delete(s.nodes, id)
	}
	drop(n.ID)
}

func (s *Server) softDeleteLocked(id string) {
	t := s.tick()
	var mark func(id string)
	mark = func(id string) {
		n := s.nodes[id]
		n.IsDeleted = true
		n.changedAt = t
		for cid, c := range s.nodes {
			if c.ParentID == id && !c.IsDeleted {
				mark(cid)
			}
		}
	}
	mark(id)
}

// Read returns the content of the live file at path.
func (s *Server) Read(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.lookupLocked(path)
	if n == nil || n.NodeType != "file" {
		return "", false
	}
	return string(n.data), true
}

// Exists reports whether a live node exists at path.
func (s *Server) Exists(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookupLocked(path) != nil
}

// ID returns the node ID at path ("" if none).
func (s *Server) ID(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.lookupLocked(path); n != nil {
		return n.ID
	}
	return ""
}

// Paths lists every live node as "path" (folders suffixed with "/"), sorted.
func (s *Server) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	var walk func(id, prefix string)
	walk = func(id, prefix string) {
		for _, n := range s.nodes {
			if n.ParentID != id || n.IsDeleted || n.ID == "root" {
				continue
			}
			p := prefix + n.Name
			if n.NodeType == "folder" {
				out = append(out, p+"/")
				walk(n.ID, p+"/")
			} else {
				out = append(out, p)
			}
		}
	}
	walk("root", "")
	sort.Strings(out)
	return out
}

// FailNext makes the next n requests whose "METHOD path-suffix" starts with
// key fail with 500 (e.g. "GET /files/", "POST /files").
func (s *Server) FailNext(key string, n int) {
	s.mu.Lock()
	s.failNext[key] = n
	s.mu.Unlock()
}

// Requests returns how many requests matched "METHOD path-suffix-prefix".
func (s *Server) Requests(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for k, v := range s.requests {
		if strings.HasPrefix(k, prefix) {
			total += v
		}
	}
	return total
}

// ResetRequests clears the request counters.
func (s *Server) ResetRequests() {
	s.mu.Lock()
	s.requests = map[string]int{}
	s.mu.Unlock()
}

// --- HTTP ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.Authorize != nil && !s.Authorize(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	base := "/v1/accounts/" + s.AccountID
	if !strings.HasPrefix(r.URL.Path, base) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, base)
	key := r.Method + " " + rest

	s.mu.Lock()
	s.requests[key]++
	for k, n := range s.failNext {
		if n > 0 && strings.HasPrefix(key, k) {
			s.failNext[k] = n - 1
			s.mu.Unlock()
			http.Error(w, "injected failure", http.StatusInternalServerError)
			return
		}
	}
	s.mu.Unlock()

	switch {
	case r.Method == "GET" && rest == "/resolve":
		s.mu.Lock()
		n := s.lookupLocked(r.URL.Query().Get("path"))
		var out Node
		if n != nil {
			out = s.withPathLocked(n)
		}
		s.mu.Unlock()
		if n == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, out)

	case r.Method == "GET" && rest == "/quota":
		s.mu.Lock()
		var used int64
		for _, n := range s.nodes {
			if !n.IsDeleted {
				used += n.SizeBytes
			}
		}
		q := s.quota
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"quotaBytes": q, "usedBytes": used, "remainingBytes": q - used, "callingUser": map[string]any{"usedBytes": used}})

	case r.Method == "GET" && rest == "/check-updates":
		s.checkUpdates(w, r)

	case r.Method == "GET" && strings.HasPrefix(rest, "/nodes/") && strings.HasSuffix(rest, "/children"):
		id := strings.TrimSuffix(strings.TrimPrefix(rest, "/nodes/"), "/children")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit <= 0 {
			limit = 200
		}
		s.mu.Lock()
		var kids []Node
		for _, n := range s.nodes {
			if n.ParentID == id && !n.IsDeleted && n.ID != "root" {
				kids = append(kids, s.withPathLocked(n))
			}
		}
		s.mu.Unlock()
		sort.Slice(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
		total := len(kids)
		if offset > len(kids) {
			offset = len(kids)
		}
		kids = kids[offset:]
		if len(kids) > limit {
			kids = kids[:limit]
		}
		if kids == nil {
			kids = []Node{}
		}
		writeJSON(w, 200, map[string]any{"data": kids, "count": total})

	case r.Method == "POST" && rest == "/nodes":
		var body struct{ ParentID, Name string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		s.mu.Lock()
		if s.childLocked(body.ParentID, body.Name) != nil {
			s.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "conflict"})
			return
		}
		n := &node{Node: Node{ID: s.newID(), ParentID: body.ParentID, Name: body.Name, NodeType: "folder", UpdatedAt: time.Now()}, changedAt: s.tick()}
		s.nodes[n.ID] = n
		out := s.withPathLocked(n)
		s.mu.Unlock()
		writeJSON(w, 201, out)

	case r.Method == "DELETE" && strings.HasPrefix(rest, "/nodes/"):
		id := strings.TrimPrefix(rest, "/nodes/")
		s.mu.Lock()
		n, ok := s.nodes[id]
		if !ok || n.IsDeleted {
			s.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		s.softDeleteLocked(id)
		s.mu.Unlock()
		w.WriteHeader(204)

	case r.Method == "POST" && rest == "/files":
		data, _ := io.ReadAll(r.Body)
		parentID, name := r.Header.Get("X-Parent-ID"), r.Header.Get("X-Filename")
		s.mu.Lock()
		if _, ok := s.nodes[parentID]; !ok {
			s.mu.Unlock()
			http.Error(w, "parent not found", 404)
			return
		}
		n := &node{Node: Node{ID: s.newID(), ParentID: parentID, Name: name, NodeType: "file", SizeBytes: int64(len(data)), ContentMD5: md5Hex(data), Etag: s.newEtag(), UpdatedAt: time.Now()}, data: data, changedAt: s.tick()}
		s.nodes[n.ID] = n
		out := s.withPathLocked(n)
		s.mu.Unlock()
		writeJSON(w, 201, map[string]any{"node": out})

	case strings.HasPrefix(rest, "/files/"):
		id := strings.TrimPrefix(rest, "/files/")
		if r.Method == "GET" && s.OnDownload != nil {
			s.OnDownload(id)
		}
		s.mu.Lock()
		n, ok := s.nodes[id]
		if !ok || n.IsDeleted || n.NodeType != "file" {
			s.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "GET":
			data, etag := n.data, n.Etag
			s.mu.Unlock()
			w.Header().Set("ETag", `"`+etag+`"`)
			w.WriteHeader(200)
			_, _ = w.Write(data)
		case "PUT":
			s.mu.Unlock()
			data, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			n.data = data
			n.SizeBytes = int64(len(data))
			n.ContentMD5 = md5Hex(data)
			n.Etag = s.newEtag()
			n.changedAt = s.tick()
			out := s.withPathLocked(n)
			s.mu.Unlock()
			writeJSON(w, 200, map[string]any{"node": out})
		default:
			s.mu.Unlock()
			w.WriteHeader(405)
		}

	case r.Method == "DELETE" && strings.HasPrefix(rest, "/clients/"):
		w.WriteHeader(204)

	default:
		http.NotFound(w, r)
	}
}

// checkUpdates serves nodes changed at or after ?since, ordered by
// (changedAt, ID) like brick-api's keyset pagination, each row carrying its
// resolved path. serverTime is clock+1, so a returned change is never reported
// again once its serverTime is adopted as the cursor.
//
// Pages hold ?limit rows (further capped by PageLimit, which tests set to
// force pagination). Soft-deleted rows are included — their changedAt is
// bumped on deletion, cascading to descendants, exactly as the real server's
// SoftDeleteNode does — so a client can propagate a trashing. Hard deletes
// (Purge) leave no row and so are invisible here, which is what the periodic
// full walk exists to catch.
func (s *Server) checkUpdates(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}

	s.mu.Lock()
	var changed []*node
	for _, n := range s.nodes {
		if n.changedAt >= since && n.ID != "root" {
			changed = append(changed, n)
		}
	}
	sort.Slice(changed, func(i, j int) bool {
		if changed[i].changedAt != changed[j].changedAt {
			return changed[i].changedAt < changed[j].changedAt
		}
		return changed[i].ID < changed[j].ID
	})
	if s.PageLimit > 0 && limit > s.PageLimit {
		limit = s.PageLimit
	}
	if offset > len(changed) {
		offset = len(changed)
	}
	rows := changed[offset:]
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.Itoa(offset + limit)
	}
	page := make([]Node, 0, len(rows))
	for _, n := range rows {
		page = append(page, s.withPathLocked(n))
	}
	serverTime := s.clock + 1
	s.mu.Unlock()

	writeJSON(w, 200, map[string]any{"data": page, "count": len(page), "serverTime": serverTime, "nextCursor": next})
}
