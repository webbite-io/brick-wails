package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
)

// --- Retrying transfers through network hiccups and Storage API restarts ---
//
// A streaming upload or download that fails on the way (a dropped or stalled
// connection, or the Storage API answering 502/503/504 while it restarts) is
// tried again with backoff rather than failing the transfer. A download picks
// up where it stopped (see ResumableDownload); an upload starts over, since
// POST /files can't append to what it already received. A transfer gives up
// once TransferRetryWindow passes without it getting any further than it had
// been, so a server that stays down still ends it as failed.

// Retry timing. Variables so tests can shorten them.
var (
	// TransferRetryMin and TransferRetryMax bound the wait between attempts,
	// which doubles from the one to the other and starts over after an
	// attempt that got further.
	TransferRetryMin = 1 * time.Second
	TransferRetryMax = 30 * time.Second
	// TransferRetryWindow is how long a transfer keeps retrying without
	// getting further before it fails.
	TransferRetryWindow = 10 * time.Minute
	// TransferStallTimeout cuts an attempt off when no bytes have moved for
	// this long: a server that went away without closing the connection
	// would otherwise leave it hanging.
	TransferStallTimeout = 2 * time.Minute
)

// TmpSuffix marks an in-progress download; the sync engine's tree walk and
// watcher skip files ending in it.
const TmpSuffix = ".brick-tmp"

// ErrTransferStalled is the error an attempt cut off for not moving fails with.
var ErrTransferStalled = errors.New("the connection stalled")

// TransientError marks err as worth retrying, for a failure whose status
// only its caller can judge (e.g. a source device's answer relayed by the
// Storage API).
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// RetryableStatus reports whether an HTTP status means the server is
// briefly unable to answer: overloaded, restarting, or behind a proxy that
// lost it. A 500 is left out, as it usually fails the same way again.
func RetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// IsTransient reports whether a transfer that failed with err may succeed
// if tried again: the network or server failed, not the request or the
// local disk.
func IsTransient(err error) bool {
	if errors.Is(err, auth.ErrSessionExpired) {
		return false
	}
	var te *TransientError
	if errors.As(err, &te) || errors.Is(err, ErrTransferStalled) {
		return true
	}
	var se *StatusError
	if errors.As(err, &se) {
		return RetryableStatus(se.Code)
	}
	// http.Client wraps every failure to send a request or read its
	// response's headers in a *url.Error.
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF)
}

// RetryObserver is told about each retry of a transfer: the error the
// attempt failed with and how long until the next one.
type RetryObserver func(err error, wait time.Duration)

type retryObserverKey struct{}

// WithRetryObserver returns ctx carrying fn, which RetryTransfer calls on
// every retry of a transfer run under it.
func WithRetryObserver(ctx context.Context, fn RetryObserver) context.Context {
	return context.WithValue(ctx, retryObserverKey{}, fn)
}

type singleAttemptKey struct{}

// WithSingleAttempt returns ctx under which RetryTransfer makes one attempt
// and returns its error, still cut off if it stalls. For a caller that has
// its own way of trying again later, which a transfer that kept retrying
// through an outage would only hold up.
func WithSingleAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptKey{}, true)
}

// TransferAttempt is one try at a transfer, run under ctx, which is cut off
// if touch isn't called for TransferStallTimeout. It reports whether it got
// further than any attempt before it.
type TransferAttempt func(ctx context.Context, touch func()) (progressed bool, err error)

