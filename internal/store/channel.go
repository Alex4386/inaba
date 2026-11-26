package store

import (
	"time"

	"gorm.io/gorm"
)

// MaxChannels is how many notification channels one account may have.
const MaxChannels = 20

// Channel is another way an account is told about its threads, beside its push devices:
// a webhook, ntfy, Discord, Slack or Telegram.
// shortcut: Secret sits in the database file in plain text, like the VAPID key; encrypt it at rest if the file may travel.
type Channel struct {
	ID     string `gorm:"primaryKey"`
	UserID string `gorm:"index"`
	Kind   string
	Label  string
	// URL is where it posts: the webhook, the ntfy server, or the Discord or Slack webhook.
	URL    string
	Topic  string // ntfy's
	ChatID string // Telegram's
	// Secret signs a webhook's deliveries, or is the ntfy access token or the Telegram bot token.
	Secret string
	// Events it is sent; empty for all.
	Events          []string `gorm:"serializer:json"`
	AutomationsOnly bool
	Enabled         bool
	CreatedAt       time.Time
	// The latest delivery: when, of what, the HTTP status, and why it failed.
	LastAt     *time.Time
	LastEvent  string
	LastStatus int
	LastError  string
}

// AddChannel saves a new channel, up to MaxChannels an account.
func (s *Store) AddChannel(c *Channel) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&Channel{}).Where("user_id = ?", c.UserID).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxChannels {
			return ErrTooMany
		}
		c.ID = newID()
		return tx.Create(c).Error
	})
}

func (s *Store) Channels(userID string) ([]Channel, error) {
	var cs []Channel
	return cs, s.db.Where("user_id = ?", userID).Order("created_at").Find(&cs).Error
}

func (s *Store) Channel(userID, id string) (*Channel, error) {
	return first[Channel](s.db.Where("id = ? AND user_id = ?", id, userID))
}

// UpdateChannel saves what a person may change of their channel: not where it delivers.
func (s *Store) UpdateChannel(c *Channel) error {
	res := s.db.Model(&Channel{}).Where("id = ? AND user_id = ?", c.ID, c.UserID).Select("label", "events", "automations_only", "enabled").Updates(c)
	if res.Error == nil && res.RowsAffected == 0 {
		return ErrNotFound
	}
	return res.Error
}

func (s *Store) DeleteChannel(userID, id string) error {
	res := s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&Channel{})
	if res.Error == nil && res.RowsAffected == 0 {
		return ErrNotFound
	}
	return res.Error
}

// RecordDelivery keeps how the latest delivery to a channel went.
func (s *Store) RecordDelivery(id string, at time.Time, event string, status int, errText string) error {
	return s.db.Model(&Channel{}).Where("id = ?", id).Updates(map[string]any{"last_at": at, "last_event": event, "last_status": status, "last_error": errText}).Error
}
