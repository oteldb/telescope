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
	argv := cfg.Argv()

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	isolate(cmd)
	// Ask the collector to shut down instead of killing it, so ssh can tear
	// down the remote side; WaitDelay bounds how long we honor that.
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, errors.Wrap(err, "stdout")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, errors.Wrap(err, "stderr")
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, errors.Wrapf(err, "start %s", argv[0])
	}

	s := &Stream{
		cfg:    cfg,
		lines:  make(chan Line, 4096),
		done:   make(chan error, 1),
		cancel: cancel,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go s.scan(ctx, stdout, false, &wg)
	go s.scan(ctx, stderr, true, &wg)
	go func() {
		wg.Wait()
		err := cmd.Wait()
		close(s.lines)
		if ctx.Err() != nil {
			err = nil
		}
		s.done <- err
		close(s.done)
	}()
	return s, nil
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
