package browser

import (
	"context"
	"testing"
	"time"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// The agent takes a free browser by using it, waits while a person has it, and lets go when done;
// a person who took it and stopped watching gives it back after a while.
func TestControlChangesHands(t *testing.T) {
	defer func(was time.Duration) { unwatchedGrace = was }(unwatchedGrace)
	unwatchedGrace = 20 * time.Millisecond
	in := &instance{}
	ctx := context.Background()
	if err := in.agentActs(ctx, "t1"); err != nil || in.controlNow() != inabav1.BrowserControl_BROWSER_CONTROL_AGENT {
		t.Fatalf("the agent's command: %v, %v", err, in.controlNow())
	}
	if in.personMay() == nil {
		t.Fatal("a person may act while the agent works")
	}
	// Another thread's turn ending is not this agent's.
	in.agentDone("t2")
	if in.controlNow() != inabav1.BrowserControl_BROWSER_CONTROL_AGENT {
		t.Fatal("another thread let go of the browser")
	}

	in.view(1)
	in.mu.Lock()
	in.setControl(inabav1.BrowserControl_BROWSER_CONTROL_USER)
	in.mu.Unlock()
	acted := make(chan error, 1)
	go func() { acted <- in.agentActs(ctx, "t1") }()
	select {
	case <-acted:
		t.Fatal("the agent acted while a person had the browser")
	case <-time.After(50 * time.Millisecond):
	}
	// The viewer reconnecting keeps it; leaving gives it back, and the agent goes on.
	in.view(-1)
	in.view(1)
	time.Sleep(60 * time.Millisecond)
	if in.controlNow() != inabav1.BrowserControl_BROWSER_CONTROL_USER {
		t.Fatal("a reconnecting viewer lost the browser")
	}
	in.view(-1)
	select {
	case err := <-acted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the browser was never given back with no one watching")
	}
	in.agentDone("t1")
	if in.controlNow() != inabav1.BrowserControl_BROWSER_CONTROL_UNSPECIFIED {
		t.Fatalf("after the agent is done: %v", in.controlNow())
	}

	// A waiting agent whose connection ends stops waiting.
	in.mu.Lock()
	in.setControl(inabav1.BrowserControl_BROWSER_CONTROL_USER)
	in.mu.Unlock()
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if in.agentActs(gone, "t1") == nil {
		t.Fatal("an agent that hung up went on")
	}
}

// What Playwright sends by itself as it attaches to a target passes; what acts on a page takes the browser.
func TestPassiveCommands(t *testing.T) {
	for method, want := range map[string]bool{
		"Runtime.runIfWaitingForDebugger": true, "Page.enable": true, "Runtime.enable": true, "Target.setAutoAttach": true,
		"Target.getTargets": true, "Network.setExtraHTTPHeaders": true, "Page.addScriptToEvaluateOnNewDocument": true,
		"Input.dispatchMouseEvent": false, "Page.navigate": false, "Runtime.evaluate": false, "Runtime.callFunctionOn": false,
		"Target.createTarget": false, "Target.closeTarget": false, "Page.captureScreenshot": false,
	} {
		if passive(method) != want {
			t.Errorf("passive(%q) = %v", method, !want)
		}
	}
}
