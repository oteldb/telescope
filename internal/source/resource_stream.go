package source

import (
	"context"
	"io"
	"sync"
	"time"
)

func (s *Stream) scanResources(ctx context.Context, r io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	emit := func(l Line) bool {
		select {
		case s.lines <- l:
			return true
		case <-ctx.Done():
			return false
		}
	}
	err := readResourceFrames(r, func(frame []byte, diagnostic bool) bool {
		if diagnostic {
			return emit(Line{Data: append([]byte(nil), frame...), Stderr: true})
		}
		lines, err := s.resources.observe(frame, time.Now())
		if err != nil {
			return emit(Line{Kind: KindReadFailed, Reason: err.Error(), Stderr: true})
		}
		// An unchanged object still proves the watch is reachable. Otherwise
		// reconnecting a quiet cluster would exhaust the empty-read budget.
		s.saw(Line{})
		for _, l := range lines {
			if !emit(l) {
				return false
			}
		}
		return ctx.Err() == nil
	})
	if err != nil && ctx.Err() == nil {
		emit(Line{Kind: KindReadFailed, Reason: err.Error(), Stderr: true})
		s.cancel()
	}
	_, _ = io.Copy(io.Discard, r)
}
