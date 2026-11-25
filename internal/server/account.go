package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/store"
)

// signedIn is the caller's account, or an error saying to sign in.
func signedIn(ctx context.Context) (*principal, error) {
	p := currentUser(ctx)
	if p == nil || p.user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "sign in first")
	}
	return p, nil
}

func (s *Server) ChangePassword(ctx context.Context, req *inabav1.ChangePasswordRequest) (*inabav1.ChangePasswordResponse, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if p.user.PasswordHash == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this account signs in through its identity provider, which keeps its password")
	}
	if bcrypt.CompareHashAndPassword([]byte(p.user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, "the current password is wrong")
	}
	if len(req.NewPassword) < 8 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a password is at least 8 characters")
	}
	keep := ""
	if p.token != nil {
		keep = p.token.ID
	}
	if err := s.store.ChangePassword(p.user.ID, req.NewPassword, keep); err != nil {
		return nil, internal("could not save the password", err)
	}
	return &inabav1.ChangePasswordResponse{}, nil
}

func (s *Server) BeginTotp(ctx context.Context, _ *inabav1.BeginTotpRequest) (*inabav1.BeginTotpResponse, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "inaba", AccountName: p.user.Name})
	if err != nil {
		return nil, internal("could not make a secret", err)
	}
	if _, err := s.store.UpdateUser(p.user.ID, map[string]any{"totp_pending": key.Secret()}); err != nil {
		return nil, internal("could not save the secret", err)
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return nil, internal("could not draw the code", err)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, internal("could not draw the code", err)
	}
	return &inabav1.BeginTotpResponse{Secret: key.Secret(), Url: key.URL(), QrCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())}, nil
}

func (s *Server) EnableTotp(ctx context.Context, req *inabav1.EnableTotpRequest) (*inabav1.User, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if p.user.TOTPPending == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "start setting up two-step sign-in first")
	}
	if err := s.allowGuess(ctx, totpKey(p.user)); err != nil {
		return nil, err
	}
	if !totp.Validate(strings.TrimSpace(req.Code), p.user.TOTPPending) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "that code does not match; check the app's clock and try the next one")
	}
	s.guessed(totpKey(p.user))
	u, err := s.store.UpdateUser(p.user.ID, map[string]any{"totp_secret": p.user.TOTPPending, "totp_pending": ""})
	if err != nil {
		return nil, internal("could not save the change", err)
	}
	return userMessage(u), nil
}

func (s *Server) DisableTotp(ctx context.Context, req *inabav1.DisableTotpRequest) (*inabav1.User, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if p.user.TOTPSecret == "" {
		return userMessage(p.user), nil
	}
	// Turning it off takes what turning it on did: a code from the app.
	if err := s.allowGuess(ctx, totpKey(p.user)); err != nil {
		return nil, err
	}
	if !totp.Validate(strings.TrimSpace(req.Code), p.user.TOTPSecret) {
		return nil, connect.NewError(connect.CodePermissionDenied, "that code does not match")
	}
	s.guessed(totpKey(p.user))
	u, err := s.store.UpdateUser(p.user.ID, map[string]any{"totp_secret": ""})
	if err != nil {
		return nil, internal("could not save the change", err)
	}
	return userMessage(u), nil
}

func (s *Server) ListSessions(ctx context.Context, _ *inabav1.ListSessionsRequest) (*inabav1.ListSessionsResponse, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	tokens, err := s.store.Tokens(store.KindSession, p.user.ID)
	if err != nil {
		return nil, internal("could not read the sign-ins", err)
	}
	res := &inabav1.ListSessionsResponse{}
	for _, t := range tokens {
		m := &inabav1.LoginSession{Id: t.ID, UserAgent: t.Name, CreatedAt: timestamppb.New(t.CreatedAt), Current: p.token != nil && p.token.ID == t.ID}
		if t.ExpiresAt != nil {
			if t.ExpiresAt.Before(time.Now()) {
				continue
			}
			m.ExpiresAt = timestamppb.New(*t.ExpiresAt)
		}
		res.Sessions = append(res.Sessions, m)
	}
	return res, nil
}

