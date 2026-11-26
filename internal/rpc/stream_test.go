package rpc

import (
	"testing"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

func TestFillFromURL(t *testing.T) {
	req := &inabav1.AttachShellRequest{}
	// Proto names and JSON names both work; the last of repeated values wins.
	if err := fill(req, map[string][]string{"thread_id": {"t"}, "shellId": {"s"}, "cols": {"1", "120"}}); err != nil || req.ThreadId != "t" || req.ShellId != "s" || req.Cols != 120 {
		t.Fatalf("fill: %v %v", req, err)
	}
	for _, bad := range []map[string][]string{{"nope": {"x"}}, {"cols": {"-1"}}, {"cols": {"wide"}}} {
		if err := fill(&inabav1.AttachShellRequest{}, bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestHTTPStatus(t *testing.T) {
	for code, want := range map[connect.Code]int{connect.CodeNotFound: 404, connect.CodePermissionDenied: 403, connect.CodeInvalidArgument: 400, connect.CodeInternal: 500} {
		if got := httpStatus(code); got != want {
			t.Errorf("%v: %d", code, got)
		}
	}
}
