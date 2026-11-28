package browser

import (
	"context"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// controlNow is who has the browser.
func (in *instance) controlNow() inabav1.BrowserControl {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.control
}

// watchControl is who has the browser, and a channel closed once that changes.
func (in *instance) watchControl() (inabav1.BrowserControl, <-chan struct{}) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.changed == nil {
		in.changed = make(chan struct{})
	}
	return in.control, in.changed
}

// setControl hands the browser over; the caller holds in.mu.
func (in *instance) setControl(c inabav1.BrowserControl) {
	if in.control == c {
		return
	}
	in.control = c
	if in.changed != nil {
		close(in.changed)
	}
	in.changed = make(chan struct{})
}

// agentActs is the agent of thread sending the browser a command: it takes the browser, or waits
// while a person has it. It fails only when ctx ends first.
func (in *instance) agentActs(ctx context.Context, thread string) error {
	for {
		in.mu.Lock()
		if in.control != inabav1.BrowserControl_BROWSER_CONTROL_USER {
			in.setControl(inabav1.BrowserControl_BROWSER_CONTROL_AGENT)
			in.agentThread = thread
			in.mu.Unlock()
			return nil
		}
		if in.changed == nil {
			in.changed = make(chan struct{})
		}
		changed := in.changed
		in.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// agentDone lets go of the browser for thread's agent, if it has it: its turn ended, or it hung up.
func (in *instance) agentDone(thread string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.control == inabav1.BrowserControl_BROWSER_CONTROL_AGENT && in.agentThread == thread {
		in.setControl(inabav1.BrowserControl_BROWSER_CONTROL_UNSPECIFIED)
	}
}

// How long a browser someone took stays theirs with no one watching: a viewer that only
// reconnects, as when a panel moves or its stream breaks, keeps it.
var unwatchedGrace = 10 * time.Second

// view counts a person watching; once the last has been gone a while, a browser they had taken
// is given back, so the agent is not left waiting on no one.
func (in *instance) view(delta int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.viewers += delta
	if in.viewers > 0 || in.control != inabav1.BrowserControl_BROWSER_CONTROL_USER {
		return
	}
	// Counted from the last to leave, not the first: a viewer that came and went since starts it again.
	if in.unwatched != nil {
		in.unwatched.Stop()
	}
	in.unwatched = time.AfterFunc(unwatchedGrace, func() {
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.viewers <= 0 && in.control == inabav1.BrowserControl_BROWSER_CONTROL_USER {
			in.setControl(inabav1.BrowserControl_BROWSER_CONTROL_UNSPECIFIED)
		}
	})
}

// personMay refuses what a person does to the browser while the agent is using it.
func (in *instance) personMay() error {
	if in.controlNow() == inabav1.BrowserControl_BROWSER_CONTROL_AGENT {
		return connect.NewError(connect.CodeFailedPrecondition, "the agent is using the browser; take control to use it")
	}
	return nil
}

// turnEnded gives back the browser the thread's agent had.
func (s *service) turnEnded(thread string) {
	s.mu.Lock()
	in := s.browsers[s.keyOf[thread]]
	s.mu.Unlock()
	if in != nil {
		in.agentDone(thread)
	}
}

func (s *service) SetBrowserControl(ctx context.Context, req *inabav1.SetBrowserControlRequest) (*inabav1.BrowserState, error) {
	if req.Control == inabav1.BrowserControl_BROWSER_CONTROL_AGENT {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the agent takes the browser by using it; take it, or give it back")
	}
	in, err := s.open(ctx, req.ThreadId)
	if err != nil {
		return nil, err
	}
	in.mu.Lock()
	in.setControl(req.Control)
	in.mu.Unlock()
	// Taken with no one watching, it is given back like one whose watchers left.
	in.view(0)
	return state(in)
}

// cursorOf is where the agent last put the mouse on a page, if it has.
func (in *instance) cursorOf(pageID string) *inabav1.AgentCursor {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.cursors[pageID]
}

// emulating notes that one of the agent's DevTools sessions gave a page a viewport of its own (Playwright's
// setViewportSize), with how to take it off again; clear nil notes that it took it off itself.
func (in *instance) emulating(pageID, session string, clear func()) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if clear == nil {
		delete(in.emulated[pageID], session)
		return
	}
	if in.emulated == nil {
		in.emulated = map[string]map[string]func(){}
	}
	if in.emulated[pageID] == nil {
		in.emulated[pageID] = map[string]func(){}
	}
	in.emulated[pageID][session] = clear
}

// unemulate takes off the viewports the agent set on a page, so the window's size, a person's, is the page's.
// Only the session that set one can take it off: the browser keeps each session's apart.
func (in *instance) unemulate(pageID string) {
	in.mu.Lock()
	clears := in.emulated[pageID]
	delete(in.emulated, pageID)
	in.mu.Unlock()
	for _, clear := range clears {
		clear()
	}
}

// agentMouse notes the agent's mouse on a page, for those watching it to see.
func (in *instance) agentMouse(pageID, kind string, x, y float64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.cursors == nil {
		in.cursors = map[string]*inabav1.AgentCursor{}
	}
	pressed := in.cursors[pageID].GetPressed()
	switch kind {
	case "mousePressed":
		pressed = true
	case "mouseReleased":
		pressed = false
	}
	in.cursors[pageID] = &inabav1.AgentCursor{X: x, Y: y, Pressed: pressed}
	// Woken like a change of hands: the page's watchers send it on.
	if in.changed != nil {
		close(in.changed)
	}
	in.changed = make(chan struct{})
}
