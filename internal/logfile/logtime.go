package logfile

import (
	"io"
	"time"
)

// TimestampLayout is the timestamp format every line in brick.log carries
// ("YYYY-MM-DD HH:ii:ss"), in place of the standard log package's built-in
// "2006/01/02 15:04:05". Same layout brick-cli stamps its own lines with (see
// its logTimestampLayout), so the shared log reads consistently whichever app
// wrote a given line.
const TimestampLayout = "2006-01-02 15:04:05"

// timestampWriter prepends the current time, in TimestampLayout, to every
// write before forwarding it. Pair it with log.New(..., 0): the standard log
// package can't be told to format its timestamps any other way, so the only
// way to change them is to turn its own off and supply them here.
//
// One write is assumed to be one line, which is what log.Logger does — it
// makes exactly one Write per Print call. A message containing newlines of
// its own is therefore stamped on its first line only.
type timestampWriter struct{ w io.Writer }

// NewTimestampWriter wraps w so every write to it is prefixed with the
// current timestamp in TimestampLayout.
func NewTimestampWriter(w io.Writer) io.Writer { return &timestampWriter{w: w} }

func (t *timestampWriter) Write(p []byte) (int, error) {
	stamped := append([]byte(time.Now().Format(TimestampLayout)+" "), p...)
	if _, err := t.w.Write(stamped); err != nil {
		return 0, err
	}
	// Report the caller's own length: it wrote p, not our stamped copy, and a
	// short write reported against a longer buffer makes io.Writer contracts
	// (and log.Logger) think the write failed.
	return len(p), nil
}