func (s *Server) RevokeSession(ctx context.Context, req *inabav1.RevokeSessionRequest) (*inabav1.RevokeSessionResponse, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	// Only a browser sign-in of one's own; API tokens have their own list.
	tokens, err := s.store.Tokens(store.KindSession, p.user.ID)
	if err != nil {
		return nil, internal("could not read the sign-ins", err)
	}
	for _, t := range tokens {
		if t.ID == req.Id {
			return &inabav1.RevokeSessionResponse{}, s.deleteToken(t.ID, p.user.ID)
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, "no sign-in "+req.Id)
}

// Passkeys.

// ceremonyTTL is how long a passkey prompt may stay open between begin and finish.
const ceremonyTTL = 5 * time.Minute

// shortcut: open passkey prompts live in this process's memory; a restart between begin and finish asks again.
type ceremony struct {
	data    webauthn.SessionData
	userID  string // empty for signing in
	origin  string
	expires time.Time
}

// waUser is an account as the WebAuthn library sees it.
type waUser struct {
	u     *store.User
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return []byte(w.u.ID) }
func (w waUser) WebAuthnName() string                       { return w.u.Name }
func (w waUser) WebAuthnDisplayName() string                { return displayName(w.u) }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func displayName(u *store.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Name
}

func (s *Server) loadWAUser(u *store.User) (waUser, error) {
	keys, err := s.store.Passkeys(u.ID)
	if err != nil {
		return waUser{}, err
	}
	w := waUser{u: u}
	for _, k := range keys {
		var c webauthn.Credential
		if json.Unmarshal(k.Credential, &c) == nil {
			w.creds = append(w.creds, c)
		}
	}
	return w, nil
}

// relyingParty is WebAuthn set up for where the request came from: the public URL
// when the server has one, otherwise the page's own origin.
func (s *Server) relyingParty(ctx context.Context) (*webauthn.WebAuthn, string, error) {
	origin := strings.TrimSuffix(s.cfg.PublicURL, "/")
	if origin == "" {
		if info, ok := connect.CallInfoForServerContext(ctx); ok {
			origin = info.RequestHeader().Get("Origin")
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return nil, "", connect.NewError(connect.CodeFailedPrecondition, "passkeys need a browser page, which says where it is")
	}
	wa, err := webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "inaba", RPOrigins: []string{origin}})
	if err != nil {
		return nil, "", internal("could not set up passkeys", err)
	}
	return wa, origin, nil
}

// maxCeremonies bounds the prompts callers without an account can make the server hold.
const maxCeremonies = 1000

func (s *Server) keepCeremony(c *ceremony) (string, error) {
	id := store.RandomPassword() + store.RandomPassword()
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ceremonies == nil {
		s.ceremonies = map[string]*ceremony{}
	}
	for k, v := range s.ceremonies {
		if now.After(v.expires) {
			delete(s.ceremonies, k)
		}
	}
	if len(s.ceremonies) >= maxCeremonies {
		return "", connect.NewError(connect.CodeResourceExhausted, "too many passkey prompts are open; try again in a few minutes")
	}
	c.expires = now.Add(ceremonyTTL)
	s.ceremonies[id] = c
	return id, nil
}

// takeCeremony returns an open prompt once; a second finish finds none.
func (s *Server) takeCeremony(id, origin string) (*ceremony, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.ceremonies[id]
	delete(s.ceremonies, id)
	if c == nil || time.Now().After(c.expires) || c.origin != origin {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "that passkey prompt expired; try again")
	}
	return c, nil
}

func passkeyMessage(k *store.Passkey) *inabav1.Passkey {
	m := &inabav1.Passkey{Id: k.ID, Name: k.Name, CreatedAt: timestamppb.New(k.CreatedAt)}
	if k.LastUsedAt != nil {
		m.LastUsedAt = timestamppb.New(*k.LastUsedAt)
	}
	return m
}

func (s *Server) ListPasskeys(ctx context.Context, _ *inabav1.ListPasskeysRequest) (*inabav1.ListPasskeysResponse, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := s.store.Passkeys(p.user.ID)
	if err != nil {
		return nil, internal("could not read the passkeys", err)
	}
	res := &inabav1.ListPasskeysResponse{}
	for i := range keys {
		res.Passkeys = append(res.Passkeys, passkeyMessage(&keys[i]))
	}
	return res, nil
}

func (s *Server) BeginPasskeyRegistration(ctx context.Context, _ *inabav1.BeginPasskeyRequest) (*inabav1.BeginPasskeyResponse, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	wa, origin, err := s.relyingParty(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.loadWAUser(p.user)
	if err != nil {
		return nil, internal("could not read the passkeys", err)
	}
	// A discoverable key, so it can sign in with no name typed; none the account already has.
	var exclude []protocol.CredentialDescriptor
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	options, data, err := wa.BeginRegistration(user, webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired), webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, internal("could not start the passkey", err)
	}
	b, err := json.Marshal(options.Response)
	if err != nil {
		return nil, internal("could not start the passkey", err)
	}
	id, err := s.keepCeremony(&ceremony{data: *data, userID: p.user.ID, origin: origin})
	if err != nil {
		return nil, err
	}
	return &inabav1.BeginPasskeyResponse{CeremonyId: id, OptionsJson: string(b)}, nil
}

