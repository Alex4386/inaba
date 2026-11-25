package server

import (
	"context"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/store"
)

// account finds an account for an administrator's call, by id or password account name.
func (a admin) account(ctx context.Context, ref string) (*store.User, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	u, err := a.s.store.FindUser(ref)
	if err != nil {
		return nil, accountError(err, ref)
	}
	return u, nil
}

func (a admin) GetUserDetail(ctx context.Context, req *inabav1.GetUserDetailRequest) (*inabav1.UserDetail, error) {
	u, err := a.account(ctx, req.User)
	if err != nil {
		return nil, err
	}
	res := &inabav1.UserDetail{User: userMessage(u), CreatedAt: timestamppb.New(u.CreatedAt)}
	sessions, err := a.s.store.Tokens(store.KindSession, u.ID)
	if err != nil {
		return nil, internal("could not read the account's sign-ins", err)
	}
	for _, t := range sessions {
		if t.ExpiresAt == nil || t.ExpiresAt.After(time.Now()) {
			res.Sessions++
		}
	}
	passkeys, err := a.s.store.Passkeys(u.ID)
	if err != nil {
		return nil, internal("could not read the account's passkeys", err)
	}
	res.Passkeys = int32(len(passkeys))
	hosts, err := a.s.store.Hosts()
	if err != nil {
		return nil, internal("could not read the hosts", err)
	}
	for i := range hosts {
		h := &hosts[i]
		m := a.s.hostMessage(h)
		m.OwnerName = a.s.ownerName(h)
		m.OwnerId = h.Owner
		m.Restricted = h.Restricted
		res.Hosts = append(res.Hosts, &inabav1.HostAccess{Host: m, Role: a.s.roleOf(u.ID, h)})
	}
	return res, nil
}

func (a admin) EndUserSessions(ctx context.Context, req *inabav1.EndUserSessionsRequest) (*inabav1.EndUserSessionsResponse, error) {
	u, err := a.account(ctx, req.User)
	if err != nil {
		return nil, err
	}
	sessions, err := a.s.store.Tokens(store.KindSession, u.ID)
	if err != nil {
		return nil, internal("could not read the account's sign-ins", err)
	}
	for _, t := range sessions {
		if err := a.s.store.DeleteToken(t.ID, u.ID); err != nil {
			return nil, internal("could not sign the account out", err)
		}
	}
	return &inabav1.EndUserSessionsResponse{}, nil
}

func (a admin) DisableUserTotp(ctx context.Context, req *inabav1.DisableUserTotpRequest) (*inabav1.User, error) {
	u, err := a.account(ctx, req.User)
	if err != nil {
		return nil, err
	}
	if u.TOTPSecret == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "two-step sign-in is off for this account already")
	}
	if u, err = a.s.store.UpdateUser(u.ID, map[string]any{"totp_secret": "", "totp_pending": ""}); err != nil {
		return nil, internal("could not turn off two-step sign-in", err)
	}
	return userMessage(u), nil
}
