package server

import (
	"context"
	"math"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// Read marks for unread threads. The host knows no accounts, so who read what lives here,
// as ids and a seq; the web compares the seq with the thread's activity_seq from the host.
// shortcut: a deleted thread's marks stay until its host or the account goes (only ids, and they mark nothing); drop them where deleted threads' snoozes are dropped if the table grows.

func (s *Server) MarkThreadSeen(ctx context.Context, req *inabav1.MarkThreadSeenRequest) (*inabav1.ThreadSeen, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.reachable(ctx, req.HostId); err != nil {
		return nil, err
	}
	// The id is the host's, which the server does not check; a wrong one only marks nothing.
	if req.ThreadId == "" || len(req.ThreadId) > maxTargetID {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a thread id is 1 to 64 characters")
	}
	// SQLite keeps signed 64-bit integers; a larger seq is no real event number.
	// shortcut: any seq up to that is trusted and only moves forward, so a buggy client can keep a thread read; compare with the host's last seq if that bites.
	if req.Seq > math.MaxInt64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "that is not an event number")
	}
	seq, err := s.store.MarkSeen(uid, req.HostId, req.ThreadId, req.Seq)
	if err != nil {
		return nil, internal("could not save the read mark", err)
	}
	return &inabav1.ThreadSeen{ThreadId: req.ThreadId, Seq: seq}, nil
}

func (s *Server) ListThreadSeen(ctx context.Context, req *inabav1.ListThreadSeenRequest) (*inabav1.ListThreadSeenResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.reachable(ctx, req.HostId); err != nil {
		return nil, err
	}
	seen, err := s.store.Seen(uid, req.HostId)
	if err != nil {
		return nil, internal("could not read the read marks", err)
	}
	res := &inabav1.ListThreadSeenResponse{}
	for _, m := range seen {
		res.Threads = append(res.Threads, &inabav1.ThreadSeen{ThreadId: m.ThreadID, Seq: m.Seq})
	}
	return res, nil
}