func (s *Server) FinishPasskeyRegistration(ctx context.Context, req *inabav1.FinishPasskeyRegistrationRequest) (*inabav1.Passkey, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	wa, origin, err := s.relyingParty(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.takeCeremony(req.CeremonyId, origin)
	if err != nil {
		return nil, err
	}
	if c.userID != p.user.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, "that passkey prompt was someone else's")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(strings.NewReader(req.CredentialJson))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the browser's answer could not be read: "+err.Error())
	}
	user, err := s.loadWAUser(p.user)
	if err != nil {
		return nil, internal("could not read the passkeys", err)
	}
	cred, err := wa.CreateCredential(user, c.data, parsed)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the passkey did not check out: "+err.Error())
	}
	raw, _ := json.Marshal(cred)
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 {
		name = "Passkey"
	}
	k := &store.Passkey{ID: base64.RawURLEncoding.EncodeToString(cred.ID), UserID: p.user.ID, Name: name, Credential: raw}
	if err := s.store.SavePasskey(k); err != nil {
		return nil, internal("could not save the passkey", err)
	}
	return passkeyMessage(k), nil
}

func (s *Server) DeletePasskey(ctx context.Context, req *inabav1.DeletePasskeyRequest) (*inabav1.DeletePasskeyResponse, error) {
	p, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	err = s.store.DeletePasskey(p.user.ID, req.Id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no passkey "+req.Id)
	}
	if err != nil {
		return nil, internal("could not remove the passkey", err)
	}
	return &inabav1.DeletePasskeyResponse{}, nil
}

func (s *Server) BeginPasskeyLogin(ctx context.Context, _ *inabav1.BeginPasskeyRequest) (*inabav1.BeginPasskeyResponse, error) {
	wa, origin, err := s.relyingParty(ctx)
	if err != nil {
		return nil, err
	}
	options, data, err := wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, internal("could not start signing in", err)
	}
	b, err := json.Marshal(options.Response)
	if err != nil {
		return nil, internal("could not start signing in", err)
	}
	id, err := s.keepCeremony(&ceremony{data: *data, origin: origin})
	if err != nil {
		return nil, err
	}
	return &inabav1.BeginPasskeyResponse{CeremonyId: id, OptionsJson: string(b)}, nil
}

func (s *Server) FinishPasskeyLogin(ctx context.Context, req *inabav1.FinishPasskeyLoginRequest) (*inabav1.User, error) {
	wa, origin, err := s.relyingParty(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.takeCeremony(req.CeremonyId, origin)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(strings.NewReader(req.CredentialJson))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the browser's answer could not be read: "+err.Error())
	}
	refused := connect.NewError(connect.CodeUnauthenticated, "that passkey does not sign in to any account here")
	found, cred, err := wa.ValidatePasskeyLogin(func(_, userHandle []byte) (webauthn.User, error) {
		u, err := s.store.User(string(userHandle))
		if err != nil {
			return nil, err
		}
		return s.loadWAUser(u)
	}, c.data, parsed)
	if err != nil {
		return nil, refused
	}
	u := found.(waUser).u
	// The sign counter moves with each use; keeping it lets a cloned key be noticed.
	if k, err := s.store.Passkey(base64.RawURLEncoding.EncodeToString(cred.ID)); err == nil && k.UserID == u.ID {
		k.Credential, _ = json.Marshal(cred)
		now := time.Now()
		k.LastUsedAt = &now
		_ = s.store.SavePasskey(k)
	}
	return s.signIn(ctx, u)
}

// signIn starts a browser session for the account and answers with it.
func (s *Server) signIn(ctx context.Context, u *store.User) (*inabav1.User, error) {
	agent := ""
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		agent = info.RequestHeader().Get("User-Agent")
	}
	cookie, err := s.startSession(u, agent)
	if err != nil {
		return nil, internal("could not save the session", err)
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		info.ResponseHeader().Add("Set-Cookie", cookie.String())
	}
	return userMessage(u), nil
}

// totpKey is the budget a signed-in account's own TOTP changes draw on. It is keyed by the
// account's id, not its name: names are not unique (an OIDC account takes whatever name the
// IdP gives), so a success here must never reset the budget a password sign-in to another
// account of the same name uses.
func totpKey(u *store.User) string { return "id:" + u.ID }
