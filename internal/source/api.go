package source

import (
	"context"
)

// startAPI opens a stream backed by a log database rather than a process.
//
// The shape is the same as a command's: lines until the stream ends, then one
// error. What differs is that there is nothing to kill, so closing is the
// context alone.
func startAPI(ctx context.Context, cfg Config) (*Stream, error) {
	// Refused here rather than reported as an error the stream ends with, so a
	// merge draws it where that place's lines would have been and keeps reading
	// the rest.
	if cfg.Collector == CollectorLoki {
		if _, ok := cfg.lokiQuery(); !ok {
			return nil, errNoSelector
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		cfg:    cfg,
		lines:  make(chan Line, 4096),
		done:   make(chan error, 1),
		cancel: cancel,
	}

	out := func(line Line) bool {
		select {
		case s.lines <- line:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go func() {
		var err error
		switch cfg.Collector {
		case CollectorVictoriaLogs:
			err = cfg.streamVictoriaLogs(ctx, out)
		case CollectorLoki:
			err = cfg.streamLoki(ctx, out)
		}
		close(s.lines)
		if ctx.Err() != nil {
			err = nil
		}
		s.done <- err
		close(s.done)
	}()
	return s, nil
}
