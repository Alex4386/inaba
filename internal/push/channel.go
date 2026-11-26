package push

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/store"
)

// Event is what a channel is told: the alert, the host it came from, and the message a
// device would show, its URL made absolute (or empty when the server has no public URL).
type Event struct {
	HostID string
	Alert  *inabav1.Alert
	Message
	// Delivery names this delivery, the same on every try.
	Delivery string
}

// Kind is one kind of channel: what makes its settings valid, and the request that tells it
// of an event. A new kind is one more entry in Kinds.
type Kind struct {
	Check   func(c *store.Channel) error
	Request func(ctx context.Context, c *store.Channel, e Event) (*http.Request, error)
}

var Kinds = map[string]Kind{
	"webhook": {Check: func(c *store.Channel) error { return checkURL(c.URL, nil, "") }, Request: webhook},
	"ntfy": {Check: func(c *store.Channel) error {
		if !ntfyTopic.MatchString(c.Topic) {
			return errors.New("an ntfy topic is 1 to 64 letters, digits, - and _")
		}
		// The topic goes in its own field; one on the end of the address would get the JSON as the message.
		if u, err := url.Parse(c.URL); err == nil && strings.Trim(u.Path, "/") != "" {
			return errors.New("the ntfy server is its address alone, such as https://ntfy.sh; the topic goes in its own field")
		}
		return checkURL(c.URL, nil, "")
	}, Request: ntfy},
	"discord": {Check: func(c *store.Channel) error {
		return checkURL(c.URL, []string{"discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com"}, "/api/webhooks/")
	}, Request: func(ctx context.Context, c *store.Channel, e Event) (*http.Request, error) {
		return postJSON(ctx, c.URL, map[string]any{"content": cut(text(e), 2000), "allowed_mentions": map[string]any{"parse": []string{}}})
	}},
	"slack": {Check: func(c *store.Channel) error {
		return checkURL(c.URL, []string{"hooks.slack.com"}, "/")
	}, Request: func(ctx context.Context, c *store.Channel, e Event) (*http.Request, error) {
		return postJSON(ctx, c.URL, map[string]any{"text": cut(text(e), 3000)})
	}},
	"telegram": {Check: func(c *store.Channel) error {
		if !botToken.MatchString(c.Secret) || !chatID.MatchString(c.ChatID) {
			return errors.New("Telegram needs a bot token from @BotFather and a chat id, a number or @channel")
		}
		return nil
	}, Request: func(ctx context.Context, c *store.Channel, e Event) (*http.Request, error) {
		return postJSON(ctx, "https://api.telegram.org/bot"+c.Secret+"/sendMessage", map[string]any{"chat_id": c.ChatID, "text": cut(text(e), 4000), "link_preview_options": map[string]any{"is_disabled": true}})
	}},
}

