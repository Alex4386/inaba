package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/host"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/testdir"
	"github.com/Alex4386/inaba/pkg/module"
	"github.com/Alex4386/inaba/pkg/provider"
)

type idleAgent struct{}

func (idleAgent) ID() string { return "idle" }
func (idleAgent) Open(context.Context, provider.Config, provider.Emit) (provider.Session, error) {
	return idleSession{}, nil
}

type idleSession struct{}

func (idleSession) NativeSessionID() string                                 { return "n" }
func (idleSession) Prompt(context.Context, provider.Prompt) (string, error) { return "end_turn", nil }
func (idleSession) Interrupt(context.Context) error                         { return nil }
func (idleSession) Respond(context.Context, string, string) error           { return nil }
func (idleSession) Close() error                                            { return nil }

// A page that shows what is done to it in its title.
const testPage = `<!doctype html><title>start</title>
<style>body{margin:0} button{position:absolute;left:0;top:0;width:200px;height:100px} input{position:absolute;left:0;top:200px;width:200px;height:40px}</style>
<button onclick="document.title='clicked'">click</button>
<input oninput="document.title='typed:'+this.value">`

func TestABrowserForEachThread(t *testing.T) {
	if _, ok := launcher.LookPath(); !ok && os.Getenv("INABA_BROWSER") == "" {
		t.Skip("no Chrome or Chromium on this machine")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("no Node on this machine")
	}
	ctx := rpc.Trusted(context.Background())
	h := host.New(idleAgent{})
	t.Cleanup(h.Close)
	if err := h.OpenData(filepath.Join(testdir.New(t), "inaba.db")); err != nil {
		t.Fatal(err)
	}
	s, err := newService(h, h, module.DataDir(h.DataDir()), module.Store{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stop)
	if err := h.Contribute([]module.Contribution{s.contribution()}); err != nil {
		t.Fatal(err)
	}
	handler, _ := h.Handler()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	client := inabav1connect.NewBrowserServiceClient(connect.NewClient(connecthttp.NewTransport(api.Client(), api.URL)))
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(testPage)) }))
	t.Cleanup(site.Close)

	th, err := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "idle", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenBrowser(ctx, &inabav1.OpenBrowserRequest{ThreadId: "nope"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a browser for no thread: %v", err)
	}
	st, err := client.Navigate(ctx, &inabav1.NavigateRequest{ThreadId: th.Id, Url: site.URL})
	if err != nil || len(st.Pages) != 1 || st.Pages[0].Url != site.URL+"/" {
		t.Fatalf("navigate: %v %v", st, err)
	}
	page := st.Pages[0].Id
	titled := func(want string) {
		t.Helper()
		for range 100 {
			st, _ := client.OpenBrowser(ctx, &inabav1.OpenBrowserRequest{ThreadId: th.Id})
			if len(st.GetPages()) > 0 && st.Pages[0].Title == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("the page's title never became %q", want)
	}
	titled("start")

	// What a person sees: a picture of the page, with the viewport size to aim by.
	watchCtx, stopWatching := context.WithCancel(ctx)
	frames, err := client.WatchPage(watchCtx, &inabav1.WatchPageRequest{ThreadId: th.Id, PageId: page, MaxWidth: 640})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := frames.Receive()
	if err != nil || len(frame.Jpeg) < 100 || string(frame.Jpeg[:2]) != "\xff\xd8" || frame.ViewportWidth < 100 || frame.PageId != page {
		t.Fatalf("frame: %d bytes, %vx%v, %v", len(frame.GetJpeg()), frame.GetViewportWidth(), frame.GetViewportHeight(), err)
	}

	// What a person does: click, then type into the field.
	click := func(x, y float64) {
		t.Helper()
		for _, kind := range []string{"pressed", "released"} {
			if _, err := client.Input(ctx, &inabav1.BrowserInputRequest{ThreadId: th.Id, PageId: page, Event: &inabav1.BrowserInputRequest_Mouse{Mouse: &inabav1.MouseInput{Type: kind, X: x, Y: y, Button: "left", Clicks: 1}}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	click(50, 50)
	titled("clicked")
	click(50, 220)
	if _, err := client.Input(ctx, &inabav1.BrowserInputRequest{ThreadId: th.Id, PageId: page, Event: &inabav1.BrowserInputRequest_Text{Text: "hi"}}); err != nil {
		t.Fatal(err)
	}
	titled("typed:hi")
	// A change shows: a new frame arrives after the input.
	if _, err := frames.Receive(); err != nil {
		t.Fatalf("no frame after the change: %v", err)
	}
	stopWatching()

	// Hangul, as an input method composes it: shown in place, then committed.
	input := func(e any) {
		t.Helper()
		req := &inabav1.BrowserInputRequest{ThreadId: th.Id, PageId: page}
		switch v := e.(type) {
		case *inabav1.BrowserInputRequest_Composition:
			req.Event = v
		case *inabav1.BrowserInputRequest_Text:
			req.Event = v
		case *inabav1.BrowserInputRequest_Key:
			req.Event = v
		}
		if _, err := client.Input(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"ㅎ", "하", "한"} {
		input(&inabav1.BrowserInputRequest_Composition{Composition: c})
	}
	titled("typed:hi한")
	input(&inabav1.BrowserInputRequest_Text{Text: "한"})
	for _, c := range []string{"ㄱ", "그", "글"} {
		input(&inabav1.BrowserInputRequest_Composition{Composition: c})
	}
	input(&inabav1.BrowserInputRequest_Text{Text: "글"})
	titled("typed:hi한글")
	// A shortcut headless Chrome only follows as an editing command.
	input(&inabav1.BrowserInputRequest_Key{Key: &inabav1.KeyInput{Type: "down", Key: "a", Code: "KeyA", KeyCode: 65, Modifiers: 2, Commands: []string{"selectAll"}}})
	input(&inabav1.BrowserInputRequest_Key{Key: &inabav1.KeyInput{Type: "up", Key: "a", Code: "KeyA", KeyCode: 65, Modifiers: 2}})
	input(&inabav1.BrowserInputRequest_Text{Text: "x"})
	titled("typed:x")

	// Japanese: romaji becomes kana as it is typed, then the kana become kanji.
	input(&inabav1.BrowserInputRequest_Key{Key: &inabav1.KeyInput{Type: "down", Key: "a", Code: "KeyA", KeyCode: 65, Modifiers: 2, Commands: []string{"selectAll"}}})
	for _, c := range []string{"n", "に", "にh", "にほ", "にほn", "にほん"} {
		input(&inabav1.BrowserInputRequest_Composition{Composition: c})
	}
	titled("typed:にほん")
	input(&inabav1.BrowserInputRequest_Composition{Composition: "日本"})
	input(&inabav1.BrowserInputRequest_Text{Text: "日本"})
	titled("typed:日本")

	// Chinese pinyin: the letters show while they are typed, then the characters chosen replace them.
	for _, c := range []string{"z", "zh", "zho", "zhon", "zhong", "zhong w", "zhong we", "zhong wen"} {
		input(&inabav1.BrowserInputRequest_Composition{Composition: c})
	}
	titled("typed:日本zhong wen")
	input(&inabav1.BrowserInputRequest_Text{Text: "中文"})
	titled("typed:日本中文")
	// A composition given up leaves nothing behind.
	input(&inabav1.BrowserInputRequest_Composition{Composition: "ㅎ"})
	input(&inabav1.BrowserInputRequest_Composition{Composition: ""})
	input(&inabav1.BrowserInputRequest_Text{Text: "!"})
	titled("typed:日本中文!")

	// The window takes the size it is shown at, and the page lays itself out for it.
	for _, size := range [][2]uint32{{600, 400}, {1000, 700}} {
		watching, stop := context.WithCancel(ctx)
		sized, err := client.WatchPage(watching, &inabav1.WatchPageRequest{ThreadId: th.Id, PageId: page, WindowWidth: size[0], WindowHeight: size[1]})
		if err != nil {
			stop()
			t.Fatal(err)
		}
		f, err := sized.Receive()
		// Ended before any check can fail, so a failure does not leave the stream holding the server open.
		stop()
		if err != nil || f.ViewportWidth != float64(size[0]) || f.ViewportHeight != float64(size[1]) {
			t.Fatalf("at %v the page is %vx%v: %v", size, f.GetViewportWidth(), f.GetViewportHeight(), err)
		}
	}

	// A second viewer of the page takes over; the first one's stream ends.
	first, err := client.WatchPage(ctx, &inabav1.WatchPageRequest{ThreadId: th.Id, PageId: page})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Receive(); err != nil {
		t.Fatalf("first viewer: %v", err)
	}
	secondCtx, stopSecond := context.WithCancel(ctx)
	second, err := client.WatchPage(secondCtx, &inabav1.WatchPageRequest{ThreadId: th.Id, PageId: page, WindowWidth: 900, WindowHeight: 600})
	if err != nil {
		stopSecond()
		t.Fatal(err)
	}
	f2, err := second.Receive()
	stopSecond()
	if err != nil || len(f2.Jpeg) == 0 {
		t.Fatalf("the viewer that took over got no picture: %v", err)
	}
	for {
		if _, err := first.Receive(); err != nil {
			break
		}
	}

	// What the agent's Playwright does: discover the browser through the proxy and drive it.
	mcp := s.mcpFor(th)
	args := strings.Join(mcp[0].Args, " ")
	endpoint := mcp[0].Args[slices.Index(mcp[0].Args, "--cdp-endpoint")+1]
	if mcp[0].Command != "npx" || !strings.HasPrefix(endpoint, "http://127.0.0.1:") || !strings.Contains(args, "--output-dir "+filepath.Join(s.dir, th.Id+"-output")) {
		t.Fatalf("mcp server: %+v", mcp)
	}
	res, err := http.Get(endpoint + "/json/version/")
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	json.NewDecoder(res.Body).Decode(&version)
	res.Body.Close()
	if !strings.HasPrefix(version.WebSocketDebuggerURL, "ws://127.0.0.1:") || !strings.Contains(version.WebSocketDebuggerURL, "/"+th.Id+"/devtools/browser/") {
		t.Fatalf("discovery: %+v", version)
	}
	agent := rod.New().ControlURL(version.WebSocketDebuggerURL)
	if err := agent.Connect(); err != nil {
		t.Fatalf("connecting through the proxy: %v", err)
	}
	pages, err := agent.Pages()
	if err != nil || len(pages) != 1 || pages[0].MustInfo().Title != "typed:日本中文!" {
		t.Fatalf("what the agent sees: %v %v", pages, err)
	}
	agent.Close()
	if res, _ := http.Get(strings.Replace(endpoint, s.token, "wrong", 1) + "/json/version"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("the proxy without its token answered %d", res.StatusCode)
	}

	// A deleted thread's browser goes, profile and all.
	profile := filepath.Join(s.dir, th.Id)
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("no profile: %v", err)
	}
	if _, err := h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: th.Id}); err != nil {
		t.Fatal(err)
	}
	// Heard of at once, not at the next look round.
	for i := 0; ; i++ {
		s.mu.Lock()
		running := len(s.browsers)
		s.mu.Unlock()
		_, statErr := os.Stat(profile)
		if running == 0 && os.IsNotExist(statErr) {
			break
		}
		if i == 100 {
			t.Fatalf("a deleted thread's browser: %d running, profile: %v", running, statErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The threads of a project set to share one browser get the same browser, with the switches
	// chosen; deleting one of them leaves it to the other.
	proj, err := h.CreateProject(ctx, &inabav1.CreateProjectRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Settings are for those who look after the host: a caller inaba-server vouched no such permission for is refused.
	if _, err := client.SaveBrowserSettings(ctx, &inabav1.BrowserSettings{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("settings saved without the hosts permission: %v", err)
	}
	if _, err := s.SaveBrowserSettings(ctx, &inabav1.BrowserSettings{
		Defaults: &inabav1.BrowserProfile{Flags: []string{"--user-agent=host-default"}},
		Projects: map[string]*inabav1.BrowserProfile{proj.Id: {Sharing: inabav1.BrowserSharing_BROWSER_SHARING_PROJECT, Flags: []string{"user-agent=project-agent"}}},
	}); err != nil {
		t.Fatal(err)
	}
	one, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "idle", ProjectId: proj.Id})
	two, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "idle", ProjectId: proj.Id})
	a, err := s.open(ctx, one.Id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.open(ctx, two.Id)
	if err != nil || a != b {
		t.Fatalf("a project's threads share one browser: %v, same %v", err, a == b)
	}
	// The project's switch replaces the host's of the same name.
	ua, err := a.browser.MustPage("about:blank").Eval(`() => navigator.userAgent`)
	if err != nil || ua.Value.Str() != "project-agent" {
		t.Fatalf("the project's user-agent switch: %v %v", ua, err)
	}
	if _, err := h.DeleteThread(ctx, &inabav1.DeleteThreadRequest{ThreadId: one.Id}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if again, err := s.open(ctx, two.Id); err != nil || again != a {
		t.Fatalf("the shared browser after one thread went: %v, same %v", err, again == a)
	}
}

func TestStaleProfileLocksAreCleared(t *testing.T) {
	for name, c := range map[string]struct {
		holder string
		stays  bool
	}{
		"its browser is gone":    {"somehost-999999", false},
		"its browser still runs": {"somehost-" + strconv.Itoa(os.Getpid()), true},
	} {
		profile := t.TempDir()
		lock := filepath.Join(profile, "SingletonLock")
		os.Symlink(c.holder, lock)
		os.Symlink("123", filepath.Join(profile, "SingletonCookie"))
		clearStaleLock(profile)
		if _, err := os.Lstat(lock); (err == nil) != c.stays {
			t.Errorf("%s: lock left = %v", name, err == nil)
		}
	}
}

func TestAStoppedModuleStartsNoBrowser(t *testing.T) {
	if _, ok := launcher.LookPath(); !ok && os.Getenv("INABA_BROWSER") == "" {
		t.Skip("no Chrome or Chromium on this machine")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("no Node on this machine")
	}
	ctx := rpc.Trusted(context.Background())
	h := host.New(idleAgent{})
	t.Cleanup(h.Close)
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "idle", Cwd: t.TempDir()})
	s, err := newService(h, h, "", module.Store{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	s.stop()
	// A call that arrives as the module stops, as from a viewer still open, starts nothing.
	if _, err := s.OpenBrowser(ctx, &inabav1.OpenBrowserRequest{ThreadId: th.Id}); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("open after stop: %v", err)
	}
	if len(s.browsers) != 0 {
		t.Fatal("a browser started after the module stopped")
	}
}
