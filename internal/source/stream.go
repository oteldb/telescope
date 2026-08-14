package source

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/go-faster/errors"
)

// maxLineSize bounds a single log line; stacktraces and payloads can be large.
const maxLineSize = 8 * 1024 * 1024

// Line is a single line read from a source.
type Line struct {
	// Data is the line as it was read. Empty for a note, which says what it has
	// to say in Kind and Reason and is written out where lines are rendered.
	Data   []byte
	Stderr bool
	// Kind says whether the line is one the source wrote or telescope's own
	// words about it. A collector's stderr is the source talking and stays a
	// line like any other; this is the view's warrant to draw one differently.
	Kind Kind
	// Reason is why, for a note: the failure as it was reported, and nothing
	// telescope wrote around it. A note reads as a sentence somewhere else
	// entirely, since the same sentence has to reach the screen and the filter.
	Reason string
	// At is when the line was written, for a source that reports it out of
	// band. Zero when the line is all there is, which is the usual case: a
	// timestamp inside the line is the parser's business, not the source's.
	At time.Time
	// Source names which stream the line came from, for a merge. Empty when
	// there is only one, since a label nobody can be confused with says nothing.
	Source string
	// Labels are the attributes the source reported beside the line, which is
	// how a log database says what a bare message cannot: the pod it came
	// from, the severity it was indexed under. Nil for a collector that hands
	// over the line and nothing else.
	Labels []Label
}

// options are the knobs [Start] takes.
type options struct {
	timeFunc func(Line) time.Time
}

// Option adjusts how a stream is opened.
type Option func(*options)

// WithTimeFunc says how to date a line whose source does not report a time out
// of band, which is what a merge orders by. Extracting it means parsing the
// line, and what a log line looks like is not this package's business, so the
// caller that does the parsing brings it.
func WithTimeFunc(f func(Line) time.Time) Option {
	return func(o *options) { o.timeFunc = f }
}

// Stream is a running collector command.
type Stream struct {
	cfg    Config
	lines  chan Line
	done   chan error
	cancel context.CancelFunc

	// mu guards what the reopen loop needs to know about what has already been
	// read: how much of it there was, and how far it got. Both scanners write
	// here and the loop reads between them.
	mu     sync.Mutex
	nread  int
	lastAt time.Time
}

// read is how many lines the collector has written to stdout, which is what
// tells a stream that ended having read something from one that could not be
// read at all.
func (s *Stream) read() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nread
}

// since is the newest line seen, which is where reading picks up again.
func (s *Stream) since() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAt
}

// saw records a line the collector wrote.
func (s *Stream) saw(l Line) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nread++
	if l.At.After(s.lastAt) {
		s.lastAt = l.At
	}
}

// Start opens the stream described by cfg, spawning a command or, for a
// [Collector.IsRemoteAPI] collector, querying the endpoint over HTTP.
func Start(ctx context.Context, cfg Config, opts ...Option) (*Stream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var opt options
	for _, o := range opts {
		o(&opt)
	}
	switch {
	case cfg.Collector == CollectorMerge:
		return startMerge(ctx, cfg, opt)
	case cfg.Collector.IsRemoteAPI():
		return startAPI(ctx, cfg)
	default:
		return startCommand(ctx, cfg)
	}
}

func startCommand(ctx context.Context, cfg Config) (*Stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd, stdout, stderr, err := spawnCommand(ctx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	s := &Stream{
		cfg:    cfg,
		lines:  make(chan Line, 4096),
		done:   make(chan error, 1),
		cancel: cancel,
	}

	// The watch outlives nothing: it is stopped when the reading is, since a
	// pod still restarting is not worth saying to a reader whose stream has
	// ended, and a stream that waited for it would never close.
	watchCtx, stopWatch := context.WithCancel(ctx)
	var watching sync.WaitGroup
	startWatch(watchCtx, cfg, s.lines, &watching)

	go func() {
		err := s.follow(ctx, cfg, cmd, stdout, stderr)
		stopWatch()
		watching.Wait()
		close(s.lines)
		if ctx.Err() != nil {
			err = nil
		}
		s.done <- err
		close(s.done)
	}()
	return s, nil
}

// spawnCommand starts one collector process. It is a variable so the reopen
// loop can be read without a cluster to restart.
var spawnCommand = func(ctx context.Context, cfg Config) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	argv := cfg.Argv()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	isolate(cmd)
	// Ask the collector to shut down instead of killing it, so ssh can tear
	// down the remote side; WaitDelay bounds how long we honor that.
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "stdout")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "stderr")
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, errors.Wrapf(err, "start %s", argv[0])
	}
	return cmd, stdout, stderr, nil
}

