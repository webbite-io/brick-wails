// Package storage is a client for the Webbite Storage API, covering what sync
// and the remote agent need.
//
// Ported from brick-cli cmd/brick/sync.go (storageClient) @ f3ef7bd.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
)

// Node is a Storage API node (subset of the OpenAPI schema).
type Node struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parentId"`
	Name      string    `json:"name"`
	NodeType  string    `json:"nodeType"` // file, folder, root
	SizeBytes int64     `json:"sizeBytes"`
	Etag      string    `json:"etag"`
	Path      string    `json:"path"`
	IsDeleted bool      `json:"isDeleted"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NodeList is a paginated list response.
type NodeList struct {
	Data  []Node `json:"data"`
	Count int64  `json:"count"`
}

// UploadResult wraps the node returned by an upload/replace.
type UploadResult struct {
	Node Node `json:"node"`
}

// Quota is GET /v1/accounts/{id}/quota.
type Quota struct {
	QuotaBytes     int64     `json:"quotaBytes"`
	UsedBytes      int64     `json:"usedBytes"`
	RemainingBytes int64     `json:"remainingBytes"`
	CallingUser    QuotaUser `json:"callingUser"`
}

// QuotaUser is the calling user's share of the account usage.
type QuotaUser struct {
	UsedBytes      int64 `json:"usedBytes"`
	UsedVideoBytes int64 `json:"usedVideoBytes"`
	UsedImageBytes int64 `json:"usedImageBytes"`
	UsedOtherBytes int64 `json:"usedOtherBytes"`
}

// Client talks to the Storage API for one account.
type Client struct {
	BaseURL   string
	AccountID string
	Auth      *auth.Client
}

// Request performs an authenticated request against
// /v1/accounts/{accountId}{path}, refreshing once on 401/403.
func (c *Client) Request(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	full := strings.TrimRight(c.BaseURL, "/") + "/v1/accounts/" + c.AccountID + path
	return c.Auth.Do(ctx, method, full, body, headers, true)
}

// RawRequest is Request against an absolute path (e.g. /v1/accounts/{id}/clients/…).
func (c *Client) RawRequest(ctx context.Context, method, path string) (*http.Response, error) {
	return c.Auth.Do(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, nil, nil, true)
}

func errFrom(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("storage API %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.Request(ctx, "GET", path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errFrom(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ResolveRoot returns the account's root node.
func (c *Client) ResolveRoot(ctx context.Context) (*Node, error) {
	var n Node
	if err := c.getJSON(ctx, "/resolve", &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// Quota fetches the account's storage usage.
func (c *Client) Quota(ctx context.Context) (*Quota, error) {
	var q Quota
	if err := c.getJSON(ctx, "/quota", &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// ListChildren returns every child of parentID, following pagination.
func (c *Client) ListChildren(ctx context.Context, parentID string) ([]Node, error) {
	var all []Node
	const limit = 200
	for offset := 0; ; offset += limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var list NodeList
		if err := c.getJSON(ctx, fmt.Sprintf("/nodes/%s/children?limit=%d&offset=%d", parentID, limit, offset), &list); err != nil {
			return nil, err
		}
		all = append(all, list.Data...)
		if len(list.Data) < limit {
			return all, nil
		}
	}
}

// CheckUpdatesPageLimit is the page size for the incremental-sync feed.
const CheckUpdatesPageLimit = 500

// CheckUpdates reports whether anything visible changed at or after since, and
// the server clock to adopt as the next cursor. It stops at the first change,
// follows nextCursor past empty (server-filtered) pages, and always returns
// the first page's serverTime so a change landing mid-pagination is never
// skipped. See brick-cli's checkUpdates for the full rationale.
func (c *Client) CheckUpdates(ctx context.Context, since int64) (changed bool, serverTime int64, err error) {
	cursor := ""
	haveServerTime := false
	for {
		if err := ctx.Err(); err != nil {
			return false, 0, err
		}
		path := fmt.Sprintf("/check-updates?since=%d&limit=%d", since, CheckUpdatesPageLimit)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var out struct {
			Data       []Node `json:"data"`
			ServerTime int64  `json:"serverTime"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.getJSON(ctx, path, &out); err != nil {
			return false, 0, err
		}
		if !haveServerTime {
			serverTime, haveServerTime = out.ServerTime, true
		}
		if len(out.Data) > 0 {
			return true, serverTime, nil
		}
		if out.NextCursor == "" {
			return false, serverTime, nil
		}
		cursor = out.NextCursor
	}
}

// ServerNow returns the server clock via a far-future check-updates probe.
func (c *Client) ServerNow(ctx context.Context) (int64, error) {
	_, t, err := c.CheckUpdates(ctx, int64(1)<<62)
	return t, err
}

// Download returns a file's content and ETag.
func (c *Client) Download(ctx context.Context, nodeID string) ([]byte, string, error) {
	resp, err := c.Request(ctx, "GET", "/files/"+nodeID, nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", errFrom(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

func decodeUpload(resp *http.Response, want int) (*Node, error) {
	defer resp.Body.Close()
	if resp.StatusCode != want {
		return nil, errFrom(resp)
	}
	var r UploadResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r.Node, nil
}

// Upload creates a new file under parentID.
func (c *Client) Upload(ctx context.Context, parentID, name string, data []byte) (*Node, error) {
	resp, err := c.Request(ctx, "POST", "/files", data, map[string]string{
		"Content-Type": "application/octet-stream",
		"X-Parent-ID":  parentID,
		"X-Filename":   name,
	})
	if err != nil {
		return nil, err
	}
	return decodeUpload(resp, http.StatusCreated)
}

// Replace uploads new content for an existing file.
func (c *Client) Replace(ctx context.Context, nodeID string, data []byte) (*Node, error) {
	resp, err := c.Request(ctx, "PUT", "/files/"+nodeID, data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return nil, err
	}
	return decodeUpload(resp, http.StatusOK)
}

// Delete soft-deletes (trashes) a node; 404 counts as success.
func (c *Client) Delete(ctx context.Context, nodeID string) error {
	resp, err := c.Request(ctx, "DELETE", "/nodes/"+nodeID, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return errFrom(resp)
	}
	return nil
}

// CreateFolder creates a folder, reusing an existing one on 409.
func (c *Client) CreateFolder(ctx context.Context, parentID, name string) (*Node, error) {
	body, _ := json.Marshal(map[string]string{"parentId": parentID, "name": name})
	resp, err := c.Request(ctx, "POST", "/nodes", body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		children, err := c.ListChildren(ctx, parentID)
		if err != nil {
			return nil, err
		}
		for _, ch := range children {
			if ch.Name == name && ch.NodeType == "folder" && !ch.IsDeleted {
				n := ch
				return &n, nil
			}
		}
		return nil, fmt.Errorf("folder %q reported as conflict but not found", name)
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, errFrom(resp)
	}
	var n Node
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return nil, err
	}
	return &n, nil
}

// FolderSummary walks the tree under rootID and returns its direct folder
// children plus the total size of every file (for the sync-scope step).
func (c *Client) FolderSummary(ctx context.Context, rootID string) (topFolders []Node, totalSize int64, err error) {
	queue := []string{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		children, err := c.ListChildren(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		for _, ch := range children {
			if ch.IsDeleted {
				continue
			}
			switch ch.NodeType {
			case "folder":
				if id == rootID {
					topFolders = append(topFolders, ch)
				}
				queue = append(queue, ch.ID)
			case "file":
				totalSize += ch.SizeBytes
			}
		}
	}
	return topFolders, totalSize, nil
}

// HumanSize renders bytes as e.g. "12.3 GB".
func HumanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
