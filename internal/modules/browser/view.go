package browser

import (
	"context"
	"net/url"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
)

// page finds a page of the thread's browser by id, or its newest page.
func (s *service) page(ctx context.Context, threadID, pageID string) (*instance, *rod.Page, error) {
	in, err := s.open(ctx, threadID)
	if err != nil {
		return nil, nil, err
	}
	if pageID == "" {
		st, err := state(in)
		if err != nil {
			return nil, nil, err
		}
		if len(st.Pages) == 0 {
			p, err := in.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
			if err != nil {
				return nil, nil, connect.NewError(connect.CodeUnavailable, "could not open a page: "+err.Error())
			}
			return in, p, nil
		}
		pageID = st.Pages[len(st.Pages)-1].Id
	}
	p, err := in.browser.PageFromTarget(proto.TargetTargetID(pageID))
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeNotFound, "no such page in the browser")
	}
	return in, p, nil
}

func (s *service) WatchPage(ctx context.Context, req *inabav1.WatchPageRequest, stream inabav1connect.BrowserServiceWatchPageServerStream) error {
	in, p, err := s.page(ctx, req.ThreadId, req.PageId)
	if err != nil {
		return err
	}
	in.touch(1)
	defer in.touch(-1)
	in.view(1)
	defer in.view(-1)
	// The stream is written from here and from the hand-over below.
	var sendMu sync.Mutex
	closed := false
	send := func(f *inabav1.PageFrame) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return context.Canceled
		}
		return stream.Send(f)
	}
	// Nothing is sent once the call is over: a hand-over heard just then is dropped, not written to a closed stream.
	defer func() {
		sendMu.Lock()
		closed = true
		sendMu.Unlock()
	}()
	// Who has the browser, now and whenever that changes, so the viewer knows whether it may act.
	control, changed := in.watchControl()
	pageID := string(p.TargetID)
	if err := send(&inabav1.PageFrame{PageId: pageID, Control: control, AgentCursor: in.cursorOf(pageID)}); err != nil {
		return err
	}
	go func(changed <-chan struct{}) {
		for {
			select {
			case <-changed:
			case <-ctx.Done():
				return
			}
			var now inabav1.BrowserControl
			now, changed = in.watchControl()
			if send(&inabav1.PageFrame{PageId: pageID, Control: now, AgentCursor: in.cursorOf(pageID)}) != nil {
				return
			}
		}
	}(changed)
	// The window stays as the agent has it while it works: a new size would change the page under it.
	if req.WindowWidth > 0 && req.WindowHeight > 0 && control != inabav1.BrowserControl_BROWSER_CONTROL_AGENT {
		if err := resize(p, int(req.WindowWidth), int(req.WindowHeight)); err != nil {
			return connect.NewError(connect.CodeUnavailable, "could not size the window: "+err.Error())
		}
	}
	// The picture, shared with whoever else watches this page.
	frames, ended, leave, failed := s.watch(in, p)
	defer leave()
	for {
		select {
		case f := <-frames:
			if err := send(f); err != nil {
				return err
			}
		case <-ended:
			if err := failed(); err != nil {
				return connect.NewError(connect.CodeUnavailable, "the page would not show itself: "+err.Error())
			}
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func ptr[T any](v T) *T { return &v }

// The smallest window the browser is made, so a page still lays itself out sensibly.
const minWindowWidth, minWindowHeight = 320, 240

// resize makes the window of the page this size, in CSS pixels. In a headless browser
// the window is the viewport, so pages see a real size, not an emulated one, and so
// does the agent's Playwright.
func resize(p *rod.Page, width, height int) error {
	win, err := proto.BrowserGetWindowForTarget{TargetID: p.TargetID}.Call(p)
	if err != nil {
		return err
	}
	// Even headless, a window can be taller or wider than its page, by a frame no one
	// sees; measure it and add it, so the page gets the size asked for.
	inner, err := proto.RuntimeEvaluate{Expression: "[innerWidth, innerHeight]", ReturnByValue: true}.Call(p)
	if err != nil {
		return err
	}
	size := inner.Result.Value.Arr()
	extraW, extraH := 0, 0
	if len(size) == 2 && win.Bounds != nil && win.Bounds.Width != nil && win.Bounds.Height != nil {
		extraW, extraH = max(0, *win.Bounds.Width-size[0].Int()), max(0, *win.Bounds.Height-size[1].Int())
	}
	return proto.BrowserSetWindowBounds{WindowID: win.WindowID, Bounds: &proto.BrowserBounds{
		Width: ptr(max(width, minWindowWidth) + extraW), Height: ptr(max(height, minWindowHeight) + extraH),
	}}.Call(p)
}

func (s *service) Input(ctx context.Context, req *inabav1.BrowserInputRequest) (*inabav1.BrowserInputResponse, error) {
	in, p, err := s.page(ctx, req.ThreadId, req.PageId)
	if err != nil {
		return nil, err
	}
	in.touch(0)
	if err := in.personMay(); err != nil {
		return nil, err
	}
	switch e := req.Event.(type) {
	case *inabav1.BrowserInputRequest_Mouse:
		m := e.Mouse
		types := map[string]proto.InputDispatchMouseEventType{
			"pressed": proto.InputDispatchMouseEventTypeMousePressed, "released": proto.InputDispatchMouseEventTypeMouseReleased,
			"moved": proto.InputDispatchMouseEventTypeMouseMoved, "wheel": proto.InputDispatchMouseEventTypeMouseWheel,
		}
		t, ok := types[m.Type]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a mouse event is pressed, released, moved or wheel")
		}
		ev := proto.InputDispatchMouseEvent{Type: t, X: m.X, Y: m.Y, Modifiers: int(m.Modifiers), ClickCount: int(m.Clicks), DeltaX: m.DeltaX, DeltaY: m.DeltaY}
		// Without the buttons held, a move is only hovering: the page sees no drag.
		if m.Buttons != 0 {
			ev.Buttons = ptr(int(m.Buttons))
		}
		if m.Button != "" {
			ev.Button = proto.InputMouseButton(m.Button)
		} else if t != proto.InputDispatchMouseEventTypeMouseWheel {
			ev.Button = proto.InputMouseButtonNone
		}
		err = ev.Call(p)
	case *inabav1.BrowserInputRequest_Key:
		k := e.Key
		t := proto.InputDispatchKeyEventTypeKeyUp
		if k.Type == "down" {
			// A key that types makes its character; one that does not is only pressed.
			t = proto.InputDispatchKeyEventTypeRawKeyDown
			if k.Text != "" {
				t = proto.InputDispatchKeyEventTypeKeyDown
			}
		}
		err = proto.InputDispatchKeyEvent{Type: t, Key: k.Key, Code: k.Code, WindowsVirtualKeyCode: int(k.KeyCode), Text: k.Text, UnmodifiedText: k.Text, Modifiers: int(k.Modifiers), Commands: k.Commands}.Call(p)
	case *inabav1.BrowserInputRequest_Text:
		// Inserting text also commits whatever was being composed.
		err = proto.InputInsertText{Text: e.Text}.Call(p)
	case *inabav1.BrowserInputRequest_Composition:
		n := len([]rune(e.Composition))
		err = proto.InputImeSetComposition{Text: e.Composition, SelectionStart: n, SelectionEnd: n}.Call(p)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "say what to put in: a mouse event, a key or text")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the page did not take it: "+err.Error())
	}
	return &inabav1.BrowserInputResponse{}, nil
}

