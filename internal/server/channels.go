package server

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/push"
	"github.com/Alex4386/inaba/internal/store"
)

// channelEvents are the events a channel can be sent.
var channelEvents = []string{"turn.ended", "turn.failed", "input.needed"}

// deliver tells one channel of an alert and records how it went.
// shortcut: retries hold a channel slot for up to their whole backoff (about 45 seconds), so a few dead receivers slow other channels; queue deliveries if that shows up.
func (s *Server) deliver(c *store.Channel, hostID string, al *inabav1.Alert, msg push.Message) *inabav1.ChannelDelivery {
	msg.URL = s.link(msg.URL)
	at := time.Now()
	status, err := s.channels.Send(c, push.Event{HostID: hostID, Alert: al, Message: msg, Delivery: rand.Text()})
	d := &inabav1.ChannelDelivery{Time: timestamppb.New(at), Event: al.Event, Status: uint32(status)}
	if err != nil {
		d.Error = err.Error()
	}
	if err := s.store.RecordDelivery(c.ID, at, d.Event, status, d.Error); err != nil {
		slog.Warn("could not record a channel's delivery", "channel", c.ID, "error", err)
	}
	return d
}

// link is a path of the web UI as an address a channel can open, or "" when the server
// does not know its own.
func (s *Server) link(path string) string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	return strings.TrimSuffix(s.cfg.PublicURL, "/") + path
}

func channelMessage(c *store.Channel) *inabav1.NotificationChannel {
	m := &inabav1.NotificationChannel{Id: c.ID, Kind: c.Kind, Label: c.Label, Events: c.Events, AutomationsOnly: c.AutomationsOnly, Enabled: c.Enabled, CreatedAt: timestamppb.New(c.CreatedAt)}
	// Credentials stay on the server: Discord and Slack webhook addresses are tokens too.
	switch c.Kind {
	case "webhook":
		m.Config = &inabav1.NotificationChannel_Webhook{Webhook: &inabav1.WebhookChannel{Url: c.URL}}
	case "ntfy":
		m.Config = &inabav1.NotificationChannel_Ntfy{Ntfy: &inabav1.NtfyChannel{ServerUrl: c.URL, Topic: c.Topic}}
	case "discord":
		m.Config = &inabav1.NotificationChannel_Discord{Discord: &inabav1.DiscordChannel{}}
	case "slack":
		m.Config = &inabav1.NotificationChannel_Slack{Slack: &inabav1.SlackChannel{}}
	case "telegram":
		m.Config = &inabav1.NotificationChannel_Telegram{Telegram: &inabav1.TelegramChannel{ChatId: c.ChatID}}
	}
	if c.LastAt != nil {
		m.LastDelivery = &inabav1.ChannelDelivery{Time: timestamppb.New(*c.LastAt), Event: c.LastEvent, Status: uint32(c.LastStatus), Error: c.LastError}
	}
	return m
}

// checkChannel checks what a person may set on a channel at any time.
func checkChannel(req *inabav1.NotificationChannel) error {
	label := strings.TrimSpace(req.Label)
	if utf8.RuneCountInString(label) > maxLabel || !utf8.ValidString(label) {
		return connect.NewError(connect.CodeInvalidArgument, "a label is at most 64 characters")
	}
	for _, e := range req.Events {
		if !slices.Contains(channelEvents, e) {
			return connect.NewError(connect.CodeInvalidArgument, "unknown event "+e+"; the events are turn.ended, turn.failed and input.needed")
		}
	}
	return nil
}

func (n notifications) ListChannels(ctx context.Context, _ *inabav1.ListChannelsRequest) (*inabav1.ListChannelsResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := n.s.store.Channels(uid)
	if err != nil {
		return nil, internal("could not read the channels", err)
	}
	res := &inabav1.ListChannelsResponse{}
	for i := range cs {
		res.Channels = append(res.Channels, channelMessage(&cs[i]))
	}
	return res, nil
}

func (n notifications) CreateChannel(ctx context.Context, req *inabav1.NotificationChannel) (*inabav1.CreateChannelResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkChannel(req); err != nil {
		return nil, err
	}
	c := &store.Channel{UserID: uid, Label: strings.TrimSpace(req.Label), Events: req.Events, AutomationsOnly: req.AutomationsOnly, Enabled: true}
	switch cfg := req.Config.(type) {
	case *inabav1.NotificationChannel_Webhook:
		c.Kind, c.URL, c.Secret = "webhook", cfg.Webhook.GetUrl(), rand.Text()
	case *inabav1.NotificationChannel_Ntfy:
		c.Kind, c.URL, c.Topic, c.Secret = "ntfy", cfg.Ntfy.GetServerUrl(), cfg.Ntfy.GetTopic(), cfg.Ntfy.GetAccessToken()
	case *inabav1.NotificationChannel_Discord:
		c.Kind, c.URL = "discord", cfg.Discord.GetWebhookUrl()
	case *inabav1.NotificationChannel_Slack:
		c.Kind, c.URL = "slack", cfg.Slack.GetWebhookUrl()
	case *inabav1.NotificationChannel_Telegram:
		c.Kind, c.Secret, c.ChatID = "telegram", cfg.Telegram.GetBotToken(), cfg.Telegram.GetChatId()
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, "a channel is a webhook, ntfy, discord, slack or telegram")
	}
	if err := push.Kinds[c.Kind].Check(c); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	if err := n.s.store.AddChannel(c); errors.Is(err, store.ErrTooMany) {
		return nil, connect.NewError(connect.CodeResourceExhausted, "an account has at most 20 channels; remove one first")
	} else if err != nil {
		return nil, internal("could not save the channel", err)
	}
	res := &inabav1.CreateChannelResponse{Channel: channelMessage(c)}
	if c.Kind == "webhook" {
		res.Secret = c.Secret
	}
	return res, nil
}

func (n notifications) UpdateChannel(ctx context.Context, req *inabav1.NotificationChannel) (*inabav1.NotificationChannel, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkChannel(req); err != nil {
		return nil, err
	}
	c := &store.Channel{ID: req.Id, UserID: uid, Label: strings.TrimSpace(req.Label), Events: req.Events, AutomationsOnly: req.AutomationsOnly, Enabled: req.Enabled}
	if err := n.s.store.UpdateChannel(c); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no channel "+req.Id)
	} else if err != nil {
		return nil, internal("could not save the channel", err)
	}
	if c, err = n.s.store.Channel(uid, req.Id); err != nil {
		return nil, internal("could not read the channel", err)
	}
	return channelMessage(c), nil
}

func (n notifications) DeleteChannel(ctx context.Context, req *inabav1.DeleteChannelRequest) (*inabav1.DeleteChannelResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if err := n.s.store.DeleteChannel(uid, req.Id); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no channel "+req.Id)
	} else if err != nil {
		return nil, internal("could not remove the channel", err)
	}
	return &inabav1.DeleteChannelResponse{}, nil
}

func (n notifications) TestChannel(ctx context.Context, req *inabav1.TestChannelRequest) (*inabav1.ChannelDelivery, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := n.s.store.Channel(uid, req.Id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no channel "+req.Id)
	} else if err != nil {
		return nil, internal("could not read the channel", err)
	}
	return n.s.deliver(c, "", &inabav1.Alert{Event: "test", At: timestamppb.Now()}, push.Message{Title: "inaba", Body: "Test notification", URL: "/ui/", Tag: "test"}), nil
}
