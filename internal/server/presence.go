package server

import (
	"context"
	"sort"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// presenceTTL is how long a browser counts as somewhere after its last heartbeat.
const presenceTTL = 30 * time.Second

// maxClientID bounds what a caller can make the server keep per heartbeat.
const maxClientID = 64

// shortcut: presence lives in this process's memory; a server restart forgets it for a
// heartbeat or two, and several servers behind one address would not share it.
type presence struct {
	userID, name, avatar, hostID, threadID, projectID, clientID string
	seen                                                        time.Time
	// passed is set once it hands back the thread's driver seat, until it asks for it again
	// or goes elsewhere: it is not waiting for the seat, so a lone watcher besides it takes it.
	passed bool
	// stealth is an administrator others are not shown.
	stealth bool
	// visible is the page on screen at the last heartbeat; only then does it hold back pushes.
	visible bool
}

func (s *Server) Heartbeat(ctx context.Context, req *inabav1.HeartbeatRequest) (*inabav1.HeartbeatResponse, error) {
	p := currentUser(ctx)
	if p == nil || p.user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "sign in to share where you are")
	}
	if req.ClientId == "" || len(req.ClientId) > maxClientID || len(req.ThreadId) > maxClientID || len(req.ProjectId) > maxClientID {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a heartbeat needs a client id of at most 64 characters")
	}
	if len(req.From) > maxClientID || len(req.GiveTo) > maxClientID {
		return nil, connect.NewError(connect.CodeInvalidArgument, "from is a client id of at most 64 characters")
	}
	// Only a host the caller can use: someone else's personal host shows no one, and takes no one.
	if req.HostId != "" {
		h, err := s.store.Host(req.HostId)
		if err != nil || s.placeOf(ctx, h) == "" {
			return nil, connect.NewError(connect.CodeNotFound, "no host "+req.HostId)
		}
	}
	now := time.Now()
	key := p.user.ID + "/" + req.ClientId
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.present == nil {
		s.present = map[string]*presence{}
	}
	for k, v := range s.present {
		if now.Sub(v.seen) > presenceTTL {
			delete(s.present, k)
		}
	}
	old := s.present[key]
	passed := (old != nil && old.passed && old.threadID == req.ThreadId && !req.Take) || req.Release
	s.present[key] = &presence{passed: passed, stealth: p.user.Admin && p.user.Stealth, userID: p.user.ID, name: displayName(p.user), avatar: avatarURL(p.user), hostID: req.HostId, threadID: req.ThreadId, projectID: req.ProjectId, clientID: req.ClientId, visible: req.Visible, seen: now}
	res := &inabav1.HeartbeatResponse{}
	if req.HostId == "" {
		return res, nil
	}
	if req.ThreadId != "" {
		res.Driver = s.seat(req, p.user.ID, p.user.Admin, now)
	}
	// One entry per person and place, however many browser tabs show it.
	seen := map[[3]string]*inabav1.Presence{}
	for k, v := range s.present {
		if v.hostID != req.HostId {
			continue
		}
		// An administrator in stealth is not there for anyone else.
		if v.stealth && v.userID != p.user.ID {
			continue
		}
		if req.ThreadId != "" && v.threadID == req.ThreadId && k != key && !v.passed {
			res.Clients = append(res.Clients, v.clientID)
		}
		// Others only: one's own tabs are not company.
		if v.userID == p.user.ID {
			continue
		}
		where := [3]string{v.userID, v.threadID, v.projectID}
		if e := seen[where]; e != nil {
			e.Clients = append(e.Clients, v.clientID)
			continue
		}
		e := &inabav1.Presence{UserId: v.userID, Name: v.name, AvatarUrl: v.avatar, ThreadId: v.threadID, ProjectId: v.projectID, SeenAt: timestamppb.New(v.seen), Clients: []string{v.clientID}}
		seen[where] = e
		res.Present = append(res.Present, e)
	}
	// By name, then place, so one person in two places comes in the same order every time.
	sort.Slice(res.Present, func(i, j int) bool {
		a, b := res.Present[i], res.Present[j]
		return a.Name+"\x00"+a.ThreadId+"\x00"+a.ProjectId < b.Name+"\x00"+b.ThreadId+"\x00"+b.ProjectId
	})
	return res, nil
}

// seat settles who drives the heartbeat's thread and says who it is. A holder no longer on
// the thread, having gone elsewhere or gone quiet, lets it go. Called with s.mu held, after
// the caller's own presence is recorded.
// shortcut: seats of threads no one comes back to stay in memory; a handful of strings each, until the server restarts.
func (s *Server) seat(req *inabav1.HeartbeatRequest, userID string, admin bool, now time.Time) string {
	if s.seats == nil {
		s.seats = map[string]string{}
	}
	k := req.HostId + "/" + req.ThreadId
	// Who, if anyone, a client on this thread is.
	on := func(client string) (string, bool) {
		for _, v := range s.present {
			if v.clientID == client && v.hostID == req.HostId && v.threadID == req.ThreadId && now.Sub(v.seen) <= presenceTTL {
				return v.userID, true
			}
		}
		return "", false
	}
	holder := s.seats[k]
	holderUser, here := on(holder)
	if holder != "" && !here {
		holder = ""
		delete(s.seats, k)
	}
	if req.Release && holder == req.ClientId {
		holder = ""
		delete(s.seats, k)
	}
	// From oneself in another tab, or by an administrator; someone else hands it over by releasing it.
	mayTake := holder == "" || holder == req.ClientId || holderUser == userID || admin
	if req.Take && mayTake && (holder == "" || holder == req.From || holder == req.ClientId) {
		holder = req.ClientId
		s.seats[k] = holder
	}
	if req.GiveTo != "" && admin && holder == req.From {
		if _, ok := on(req.GiveTo); ok {
			holder = req.GiveTo
			s.seats[k] = holder
		}
	}
	return holder
}
