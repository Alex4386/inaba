package server

import (
	"context"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/store"
)

// inviteTTL is how long an invite link to a personal host works.
const inviteTTL = 7 * 24 * time.Hour

// Places on a host besides store.RoleMember and store.RoleManager.
const (
	placeOwner    = "owner"
	placeAdmin    = "admin"
	placeEveryone = "everyone"
)

// roleOf is a user's own place on a host, an administrator's rights aside: its owner, a member or
// manager they were made, everyone's on an open shared host, or "" for none.
func (s *Server) roleOf(userID string, h *store.Host) string {
	switch {
	case userID != "" && h.Owner == userID:
		return placeOwner
	}
	if role := s.store.MemberRole(h.ID, userID); role != "" {
		return role
	}
	if h.Owner == "" && !h.Restricted {
		return placeEveryone
	}
	return ""
}

// placeOf is the caller's place on a host, administrators and the control socket being admitted
// everywhere; "" means the host is not theirs to reach.
func (s *Server) placeOf(ctx context.Context, h *store.Host) string {
	if ctx.Value(trustedKey{}) != nil {
		return placeAdmin
	}
	p := currentUser(ctx)
	if p == nil || p.user == nil {
		return ""
	}
	if role := s.roleOf(p.user.ID, h); role == placeOwner {
		return role
	} else if p.user.Admin {
		return placeAdmin
	} else {
		return role
	}
}

// grantsOn is what a caller may do on a host, told to the host on each call. An owner or an
// administrator may do anything, restarting it included. A member or manager may do everything on the host itself; on
// someone's personal host that is all, while on a shared host the account's own permissions come too.
func (s *Server) grantsOn(ctx context.Context, h *store.Host, u *store.User) []string {
	granted := s.hostGrants(ctx, h, u)
	// Hosts from before the split know host settings and housekeeping together by the old name.
	if slices.Contains(granted, store.PermHostSettings) && slices.Contains(granted, store.PermHousekeeping) {
		granted = append(granted, rpc.PermHosts)
	}
	// Restarting is for whoever may take the host away (removable): its owner, an administrator,
	// and on a shared host those who may add shared hosts. Its members and managers work in it,
	// and a restart would end everyone's running turns.
	if p := s.placeOf(ctx, h); p == placeOwner || p == placeAdmin || h.Owner == "" && slices.Contains(u.Granted(), store.PermSharedHosts) {
		granted = append(granted, rpc.PermRestart)
	}
	return granted
}

func (s *Server) hostGrants(ctx context.Context, h *store.Host, u *store.User) []string {
	place := s.placeOf(ctx, h)
	if place == placeOwner || place == placeAdmin {
		return slices.Clone(store.AllPermissions)
	}
	var granted []string
	if h.Owner == "" {
		granted = slices.Clone(u.Granted())
	}
	add := func(ps ...string) {
		for _, p := range ps {
			if !slices.Contains(granted, p) {
				granted = append(granted, p)
			}
		}
	}
	// A member can use the host's terminal anyway, so holding back any part of the host from them
	// would protect nothing; a manager differs only in looking after members and invites (ownHost).
	if place == store.RoleManager || place == store.RoleMember {
		add(store.PermProjects, store.PermThreadsAnywhere, store.PermAutomations, store.PermHostSettings, store.PermHousekeeping)
	}
	return granted
}

// ownHost returns a host whose members and invites the caller looks after: as its owner, its
// manager, or an administrator. One they cannot reach is answered as missing.
func (s *Server) ownHost(ctx context.Context, id string) (*store.Host, error) {
	h, err := s.store.Host(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && s.placeOf(ctx, h) == "") {
		return nil, connect.NewError(connect.CodeNotFound, "no host "+id)
	}
	if err != nil {
		return nil, internal("could not read the host", err)
	}
	switch s.placeOf(ctx, h) {
	case placeOwner, placeAdmin, store.RoleManager:
		return h, nil
	}
	return nil, connect.NewError(connect.CodePermissionDenied, "only the host's owner or a manager lets people into it")
}

func (s *Server) SetHostMember(ctx context.Context, req *inabav1.SetHostMemberRequest) (*inabav1.SetHostMemberResponse, error) {
	h, err := s.ownHost(ctx, req.HostId)
	if err != nil {
		return nil, err
	}
	u, err := s.store.User(req.UserId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "no account "+req.UserId)
	}
	if u.ID == h.Owner {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the owner has the host already")
	}
	switch req.Role {
	case "none":
		if err := s.store.RemoveMember(h.ID, u.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, internal("could not take the account out", err)
		}
	case store.RoleMember, store.RoleManager:
		if err := s.store.SetMember(h.ID, u.ID, req.Role); err != nil {
			return nil, internal("could not let the account in", err)
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, `a role is "member", "manager" or "none"`)
	}
	if req.Role != store.RoleManager {
		if err := s.dropInvitesBy(h.ID, u.ID); err != nil {
			return nil, err
		}
	}
	return &inabav1.SetHostMemberResponse{}, nil
}

