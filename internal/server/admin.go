package server

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/store"
)

// AdminServer is what the local control socket serves: account administration and
// the host registry, with every call trusted. Never mount it on a network listener.
func (s *Server) AdminServer() *connect.Server {
	server := connect.NewServer(trust)
	inabav1connect.RegisterAdminServiceHandler(server, admin{s})
	inabav1connect.RegisterHostServiceHandler(server, s)
	return server
}

type admin struct{ s *Server }

var permissionNames = map[inabav1.Permission]string{
	inabav1.Permission_PERMISSION_MANAGE_HOSTS:     store.PermSharedHosts,
	inabav1.Permission_PERMISSION_MANAGE_PROJECTS:  store.PermProjects,
	inabav1.Permission_PERMISSION_THREADS_ANYWHERE: store.PermThreadsAnywhere,
	inabav1.Permission_PERMISSION_AUTOMATIONS:      store.PermAutomations,
	inabav1.Permission_PERMISSION_HOST_SETTINGS:    store.PermHostSettings,
	inabav1.Permission_PERMISSION_HOUSEKEEPING:     store.PermHousekeeping,
}

// avatarURL is where a user's picture is served, versioned so a browser may keep it for good.
func avatarURL(u *store.User) string {
	if u.AvatarVersion == 0 {
		return ""
	}
	return "/v1/avatars/" + u.ID + "?v=" + strconv.FormatInt(u.AvatarVersion, 36)
}

func userMessage(u *store.User) *inabav1.User {
	m := &inabav1.User{Id: u.ID, Name: u.Name, Oidc: u.OIDCSubject != "", Admin: u.Admin, AvatarUrl: avatarURL(u), DisplayName: u.DisplayName, Totp: u.TOTPSecret != "", Stealth: u.Admin && u.Stealth}
	for permission, name := range permissionNames {
		if u.Can(name) {
			m.Permissions = append(m.Permissions, permission)
		}
	}
	slices.Sort(m.Permissions)
	return m
}

// accountError maps the store's refusals about an account to what the caller is told.
func accountError(err error, ref string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, "no account "+ref)
	case errors.Is(err, store.ErrLastAdmin):
		return connect.NewError(connect.CodeFailedPrecondition, "this is the last administrator; make another account an administrator first")
	}
	return internal("could not save the change", err)
}

// permissionList names the permissions for the store, refusing ones it does not know.
func permissionList(in []inabav1.Permission) ([]string, error) {
	var out []string
	for _, p := range in {
		name, ok := permissionNames[p]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, "unknown permission")
		}
		out = append(out, name)
	}
	return out, nil
}

func (a admin) SetAccess(ctx context.Context, req *inabav1.SetAccessRequest) (*inabav1.User, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	permissions, err := permissionList(req.Permissions)
	if err != nil {
		return nil, err
	}
	u, err := a.s.store.SetAccess(req.User, req.Admin, permissions)
	if err != nil {
		return nil, accountError(err, req.User)
	}
	return userMessage(u), nil
}

func (a admin) ListUsers(ctx context.Context, _ *inabav1.ListUsersRequest) (*inabav1.ListUsersResponse, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	users, err := a.s.store.Users()
	if err != nil {
		return nil, internal("could not read the accounts", err)
	}
	res := &inabav1.ListUsersResponse{}
	for i := range users {
		res.Users = append(res.Users, userMessage(&users[i]))
	}
	return res, nil
}

func (a admin) CreateUser(ctx context.Context, req *inabav1.CreateUserRequest) (*inabav1.User, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if req.Name == "" || req.Password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name and password are required")
	}
	permissions, err := permissionList(req.Permissions)
	if err != nil {
		return nil, err
	}
	u, err := a.s.store.AddLocalUser(req.Name, req.Password, req.Admin, permissions)
	if errors.Is(err, store.ErrExists) {
		return nil, connect.NewError(connect.CodeAlreadyExists, "there is already an account named "+req.Name)
	}
	if err != nil {
		return nil, internal("could not save the account", err)
	}
	return userMessage(u), nil
}

func (a admin) SetPassword(ctx context.Context, req *inabav1.SetPasswordRequest) (*inabav1.SetPasswordResponse, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if req.Password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "password is required")
	}
	err := a.s.store.SetPassword(req.User, req.Password)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no password account "+req.User)
	}
	if err != nil {
		return nil, internal("could not save the password", err)
	}
	return &inabav1.SetPasswordResponse{}, nil
}

func (a admin) DeleteUser(ctx context.Context, req *inabav1.DeleteUserRequest) (*inabav1.DeleteUserResponse, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := a.s.store.DeleteUser(req.User); err != nil {
		return nil, accountError(err, req.User)
	}
	return &inabav1.DeleteUserResponse{}, nil
}

func (a admin) ListUserTokens(ctx context.Context, _ *inabav1.ListUserTokensRequest) (*inabav1.ListUserTokensResponse, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	tokens, err := a.s.store.Tokens(store.KindAPI, "")
	if err != nil {
		return nil, internal("could not read the tokens", err)
	}
	res := &inabav1.ListUserTokensResponse{}
	for i := range tokens {
		entry := &inabav1.ListUserTokensResponse_Entry{Token: tokenMessage(&tokens[i])}
		if u, err := a.s.store.User(tokens[i].Owner); err == nil {
			entry.UserName = u.Name
		}
		res.Tokens = append(res.Tokens, entry)
	}
	return res, nil
}

func (a admin) CreateUserToken(ctx context.Context, req *inabav1.CreateUserTokenRequest) (*inabav1.CreateTokenResponse, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	u, err := a.s.store.FindUser(req.User)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no account "+req.User)
	}
	if err != nil {
		return nil, internal("could not read the account", err)
	}
	t, secret, err := a.s.store.NewToken(store.KindAPI, u.ID, req.Name, 0)
	if err != nil {
		return nil, internal("could not save the token", err)
	}
	return &inabav1.CreateTokenResponse{Token: tokenMessage(t), Secret: secret}, nil
}

func (a admin) RevokeUserToken(ctx context.Context, req *inabav1.RevokeTokenRequest) (*inabav1.RevokeTokenResponse, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return &inabav1.RevokeTokenResponse{}, a.s.deleteToken(req.Id, "")
}

func (a admin) GetServerHousekeeping(ctx context.Context, _ *inabav1.GetServerHousekeepingRequest) (*inabav1.ServerHousekeeping, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return a.housekeeping()
}

func (a admin) CleanUpServer(ctx context.Context, req *inabav1.CleanUpServerRequest) (*inabav1.ServerHousekeeping, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if req.ExpiredTokens {
		if err := a.s.store.DeleteExpiredTokens(); err != nil {
			return nil, internal("could not delete the expired tokens", err)
		}
	}
	if req.CompactDatabase {
		if err := a.s.store.Compact(); err != nil {
			return nil, internal("could not compact the database", err)
		}
	}
	return a.housekeeping()
}

func (a admin) housekeeping() (*inabav1.ServerHousekeeping, error) {
	u, err := a.s.store.Upkeep()
	if err != nil {
		return nil, internal("could not read the database", err)
	}
	return &inabav1.ServerHousekeeping{DatabaseBytes: u.DatabaseBytes, ExpiredTokens: uint32(u.ExpiredTokens), ActiveSignIns: uint32(u.ActiveSignIns)}, nil
}