func (s *service) Navigate(ctx context.Context, req *inabav1.NavigateRequest) (*inabav1.BrowserState, error) {
	if req.Url != "" {
		if u, err := url.Parse(req.Url); err != nil || u.Scheme == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "give a full address, such as https://example.com")
		}
	}
	if req.NewPage {
		in, err := s.open(ctx, req.ThreadId)
		if err != nil {
			return nil, err
		}
		p, err := in.browser.Page(proto.TargetCreateTarget{URL: req.Url})
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, "could not open the page: "+err.Error())
		}
		st, err := state(in)
		if st != nil {
			st.Opened = string(p.TargetID)
		}
		return st, err
	}
	in, p, err := s.page(ctx, req.ThreadId, req.PageId)
	if err != nil {
		return nil, err
	}
	// Closing a page is closing its tab, which is the person's to do; going somewhere in it is not, while the agent works there.
	if req.Action != "close" {
		if err := in.personMay(); err != nil {
			return nil, err
		}
	}
	switch {
	case req.Action == "back":
		err = p.NavigateBack()
	case req.Action == "forward":
		err = p.NavigateForward()
	case req.Action == "reload":
		err = p.Reload()
	case req.Action == "close":
		if err := p.Close(); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, "the page did not close: "+err.Error())
		}
		return state(in)
	case req.Url != "":
		err = p.Navigate(req.Url)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "give an address, or back, forward or reload")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the page did not go: "+err.Error())
	}
	// Answer with the page where it went; a page that never finishes loading is not waited for long.
	_ = p.Timeout(loadWait).WaitLoad()
	return state(in)
}

// How long Navigate waits for a page to load before answering anyway.
const loadWait = 10 * time.Second