// reopenAfter is how long to wait before opening a collector again, doubling
// with each attempt that read nothing.
func reopenAfter(attempt int) time.Duration {
	d := reopenBase << min(attempt, 4)
	return min(d, reopenCap)
}

// reopenBase and reopenCap pace the reopening. They are variables so a test can
// read the loop without waiting out the politeness.
var (
	reopenBase = 250 * time.Millisecond
	reopenCap  = 4 * time.Second
)

const (
	// maxReopen is how many times in a row a collector may be opened and read
	// nothing before the stream is called finished. A container crash-looping
	// writes something every time round, so this bounds the case where there is
	// nothing to read rather than the case worth following.
	maxReopen = 5
)

// follow reads one collector to its end, and opens it again where that end is
// not the end of the thing being read.
//
// "kubectl logs -f" ends when the container does, so a restart looks exactly
// like a finished stream: the view would go dead beside a pod that is running.
// Reopening asks from the last line's time rather than from the top, which
// costs a duplicate second at the seam — kubectl's --since-time is written to
// the second — and never a lost line. A duplicate is a row the list already
// folds; a hole in a log is not something the reader can find later.
func (s *Stream) follow(ctx context.Context, cfg Config, cmd *exec.Cmd, stdout, stderr io.ReadCloser) error {
	for attempt := 0; ; {
		before := s.read()

		var wg sync.WaitGroup
		wg.Add(2)
		go s.scan(ctx, stdout, false, &wg)
		go s.scan(ctx, stderr, true, &wg)
		wg.Wait()
		err := cmd.Wait()

		switch {
		case ctx.Err() != nil, !cfg.reopens():
			return err
		}
		// Only what came back on stdout counts as having read something: a
		// place that answers "no such resource" writes on stderr every time,
		// and reopening it forever is a way to never say so.
		if s.read() > before {
			attempt = 0
		} else {
			attempt++
			if attempt >= maxReopen {
				return err
			}
		}

		select {
		case <-time.After(reopenAfter(attempt)):
		case <-ctx.Done():
			return err
		}

		var next *exec.Cmd
		if next, stdout, stderr, err = spawnCommand(ctx, cfg.resume(s.since())); err != nil {
			return err
		}
		cmd = next
	}
}

// Config returns the config the stream was started with.
func (s *Stream) Config() Config { return s.cfg }

// Lines yields log lines until the command exits.
func (s *Stream) Lines() <-chan Line { return s.lines }

// Done yields the command exit error once, after Lines is closed.
func (s *Stream) Done() <-chan error { return s.done }

// Close terminates the command.
func (s *Stream) Close() { s.cancel() }

// unstamp takes off the RFC3339 timestamp docker and kubectl prefix their lines
// with when asked to, so the time is carried beside the line rather than
// rendered in front of it. A line without one is returned as it came.
func unstamp(data []byte) ([]byte, time.Time) {
	stamp, rest, ok := bytes.Cut(data, []byte(" "))
	if !ok {
		return data, time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, string(stamp))
	if err != nil {
		return data, time.Time{}
	}
	return rest, at
}

func (s *Stream) scan(ctx context.Context, r io.Reader, isErr bool, wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for sc.Scan() {
		// Copy: the scanner reuses its buffer on the next Scan. Trailing \r
		// comes from the pty forced for ssh follow mode.
		data := bytes.TrimSuffix(sc.Bytes(), []byte("\r"))
		line := Line{Data: append([]byte(nil), data...), Stderr: isErr}
		if s.cfg.Stamps() {
			line.Data, line.At = unstamp(line.Data)
		}
		if !isErr {
			s.saw(line)
		}
		select {
		case s.lines <- line:
		case <-ctx.Done():
			return
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		// Surface read failures (most often a line over maxLineSize) in the
		// stream itself instead of losing the rest of the output silently.
		select {
		case s.lines <- Line{Kind: KindReadFailed, Reason: err.Error(), Stderr: true}:
		case <-ctx.Done():
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
