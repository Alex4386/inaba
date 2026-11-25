package server

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/store"
)

// A machine signs in as a host with the device authorization grant (RFC 8628): it
// asks for a code without an account, a signed-in user approves the code in the
// browser, and the machine's polling then picks up a refresh token.

const (
	loginTTL      = 10 * time.Minute
	loginInterval = 3 * time.Second
	// maxLogins bounds what callers without an account can make the server hold.
	maxLogins = 200
	// accessTTL is how long a host's access token lets it connect.
	accessTTL = time.Hour
	// codeLetters leaves out vowels, so codes spell no words, and look-alikes.
	codeLetters = "BCDFGHJKLMNPQRSTVWXZ"
)

// shortcut: pending logins live in memory and a server restart drops them; the host
// then starts over. Keep them in the store if logins have to survive restarts.
type hostLogin struct {
	userCode, deviceCode, name, address string
	expires                             time.Time
	lastPoll                            time.Time
	denied                              bool
	// Set once approved, until the host picks them up.
	host   *store.Host
	secret string
}

func newUserCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = codeLetters[int(b[i])%len(codeLetters)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// normalizeCode accepts a code as a person types it: any case, with or without the dash.
func normalizeCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if strings.ContainsRune(codeLetters, r) {
			b.WriteRune(r)
		}
	}
	if b.Len() != 8 {
		return ""
	}
	s := b.String()
	return s[:4] + "-" + s[4:]
}

// pruneLogins drops expired logins. The caller holds mu.
func (s *Server) pruneLogins(now time.Time) {
	for code, l := range s.logins {
		if now.After(l.expires) {
			delete(s.logins, code)
		}
	}
}

func (s *Server) StartHostLogin(ctx context.Context, req *inabav1.StartHostLoginRequest) (*inabav1.StartHostLoginResponse, error) {
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		name = name[:64]
	}
	address := ""
	if ip := callerIP(ctx); ip.IsValid() {
		address = ip.String()
	}
	now := time.Now()
	l := &hostLogin{userCode: newUserCode(), deviceCode: "inaba_login_" + store.RandomPassword() + store.RandomPassword(), name: name, address: address, expires: now.Add(loginTTL)}
	s.mu.Lock()
	if s.logins == nil {
		s.logins = map[string]*hostLogin{}
	}
	s.pruneLogins(now)
	if len(s.logins) >= maxLogins {
		s.mu.Unlock()
		return nil, connect.NewError(connect.CodeResourceExhausted, "too many sign-ins are waiting; try again in a few minutes")
	}
	for s.logins[l.userCode] != nil {
		l.userCode = newUserCode()
	}
	s.logins[l.userCode] = l
	s.mu.Unlock()
	uri := strings.TrimSuffix(s.cfg.PublicURL, "/") + "/ui/device"
	return &inabav1.StartHostLoginResponse{
		DeviceCode: l.deviceCode, UserCode: l.userCode,
		VerificationUri: uri, VerificationUriComplete: uri + "?code=" + l.userCode,
		IntervalSeconds: int32(loginInterval / time.Second), ExpiresAt: timestamppb.New(l.expires),
	}, nil
}

func (s *Server) PollHostLogin(_ context.Context, req *inabav1.PollHostLoginRequest) (*inabav1.PollHostLoginResponse, error) {
	res, host, err := s.pollLogin(req.DeviceCode)
	if host != nil {
		// hostMessage takes mu, so the host is described once it is let go.
		res.Host = s.hostMessage(host)
	}
	return res, err
}

func (s *Server) pollLogin(deviceCode string) (*inabav1.PollHostLoginResponse, *store.Host, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLogins(now)
	var l *hostLogin
	for _, x := range s.logins {
		if x.deviceCode == deviceCode && deviceCode != "" {
			l = x
		}
	}
	switch {
	case l == nil:
		return nil, nil, connect.NewError(connect.CodeNotFound, "this sign-in expired or was never started; run inaba login again")
	case l.denied:
		delete(s.logins, l.userCode)
		return nil, nil, connect.NewError(connect.CodePermissionDenied, "the sign-in was denied")
	case l.host == nil:
		// Polling faster than asked earns no answer sooner.
		if now.Sub(l.lastPoll) < loginInterval/2 {
			return nil, nil, connect.NewError(connect.CodeResourceExhausted, "slow down")
		}
		l.lastPoll = now
		return &inabav1.PollHostLoginResponse{Pending: true}, nil, nil
	}
	// The refresh token is handed over once.
	delete(s.logins, l.userCode)
	return &inabav1.PollHostLoginResponse{RefreshToken: l.secret}, l.host, nil
}

// pendingLogin finds a login still waiting for a user. The caller holds mu.
func (s *Server) pendingLogin(code string) (*hostLogin, error) {
	s.pruneLogins(time.Now())
	l := s.logins[normalizeCode(code)]
	if l == nil || l.denied || l.host != nil {
		return nil, connect.NewError(connect.CodeNotFound, "no sign-in is waiting with that code; it may have expired")
	}
	return l, nil
}

func (s *Server) GetHostLogin(ctx context.Context, req *inabav1.GetHostLoginRequest) (*inabav1.HostLogin, error) {
	if userID(ctx) == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "sign in to approve a host")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.pendingLogin(req.UserCode)
	if err != nil {
		return nil, err
	}
	return &inabav1.HostLogin{UserCode: l.userCode, Name: l.name, Address: l.address, ExpiresAt: timestamppb.New(l.expires)}, nil
}

func (s *Server) ApproveHostLogin(ctx context.Context, req *inabav1.ApproveHostLoginRequest) (*inabav1.Host, error) {
	owner := userID(ctx)
	if owner == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "sign in to approve a host")
	}
	if req.Shared {
		if err := require(ctx, store.PermSharedHosts); err != nil {
			return nil, err
		}
		owner = ""
	}
	s.mu.Lock()
	l, err := s.pendingLogin(req.UserCode)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = l.name
	}
	if name == "" {
		s.mu.Unlock()
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	h, secret, err := s.store.AddHost(name, owner)
	if err != nil {
		s.mu.Unlock()
		return nil, internal("could not save the host", err)
	}
	l.host, l.secret = h, secret
	s.mu.Unlock()
	return s.hostMessage(h), nil
}

func (s *Server) DenyHostLogin(ctx context.Context, req *inabav1.DenyHostLoginRequest) (*inabav1.DenyHostLoginResponse, error) {
	if userID(ctx) == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "sign in to deny a host")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.pendingLogin(req.UserCode)
	if err != nil {
		return nil, err
	}
	l.denied = true
	return &inabav1.DenyHostLoginResponse{}, nil
}

func (s *Server) RefreshHostToken(_ context.Context, req *inabav1.RefreshHostTokenRequest) (*inabav1.RefreshHostTokenResponse, error) {
	t, err := s.store.LookupToken(req.RefreshToken)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.Kind != store.KindHost) {
		return nil, connect.NewError(connect.CodeUnauthenticated, "unknown refresh token; the host may have been removed. Run inaba login again")
	}
	if err != nil {
		return nil, internal("could not read the token", err)
	}
	access, secret, err := s.store.NewToken(store.KindHostAccess, t.Owner, "", accessTTL)
	if err != nil {
		return nil, internal("could not save the token", err)
	}
	return &inabav1.RefreshHostTokenResponse{AccessToken: secret, ExpiresAt: timestamppb.New(*access.ExpiresAt)}, nil
}
