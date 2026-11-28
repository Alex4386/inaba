package browser

import (
	"context"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	gproto "google.golang.org/protobuf/proto"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// cast is one page's picture, shown to everyone watching it: a single screencast whose frames each watcher
// gets the latest of. Two tabs on the same thread watch side by side instead of taking the page from each
// other, which restarted the picture, and resized the window, back and forth.
type cast struct {
	subs map[chan *inabav1.PageFrame]struct{}
	stop context.CancelFunc
	// ended is closed when the screencast does: the page closed, or it would not start.
	ended chan struct{}
	err   error
	// last is the latest picture, for one who joins: Chrome sends a frame only when the page changes.
	last *inabav1.PageFrame
}

// watch joins the page's cast, starting it for the first watcher. The picture is the window's own size:
// each viewer scales it down to its panel, so one viewer's panel does not set what the others see.
// leave lets go; the last to leave stops the screencast.
func (s *service) watch(in *instance, p *rod.Page) (frames <-chan *inabav1.PageFrame, ended <-chan struct{}, leave func(), err func() error) {
	id := string(p.TargetID)
	sub := make(chan *inabav1.PageFrame, 1)
	s.mu.Lock()
	c := s.casts[id]
	if c == nil {
		ctx, stop := context.WithCancel(context.Background())
		c = &cast{subs: map[chan *inabav1.PageFrame]struct{}{}, stop: stop, ended: make(chan struct{})}
		s.casts[id] = c
		go s.run(ctx, c, in, p)
	}
	c.subs[sub] = struct{}{}
	// One who joins sees the page at once, with who has it now rather than when the picture was taken.
	if c.last != nil {
		f := gproto.Clone(c.last).(*inabav1.PageFrame)
		f.Control, f.AgentCursor = in.controlNow(), in.cursorOf(id)
		sub <- f
	}
	s.mu.Unlock()
	leave = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(c.subs, sub)
		if len(c.subs) == 0 {
			c.stop()
			if s.casts[id] == c {
				delete(s.casts, id)
			}
		}
	}
	return sub, c.ended, leave, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		return c.err
	}
}

// run screencasts the page to the cast's watchers until no one watches or the page goes.
func (s *service) run(ctx context.Context, c *cast, in *instance, p *rod.Page) {
	id := string(p.TargetID)
	defer func() {
		s.mu.Lock()
		if s.casts[id] == c {
			delete(s.casts, id)
		}
		s.mu.Unlock()
		close(c.ended)
	}()
	// A screencast a viewer gone astray left running would refuse this one.
	_ = proto.PageStopScreencast{}.Call(p)
	p = p.Context(ctx)
	start := proto.PageStartScreencast{Format: proto.PageStartScreencastFormatJpeg, Quality: ptr(70)}
	wait := p.EachEvent(func(f *proto.PageScreencastFrame) bool {
		frame := &inabav1.PageFrame{PageId: id, Jpeg: f.Data, Control: in.controlNow(), AgentCursor: in.cursorOf(id)}
		if m := f.Metadata; m != nil {
			frame.ViewportWidth, frame.ViewportHeight = m.DeviceWidth, m.DeviceHeight
		}
		if info, err := p.Info(); err == nil {
			frame.Url, frame.Title = info.URL, info.Title
		}
		s.mu.Lock()
		c.last = frame
		for sub := range c.subs {
			// The latest frame only: one not yet taken is replaced, so a slow watcher holds no one up.
			select {
			case <-sub:
			default:
			}
			sub <- frame
		}
		s.mu.Unlock()
		// Chrome sends the next frame once this one is acknowledged.
		_ = proto.PageScreencastFrameAck{SessionID: f.SessionID}.Call(p)
		return false
	}, func(e *proto.TargetTargetDestroyed) bool {
		return e.TargetID == p.TargetID
	})
	if err := start.Call(p); err != nil {
		s.mu.Lock()
		c.err = err
		s.mu.Unlock()
		return
	}
	wait()
	// No one watches any more, or the page went; it stops sending, if it is still there to hear it.
	_ = proto.PageStopScreencast{}.Call(p.Context(context.Background()))
}