var (
	ntfyTopic = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	botToken  = regexp.MustCompile(`^[0-9]{1,20}:[-_A-Za-z0-9]{20,100}$`)
	chatID    = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{4,64})$`)
)

// checkURL says whether raw is an address a channel may post to: http or https, or only
// https on one of hosts under pathPrefix when hosts are given.
func checkURL(raw string, hosts []string, pathPrefix string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 2048 {
		return errors.New("an http or https address is needed")
	}
	if hosts == nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range hosts {
		if host == h && u.Scheme == "https" && u.Port() == "" && u.User == nil && strings.HasPrefix(u.Path, pathPrefix) {
			return nil
		}
	}
	return fmt.Errorf("the address must be https://%s%s…", hosts[0], pathPrefix)
}

// text is the message as one plain text, for the chat kinds.
func text(e Event) string {
	t := e.Title + "\n" + e.Body
	if e.URL != "" {
		t += "\n" + e.URL
	}
	return t
}

func postJSON(ctx context.Context, to string, body any) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return post(ctx, to, data)
}

func post(ctx context.Context, to string, data []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "inaba")
	return req, nil
}

// webhook posts the event as JSON, signed with the channel's secret the way GitHub signs.
func webhook(ctx context.Context, c *store.Channel, e Event) (*http.Request, error) {
	al := e.Alert
	at := time.Now()
	if al.At != nil {
		at = al.At.AsTime()
	}
	body := map[string]any{"event": al.Event, "time": at.UTC().Format(time.RFC3339), "host": map[string]any{"id": e.HostID}}
	if al.ThreadId != "" {
		body["thread"] = map[string]any{"id": al.ThreadId, "title": al.ThreadTitle, "projectId": al.ProjectId, "projectName": al.ProjectName, "automation": al.Automation, "url": e.URL}
	}
	if al.Error != "" {
		body["error"] = al.Error
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := post(ctx, c.URL, data)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(c.Secret))
	mac.Write(data)
	req.Header.Set("X-Inaba-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Inaba-Event", al.Event)
	req.Header.Set("X-Inaba-Delivery", e.Delivery)
	return req, nil
}

// ntfy publishes as JSON, which carries any language, unlike ntfy's headers.
func ntfy(ctx context.Context, c *store.Channel, e Event) (*http.Request, error) {
	msg := map[string]any{"topic": c.Topic, "title": cut(e.Title, maxTitle), "message": cut(e.Body, maxBody), "priority": 3}
	if e.High {
		msg["priority"] = 4
	}
	if e.URL != "" {
		msg["click"] = e.URL
	}
	req, err := postJSON(ctx, c.URL, msg)
	if err == nil && c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	return req, err
}

// Channels delivers to channels over HTTP, refusing addresses inside the server's network.
type Channels struct {
	client  *http.Client
	timeout time.Duration
	// retryDelays is the wait before each try; a field, so tests need not wait.
	retryDelays []time.Duration
}

// ErrPrivate is an address a channel may not post to: loopback, private, link-local,
// shared (CGNAT), unspecified or multicast.
var ErrPrivate = errors.New("the address is inside the server's network")

// Ranges no public receiver lives in, beyond what netip names: CGNAT, "this network", and the benchmarking block.
var blockedNets = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("198.18.0.0/15")}

// NewChannels makes the deliverer. allowPrivate lets channels reach the server's own
// network, for an operator whose receivers live there.
func NewChannels(allowPrivate bool) *Channels {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		// Checked at dial time, on the address actually dialled, so a name that resolves
		// differently later cannot slip past.
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			if ip := ap.Addr().Unmap(); ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || slices.ContainsFunc(blockedNets, func(p netip.Prefix) bool { return p.Contains(ip) }) {
				return ErrPrivate
			}
			return nil
		}
	}
	return &Channels{
		client: &http.Client{
			// No Proxy: a proxy would dial for us, past the check.
			Transport:     &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		timeout:     10 * time.Second,
		retryDelays: []time.Duration{0, 2 * time.Second, 10 * time.Second},
	}
}

// Send tells one channel of an event, trying again after a failure the receiver may get
// over, and answers the last HTTP status (0 for none) and why it did not get through.
func (cs *Channels) Send(c *store.Channel, e Event) (int, error) {
	kind, ok := Kinds[c.Kind]
	if !ok {
		return 0, errors.New("no channel kind " + c.Kind)
	}
	var status int
	var err error
	for _, wait := range cs.retryDelays {
		time.Sleep(wait)
		status, err = cs.try(kind, c, e)
		// A client error will not change on its own; a server error or no answer may.
		if errors.Is(err, ErrPrivate) || (status != 0 && status < 500 && status != http.StatusTooManyRequests) {
			break
		}
	}
	return status, err
}

func (cs *Channels) try(kind Kind, c *store.Channel, e Event) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cs.timeout)
	defer cancel()
	req, err := kind.Request(ctx, c, e)
	if err != nil {
		return 0, err
	}
	res, err := cs.client.Do(req)
	if err != nil {
		// Without the address, which may hold the channel's token.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return res.StatusCode, fmt.Errorf("the receiver answered %d", res.StatusCode)
	}
	return res.StatusCode, nil
}