// RetryTransfer runs attempt until it succeeds, fails with an error that
// isn't transient, ctx ends, or TransferRetryWindow passes without an
// attempt getting further. It returns the last attempt's error.
func RetryTransfer(ctx context.Context, attempt TransferAttempt) error {
	observe, _ := ctx.Value(retryObserverKey{}).(RetryObserver)
	single, _ := ctx.Value(singleAttemptKey{}).(bool)
	wait := TransferRetryMin
	giveUpAt := time.Now().Add(TransferRetryWindow)
	for {
		progressed, err := runGuarded(ctx, attempt)
		if err == nil || ctx.Err() != nil || !IsTransient(err) || single {
			return err
		}
		if progressed {
			wait = TransferRetryMin
			giveUpAt = time.Now().Add(TransferRetryWindow)
		}
		if time.Now().Add(wait).After(giveUpAt) {
			return fmt.Errorf("gave up after retrying for %s: %w", TransferRetryWindow, err)
		}
		if observe != nil {
			observe(err, wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		wait = min(wait*2, TransferRetryMax)
	}
}

// runGuarded runs one attempt, cancelling it if it stalls. An attempt cut
// off that way fails with ErrTransferStalled, whatever error the
// cancellation surfaced as.
func runGuarded(ctx context.Context, attempt TransferAttempt) (bool, error) {
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(TransferStallTimeout, func() { cancel(ErrTransferStalled) })
	defer timer.Stop()
	progressed, err := attempt(actx, func() { timer.Reset(TransferStallTimeout) })
	if err != nil && errors.Is(context.Cause(actx), ErrTransferStalled) && ctx.Err() == nil {
		err = fmt.Errorf("%w after %s without data", ErrTransferStalled, TransferStallTimeout)
	}
	return progressed, err
}

// ResumableDownload streams what fetch serves to destPath, via a temporary
// file renamed into place once it's all there, reporting bytes written as it
// goes. An attempt that fails part way is retried (see RetryTransfer) asking
// for only the rest, with Range. If-Range, set to the first response's
// validator, makes a file that changed in the meantime come back whole, and
// the download then starts over. So does a server that ignores Range.
//
// fetch makes the request with the given extra headers. errFor turns a
// response with an unexpected status into the error to fail (or retry) with.
// With noClobber, a file that appeared at destPath while downloading is kept
// and the download fails instead (see PlaceNoClobber).
func ResumableDownload(ctx context.Context, destPath string, total int64, noClobber bool, onProgress func(written, total int64),
	fetch func(ctx context.Context, headers map[string]string) (*http.Response, error),
	errFor func(resp *http.Response) error) error {
	return resumableDownload(ctx, destPath, total, noClobber, onProgress, fetch, errFor, nil)
}

// resumableDownload is ResumableDownload, calling beforePlace (if set) with
// the finished temporary file before it is put in place. An error from it
// fails the download.
func resumableDownload(ctx context.Context, destPath string, total int64, noClobber bool, onProgress func(written, total int64),
	fetch func(ctx context.Context, headers map[string]string) (*http.Response, error),
	errFor func(resp *http.Response) error, beforePlace func(tmpPath string) error) error {
	out, err := CreateTempFor(destPath)
	if err != nil {
		return err
	}
	tmpPath := out.Name()
	var written int64
	var validator string // the ETag or Last-Modified of what's in out
	report := func() {
		if onProgress != nil {
			onProgress(written, total)
		}
	}
	restart := func() error {
		written, validator = 0, ""
		if err := out.Truncate(0); err != nil {
			return err
		}
		_, err := out.Seek(0, io.SeekStart)
		report()
		return err
	}

	err = RetryTransfer(ctx, func(ctx context.Context, touch func()) (bool, error) {
		if written > 0 && written == total {
			return false, nil // the last attempt got every byte but failed before seeing the end
		}
		headers := map[string]string{}
		if written > 0 && validator != "" {
			headers["Range"] = fmt.Sprintf("bytes=%d-", written)
			headers["If-Range"] = validator
		}
		resp, err := fetch(ctx, headers)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			if written > 0 {
				if err := restart(); err != nil {
					return false, err
				}
			}
			validator = responseValidator(resp)
		case http.StatusPartialContent:
			if _, ok := headers["Range"]; !ok || contentRangeStart(resp) != written {
				if err := restart(); err != nil {
					return false, err
				}
				return false, &TransientError{fmt.Errorf("unexpected partial response %q", resp.Header.Get("Content-Range"))}
			}
		default:
			return false, errFor(resp)
		}

		start := written
		buf := make([]byte, 256*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				touch()
				if _, err := out.Write(buf[:n]); err != nil {
					return written > start, err
				}
				written += int64(n)
				report()
			}
			if readErr == io.EOF {
				return written > start, nil
			}
			if readErr != nil {
				return written > start, &TransientError{readErr}
			}
		}
	})
	if err == nil {
		err = out.Close()
	} else {
		out.Close()
	}
	if err == nil && beforePlace != nil {
		err = beforePlace(tmpPath)
	}
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	place := os.Rename
	if noClobber {
		place = PlaceNoClobber
	}
	if err := place(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// ErrDestExists is PlaceNoClobber's error for a destination already taken.
var ErrDestExists = errors.New("a file or folder with this name already exists")

// PlaceNoClobber moves finished download tmp to dest, failing with
// ErrDestExists rather than replacing anything already there. A hard link
// does that atomically; where the disk doesn't support them it falls back
// to checking first, which leaves only a brief window for a race.
func PlaceNoClobber(tmp, dest string) error {
	err := os.Link(tmp, dest)
	if err == nil {
		return os.Remove(tmp)
	}
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s: %w", dest, ErrDestExists)
	}
	if _, lerr := os.Lstat(dest); lerr == nil {
		return fmt.Errorf("%s: %w", dest, ErrDestExists)
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return lerr
	}
	return os.Rename(tmp, dest)
}

// CreateTempFor creates the temporary file a download to destPath is written
// to before it is put in place: next to it, named after it with a random part
// and TmpSuffix, so the sync engine ignores it and two downloads to the same
// place never share one. It is created new (O_EXCL), so a symlink that
// happens to have its name is not followed.
func CreateTempFor(destPath string) (*os.File, error) {
	for range 10 {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		p := fmt.Sprintf("%s.%x%s", destPath, b, TmpSuffix)
		f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if !errors.Is(err, os.ErrExist) {
			return f, err
		}
	}
	return nil, fmt.Errorf("could not pick a temporary file name for %s", destPath)
}

// responseValidator returns what an If-Range for the rest of resp's body
// should name: its strong ETag, else its Last-Modified, else "" (no resuming).
func responseValidator(resp *http.Response) string {
	if etag := resp.Header.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		return etag
	}
	return resp.Header.Get("Last-Modified")
}

// contentRangeStart returns the first byte position of a 206 response's
// Content-Range ("bytes 100-199/200"), or -1 if it can't be read.
func contentRangeStart(resp *http.Response) int64 {
	var start, end, size int64
	if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &size); err != nil {
		return -1
	}
	return start
}
