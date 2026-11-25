package server

import (
	"context"
	"crypto/sha256"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"golang.org/x/time/rate"
)

// attempts paces guesses at a secret (a password or a one-time code), by key.
type attempts struct {
	every time.Duration
	burst int

	mu   sync.Mutex
	keys map[string]*rate.Limiter
}

// maxAttemptKeys is how many keys a limiter holds at most.
const maxAttemptKeys = 10000

// allow takes one attempt for key, reporting false when the key has used up its budget.
func (a *attempts) allow(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keys == nil {
		a.keys = map[string]*rate.Limiter{}
	}
	l := a.keys[key]
	if l == nil {
		if len(a.keys) >= maxAttemptKeys {
			a.shrink()
		}
		l = rate.NewLimiter(rate.Every(a.every), a.burst)
		a.keys[key] = l
	}
	return l.Allow()
}

// shrink makes room, under mu. A limiter that has refilled is the same as a new one, so
// those go first; if every key is still busy, arbitrary ones go until a tenth is free,
// so the scan runs once per thousand new keys, not on each. Forgetting a busy key gives
// it a fresh budget, which only someone already holding thousands of addresses gains from.
func (a *attempts) shrink() {
	now := time.Now()
	for k, v := range a.keys {
		if v.TokensAt(now) >= float64(a.burst) {
			delete(a.keys, k)
		}
	}
	for k := range a.keys {
		if len(a.keys) < maxAttemptKeys*9/10 {
			break
		}
		delete(a.keys, k)
	}
}

// forget gives key a full budget again.
func (a *attempts) forget(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.keys, key)
}

type clientIPKey struct{}

// clientIP is the address a request came from. Forwarded headers are believed only
// when the direct peer is a trusted proxy, since anyone else can write them. In
// X-Forwarded-For each proxy appends the address it saw, so the list is read from the
// right and the first hop that is not a trusted proxy is the client.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	addr := parseIP(r.RemoteAddr)
	if !s.trusted(addr) {
		return addr
	}
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		hops := strings.Split(strings.Join(values, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop := parseIP(strings.TrimSpace(hops[i]))
			if !hop.IsValid() {
				// Garbage left of a trusted hop: the last trusted hop is all that is known.
				return addr
			}
			addr = hop
			if !s.trusted(hop) {
				return hop
			}
		}
		return addr
	}
	if xr := parseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); xr.IsValid() {
		return xr
	}
	return addr
}

func (s *Server) trusted(addr netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if addr.IsValid() && p.Contains(addr) {
			return true
		}
	}
	return false
}

// parseIP reads an address with or without a port; the zero Addr when it is neither.
func parseIP(v string) netip.Addr {
	if host, _, err := net.SplitHostPort(v); err == nil {
		v = host
	}
	addr, _ := netip.ParseAddr(strings.Trim(v, "[]"))
	return addr.Unmap().WithZone("")
}

// withClientIP records each request's client address for the calls below it.
func (s *Server) withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, s.clientIP(r))))
	})
}

// callerIP is the client address withClientIP recorded, the zero Addr when the call did
// not come through the HTTP handler (the control socket, tests).
func callerIP(ctx context.Context) netip.Addr {
	addr, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return addr
}

func tooManyAttempts() error {
	return connect.NewError(connect.CodeResourceExhausted, "too many attempts; wait a minute and try again")
}

// accountKey is a fixed-size key for an account name, so a long name costs no more memory
// than a short one; lowercased, so changing case does not reset the budget.
func accountKey(account string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(account)))
	return string(sum[:16])
}

// allowGuess takes one try from the client's and the account's budgets before any password
// or code is compared. The client's budget is the main brake and counts every try. The
// account's is larger and refills faster than one client's, so a single client cannot
// keep someone else's account locked; guessed resets it after a success, so in effect it
// counts only failures. Taking the account's try up front, not after a failure, keeps
// parallel requests from all slipping through before the first failure is recorded.
func (s *Server) allowGuess(ctx context.Context, account string) error {
	if ip := callerIP(ctx); ip.IsValid() {
		key := ip.String()
		if ip.Is6() {
			// One machine usually holds a whole /64, so that is what a client is.
			key = netip.PrefixFrom(ip, 64).Masked().String()
		}
		if !s.ipGuesses.allow(key) {
			return tooManyAttempts()
		}
	}
	if !s.accountGuesses.allow(accountKey(account)) {
		return tooManyAttempts()
	}
	return nil
}

// guessed marks a fully successful sign-in, giving the account its whole budget back.
func (s *Server) guessed(account string) {
	s.accountGuesses.forget(accountKey(account))
}