func (s *Server) CreateHostInvite(ctx context.Context, req *inabav1.CreateHostInviteRequest) (*inabav1.CreateHostInviteResponse, error) {
	h, err := s.ownHost(ctx, req.HostId)
	if err != nil {
		return nil, err
	}
	role := req.Role
	switch role {
	case "":
		role = store.RoleMember
	case store.RoleMember, store.RoleManager:
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, `an invite grants "member" or "manager"`)
	}
	if utf8.RuneCountInString(req.Note) > 200 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "keep the note under 200 characters")
	}
	t, secret, err := s.store.NewInvite(h.ID, userID(ctx), role, req.Note, inviteTTL)
	if err != nil {
		return nil, internal("could not make the invite", err)
	}
	return &inabav1.CreateHostInviteResponse{Token: secret, ExpiresAt: timestamppb.New(*t.ExpiresAt)}, nil
}

func (s *Server) RevokeHostInvites(ctx context.Context, req *inabav1.RevokeHostInvitesRequest) (*inabav1.RevokeHostInvitesResponse, error) {
	h, err := s.ownHost(ctx, req.HostId)
	if err != nil {
		return nil, err
	}
	invites, err := s.store.Tokens(store.KindHostInvite, h.ID)
	if err != nil {
		return nil, internal("could not read the invites", err)
	}
	for _, t := range invites {
		if err := s.store.DeleteInvite(h.ID, t.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, internal("could not end the invite", err)
		}
	}
	return &inabav1.RevokeHostInvitesResponse{}, nil
}

func (s *Server) ListHostInvites(ctx context.Context, req *inabav1.ListHostInvitesRequest) (*inabav1.ListHostInvitesResponse, error) {
	h, err := s.ownHost(ctx, req.HostId)
	if err != nil {
		return nil, err
	}
	invites, err := s.store.Tokens(store.KindHostInvite, h.ID)
	if err != nil {
		return nil, internal("could not read the invites", err)
	}
	res := &inabav1.ListHostInvitesResponse{}
	for _, t := range invites {
		if t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now()) {
			continue
		}
		m := &inabav1.PendingHostInvite{Id: t.ID, Role: inviteRole(&t), Note: t.Note, CreatedAt: timestamppb.New(t.CreatedAt)}
		if t.ExpiresAt != nil {
			m.ExpiresAt = timestamppb.New(*t.ExpiresAt)
		}
		if u, err := s.store.User(t.Name); err == nil {
			m.CreatedBy = displayName(u)
		}
		res.Invites = append(res.Invites, m)
	}
	return res, nil
}

func (s *Server) RevokeHostInvite(ctx context.Context, req *inabav1.RevokeHostInviteRequest) (*inabav1.RevokeHostInviteResponse, error) {
	h, err := s.ownHost(ctx, req.HostId)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteInvite(h.ID, req.Id); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no such invite on this host")
	} else if err != nil {
		return nil, internal("could not end the invite", err)
	}
	return &inabav1.RevokeHostInviteResponse{}, nil
}

// dropInvitesBy ends the invites someone made into a host, once they no longer look after it, so a
// manager who was taken out cannot let themselves back in with a link of their own.
func (s *Server) dropInvitesBy(hostID, userID string) error {
	invites, err := s.store.Tokens(store.KindHostInvite, hostID)
	if err != nil {
		return internal("could not read the invites", err)
	}
	for _, t := range invites {
		if t.Name != userID {
			continue
		}
		if err := s.store.DeleteInvite(hostID, t.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return internal("could not end the invite", err)
		}
	}
	return nil
}

// inviteRole is the role an invite grants; invites from before roles grant membership.
func inviteRole(t *store.Token) string {
	if t.Role == "" {
		return store.RoleMember
	}
	return t.Role
}

