package agent

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Restore phases, as RestoreEvent.phase carries them (#361). `failed` is not
// emitted by the runtime: Service.RestoreBackupStream turns a returned error
// into it, so every runtime reports a failure the same way.
const (
	restorePhaseOpening    = "opening"
	restorePhaseExtracting = "extracting"
	restorePhaseApplying   = "applying"
	restorePhaseDone       = "done"
	restorePhaseFailed     = "failed"
)

// restoreEmitEvery is the floor between two progress events inside a phase. A
// read is 32KB, so an unthrottled meter would send a message per read — some
// 300,000 for a 10GB world — to a Panel that polls every two seconds.
const restoreEmitEvery = 250 * time.Millisecond

// restoreMeter counts the compressed bytes a restore has read and narrates them
// to emit. A nil emit makes it a no-op, which is what the unary RestoreBackup
// passes: the extraction path is one path whether or not anybody is listening.
type restoreMeter struct {
	ctx   context.Context
	emit  func(*agentpb.RestoreEvent) error
	total int64
	done  int64
	phase string
	entry string
	last  time.Time
	err   error // the first failed emit; the stream is gone, so the restore stops
}

func newRestoreMeter(ctx context.Context, emit func(*agentpb.RestoreEvent) error) *restoreMeter {
	return &restoreMeter{ctx: ctx, emit: emit}
}

// enter moves to a new phase and reports it at once — a phase change is the
// event the operator is waiting on, so it is never throttled.
func (m *restoreMeter) enter(phase string) error {
	m.phase = phase
	if phase != restorePhaseExtracting {
		m.entry = ""
	}
	return m.send(true)
}

// setEntry names the archive entry being staged. It reports only on the
// throttle's schedule: a save-set of thousands of small files must not become
// thousands of messages.
func (m *restoreMeter) setEntry(rel string) error {
	m.entry = rel
	return m.send(false)
}

func (m *restoreMeter) send(force bool) error {
	if m.emit == nil || m.err != nil {
		return m.err
	}
	if !force && time.Since(m.last) < restoreEmitEvery {
		return nil
	}
	m.last = time.Now()
	done := m.done
	if m.total > 0 && done > m.total {
		// A target whose Stat raced a rewrite could read past the size it
		// reported; the meter never claims more than all of it.
		done = m.total
	}
	m.err = m.emit(&agentpb.RestoreEvent{
		Phase: m.phase, BytesDone: done, BytesTotal: m.total, Entry: m.entry,
	})
	return m.err
}

// reader wraps the raw (still compressed) archive stream so every byte the
// gzip layer pulls is counted. It is also where a long single-file extraction
// notices a cancelled context: io.Copy of a 5GB world file is one entry, and
// the between-entries check alone would not see the cancel until it finished.
func (m *restoreMeter) reader(r io.Reader) io.Reader { return &countingReader{r: r, m: m} }

type countingReader struct {
	r io.Reader
	m *restoreMeter
}

func (c *countingReader) Read(p []byte) (int, error) {
	if err := c.m.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	c.m.done += int64(n)
	if serr := c.m.send(false); serr != nil {
		return n, serr
	}
	return n, err
}

// statter is what every backup target's Open hands back underneath: *os.File
// for the local and share targets, and the SFTP/SMB wrappers delegate to their
// remote file's Stat.
type statter interface {
	Stat() (os.FileInfo, error)
}

// archiveSize is the size of an opened archive, or 0 when the reader cannot
// say — in which case the meter reports bytes_total 0 and the Panel draws an
// indeterminate bar instead of a guess.
func archiveSize(r io.Reader) int64 {
	s, ok := r.(statter)
	if !ok {
		return 0
	}
	fi, err := s.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	return fi.Size()
}

// statOf delegates Stat to a wrapped remote file when it has one.
func statOf(r io.Reader) (os.FileInfo, error) {
	if s, ok := r.(statter); ok {
		return s.Stat()
	}
	return nil, os.ErrInvalid
}
