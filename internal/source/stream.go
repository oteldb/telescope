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
	Data   []byte
	Stderr bool
}

// Stream is a running collector command.
type Stream struct {
	cfg    Config
	lines  chan Line
	done   chan error
	cancel context.CancelFunc
}

// Start spawns the command described by cfg.
func Start(ctx context.Context, cfg Config) (*Stream, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
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

func (s *Stream) scan(ctx context.Context, r io.Reader, isErr bool, wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for sc.Scan() {
		// Copy: the scanner reuses its buffer on the next Scan. Trailing \r
		// comes from the pty forced for ssh follow mode.
		data := bytes.TrimSuffix(sc.Bytes(), []byte("\r"))
		line := Line{Data: append([]byte(nil), data...), Stderr: isErr}
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
		case s.lines <- Line{Data: []byte("telescope: read: " + err.Error()), Stderr: true}:
		case <-ctx.Done():
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