// invite finds the host an invite secret is for, and the role it grants.
func (s *Server) invite(ctx context.Context, secret string) (*store.Host, *store.User, string, error) {
	p := currentUser(ctx)
	if p == nil || p.user == nil {
		return nil, nil, "", connect.NewError(connect.CodeUnauthenticated, "sign in to open an invite")
	}
	t, err := s.store.LookupToken(secret)
	if err != nil || t.Kind != store.KindHostInvite {
		return nil, nil, "", connect.NewError(connect.CodeNotFound, "this invite has ended or never was; ask for a new one")
	}
	h, err := s.store.Host(t.Owner)
	if err != nil {
		return nil, nil, "", connect.NewError(connect.CodeNotFound, "the host this invite was for is gone")
	}
	role := inviteRole(t)
	// Invites are dropped when their maker stops looking after the host; this catches any left
	// behind (a deleted account, an administrator no longer one), so a link only works while
	// whoever made it could still make it. Members get the host's full rights, so this holds
	// for every role. An invite with no maker came from the control socket, which answers to
	// the server's operator.
	if t.Name != "" {
		maker, err := s.store.User(t.Name)
		if err != nil || (!maker.Admin && !slices.Contains([]string{placeOwner, store.RoleManager}, s.roleOf(maker.ID, h))) {
			return nil, nil, "", connect.NewError(connect.CodeNotFound, "this invite has ended or never was; ask for a new one")
		}
	}
	return h, p.user, role, nil
}

func (s *Server) GetHostInvite(ctx context.Context, req *inabav1.GetHostInviteRequest) (*inabav1.HostInvite, error) {
	h, u, role, err := s.invite(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	return &inabav1.HostInvite{HostId: h.ID, HostName: h.Name, OwnerName: s.ownerName(h), Joined: h.Owner == u.ID || s.store.IsMember(h.ID, u.ID), Role: role}, nil
}

func (s *Server) AcceptHostInvite(ctx context.Context, req *inabav1.AcceptHostInviteRequest) (*inabav1.Host, error) {
	h, u, role, err := s.invite(ctx, req.Token)
	if err != nil {
		return nil, err
	}
	// A manager opening a member's invite stays a manager; an invite only ever raises one's role.
	if h.Owner != u.ID && s.store.MemberRole(h.ID, u.ID) != store.RoleManager {
		if err := s.store.SetMember(h.ID, u.ID, role); err != nil {
			return nil, internal("could not join the host", err)
		}
	}
	return s.hostFor(ctx, h), nil
}

func (s *Server) ListHostMembers(ctx context.Context, req *inabav1.ListHostMembersRequest) (*inabav1.ListHostMembersResponse, error) {
	h, err := s.ownHost(ctx, req.HostId)
	if err != nil {
		return nil, err
	}
	members, err := s.store.Members(h.ID)
	if err != nil {
		return nil, internal("could not read the members", err)
	}
	res := &inabav1.ListHostMembersResponse{}
	for _, m := range members {
		u, err := s.store.User(m.UserID)
		if err != nil {
			continue
		}
		role := m.Role
		if role == "" {
			role = store.RoleMember
		}
		res.Members = append(res.Members, &inabav1.HostMember{UserId: u.ID, Name: displayName(u), AvatarUrl: avatarURL(u), JoinedAt: timestamppb.New(m.CreatedAt), Role: role})
	}
	return res, nil
}

func (s *Server) RemoveHostMember(ctx context.Context, req *inabav1.RemoveHostMemberRequest) (*inabav1.RemoveHostMemberResponse, error) {
	h, err := s.ownHost(ctx, req.HostId)
	if err != nil {
		return nil, err
	}
	if err := s.store.RemoveMember(h.ID, req.UserId); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "not a member of this host")
	} else if err != nil {
		return nil, internal("could not remove the member", err)
	}
	if err := s.dropInvitesBy(h.ID, req.UserId); err != nil {
		return nil, err
	}
	return &inabav1.RemoveHostMemberResponse{}, nil
}

// ownerName is whose personal host it is, by name; "" for a shared one.
func (s *Server) ownerName(h *store.Host) string {
	if h.Owner == "" {
		return ""
	}
	if u, err := s.store.User(h.Owner); err == nil {
		return displayName(u)
	}
	return ""
}

// hostFor describes a host as the caller sees it: whether they were invited into it, and whose it is.
func (s *Server) hostFor(ctx context.Context, h *store.Host) *inabav1.Host {
	m := s.hostMessage(h)
	m.OwnerName = s.ownerName(h)
	m.OwnerId = h.Owner
	m.Restricted = h.Restricted
	m.Role = s.roleOf(userID(ctx), h)
	if m.Role == placeEveryone {
		m.Role = ""
	}
	m.Invited = h.Owner != "" && (m.Role == store.RoleMember || m.Role == store.RoleManager)
	return m
}
