package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Streaming file transfers: what remote control's uploads and downloads use,
// so a large file is never held in memory. Ported from brick-cli
// cmd/brick/transfer.go.

// GetNode fetches one node by ID.
func (c *Client) GetNode(ctx context.Context, id string) (*Node, error) {
	var n Node
	if err := c.getJSON(ctx, "/nodes/"+url.PathEscape(id), &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// DownloadToFile streams GET /files/{id} to destPath, via a temporary file
// renamed into place once it's all there. total is the file's known size
// (from its node), used to report progress and to tell a finished download
// from one cut off at its last byte. A download that fails part way resumes
// where it stopped (see ResumableDownload).
func (c *Client) DownloadToFile(ctx context.Context, nodeID, destPath string, total int64, onProgress func(written, total int64)) error {
	return c.downloadNode(ctx, nodeID, destPath, total, false, onProgress)
}

// DownloadToNewFile is DownloadToFile for a destPath that must not exist: if
// something appears there while downloading, it is kept and the download
// fails with ErrDestExists.
func (c *Client) DownloadToNewFile(ctx context.Context, nodeID, destPath string, total int64, onProgress func(written, total int64)) error {
	return c.downloadNode(ctx, nodeID, destPath, total, true, onProgress)
}

func (c *Client) downloadNode(ctx context.Context, nodeID, destPath string, total int64, noClobber bool, onProgress func(written, total int64)) error {
	return ResumableDownload(ctx, destPath, total, noClobber, onProgress, func(ctx context.Context, headers map[string]string) (*http.Response, error) {
		return c.StreamGet(ctx, "/files/"+url.PathEscape(nodeID), headers)
	}, errFrom)
}

// DownloadedFile is what DownloadForSync reports about the file it wrote.
type DownloadedFile struct {
	Size   int64
	ETag   string // the response's ETag, unquoted; "" if it had none
	SHA256 string // hex SHA-256 of the bytes put at destPath
}

// DownloadForSync is DownloadToFile for the sync engine, which records what
// it wrote: the file's ETag, and the size and SHA-256 of the bytes put at
// destPath. They're hashed off the temporary file before it is put in place,
// so an edit landing right after can't be mistaken for the download.
func (c *Client) DownloadForSync(ctx context.Context, nodeID, destPath string, total int64) (*DownloadedFile, error) {
	var d DownloadedFile
	fetch := func(ctx context.Context, headers map[string]string) (*http.Response, error) {
		resp, err := c.StreamGet(ctx, "/files/"+url.PathEscape(nodeID), headers)
		if err == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) {
			d.ETag = strings.Trim(resp.Header.Get("ETag"), `"`)
		}
		return resp, err
	}
	hashTemp := func(tmpPath string) error {
		f, err := os.Open(tmpPath)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if d.Size, err = io.Copy(h, f); err != nil {
			return err
		}
		d.SHA256 = hex.EncodeToString(h.Sum(nil))
		return nil
	}
	if err := resumableDownload(ctx, destPath, total, false, nil, fetch, errFrom, hashTemp); err != nil {
		return nil, err
	}
	return &d, nil
}

// StreamGet is Request for a GET whose response body is a file: it has no
// overall timeout, which a large file would outlast, so it relies on ctx
// (and RetryTransfer's stall timeout) to end it.
func (c *Client) StreamGet(ctx context.Context, path string, headers map[string]string) (*http.Response, error) {
	return c.Auth.DoStream(ctx, "GET", c.accountURL(path), nil, 0, headers, nil)
}

func (c *Client) accountURL(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + "/v1/accounts/" + c.AccountID + path
}

// streamRequest is the shared body of UploadStream/ReplaceStream: issue an
// authenticated streaming request and decode the resulting node. An attempt
// that fails on the way is retried from the start (see RetryTransfer), so
// onProgress can go back to zero. Retrying after a response that was lost
// once the server had stored the file stores it again, which for a name
// already taken is one more version of the same content.
func (c *Client) streamRequest(ctx context.Context, method, path string, body io.ReadSeeker, size int64, headers map[string]string, onProgress func(written int64), wantStatus int) (*Node, error) {
	var node *Node
	var furthest int64 // the most bytes any attempt has sent
	err := RetryTransfer(ctx, func(ctx context.Context, touch func()) (bool, error) {
		var sent int64
		resp, err := c.Auth.DoStream(ctx, method, c.accountURL(path), body, size, headers, func(written int64) {
			touch()
			sent = written
			if onProgress != nil {
				onProgress(written)
			}
		})
		progressed := sent > furthest
		furthest = max(furthest, sent)
		if err != nil {
			return progressed, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != wantStatus {
			return progressed, errFrom(resp)
		}
		var result UploadResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return progressed, &TransientError{err}
		}
		node = &result.Node
		return progressed, nil
	})
	return node, err
}

// UploadStream is Upload's streaming counterpart: POST /files without
// buffering the whole file in memory first, reporting progress as the body
// is read off disk and sent.
func (c *Client) UploadStream(ctx context.Context, parentID, name string, r io.ReadSeeker, size int64, onProgress func(written int64)) (*Node, error) {
	headers := map[string]string{
		"Content-Type": "application/octet-stream",
		"X-Parent-ID":  parentID,
		"X-Filename":   name,
	}
	return c.streamRequest(ctx, "POST", "/files", r, size, headers, onProgress, http.StatusCreated)
}

// ReplaceStream is Replace's streaming counterpart.
func (c *Client) ReplaceStream(ctx context.Context, nodeID string, r io.ReadSeeker, size int64, onProgress func(written int64)) (*Node, error) {
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	return c.streamRequest(ctx, "PUT", "/files/"+url.PathEscape(nodeID), r, size, headers, onProgress, http.StatusOK)
}
