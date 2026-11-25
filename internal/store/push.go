package store

import (
	"errors"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"gorm.io/gorm"
)

// ErrTooMany is a limit reached, such as an account's number of devices.
var ErrTooMany = errors.New("too many")

// MaxDevices is how many push devices one account may have.
const MaxDevices = 20

// PushDevice is somewhere an account's notifications are pushed to.
type PushDevice struct {
	ID     string `gorm:"primaryKey"`
	UserID string `gorm:"index"`
	// Platform is "web" for now; "apns" and "fcm" when native apps come.
	Platform string
	// Endpoint is the web push service URL, or a native device token.
	Endpoint string `gorm:"uniqueIndex"`
	// P256dh and Auth are a browser subscription's keys (RFC 8291), base64url; empty for native.
	P256dh, Auth string
	Label        string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

// Snooze mutes a project or a thread of a host for one account, until a time or for good.
// It holds only ids and a time, nothing of what the thread says.
type Snooze struct {
	UserID   string `gorm:"primaryKey"`
	HostID   string `gorm:"primaryKey"`
	Kind     string `gorm:"primaryKey"` // "project" or "thread"
	TargetID string `gorm:"primaryKey"`
	// Until is when it wakes; nil for never.
	Until     *time.Time
	CreatedAt time.Time
}

// VAPIDKey is the server's one key pair for signing web pushes. Browsers subscribe with
// its public half, so it is made once and never rotated: a new one would orphan them all.
// shortcut: the private key sits in the database file in plain text, like the TOTP secrets; encrypt it at rest if the file may travel.
type VAPIDKey struct {
	ID      int `gorm:"primaryKey"`
	Public  string
	Private string
}

// VAPIDKeys returns the server's key pair, making it on first use.
func (s *Store) VAPIDKeys() (public, private string, err error) {
	k, err := first[VAPIDKey](s.db.Where("id = 1"))
	if err == nil {
		return k.Public, k.Private, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	// Two servers starting at once on one file both try; the first row wins and both read it back.
	if err := s.db.Where(VAPIDKey{ID: 1}).FirstOrCreate(&VAPIDKey{ID: 1, Public: pub, Private: priv}).Error; err != nil {
		return "", "", err
	}
	k, err = first[VAPIDKey](s.db.Where("id = 1"))
	if err != nil {
		return "", "", err
	}
	return k.Public, k.Private, nil
}

// RegisterDevice adds a device, or updates the one with the same endpoint and moves it to
// d.UserID: a browser that signs in to another account takes its subscription along.
func (s *Store) RegisterDevice(d *PushDevice) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		old, err := first[PushDevice](tx.Where("endpoint = ?", d.Endpoint))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		var n int64
		if err := tx.Model(&PushDevice{}).Where("user_id = ? AND endpoint <> ?", d.UserID, d.Endpoint).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxDevices {
			return ErrTooMany
		}
		if old == nil {
			d.ID = newID()
			return tx.Create(d).Error
		}
		d.ID, d.CreatedAt, d.LastUsedAt = old.ID, old.CreatedAt, old.LastUsedAt
		return tx.Model(old).Select("user_id", "platform", "p256dh", "auth", "label").Updates(d).Error
	})
}

func (s *Store) Devices(userID string) ([]PushDevice, error) {
	var ds []PushDevice
	return ds, s.db.Where("user_id = ?", userID).Order("created_at").Find(&ds).Error
}

func (s *Store) Device(userID, id string) (*PushDevice, error) {
	return first[PushDevice](s.db.Where("id = ? AND user_id = ?", id, userID))
}

// DeleteDevice removes one of a user's devices; an empty user removes it whoever owns it,
// as when its push service says it is gone.
func (s *Store) DeleteDevice(userID, id string) error {
	q := s.db.Where("id = ?", id)
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	res := q.Delete(&PushDevice{})
	if res.Error == nil && res.RowsAffected == 0 {
		return ErrNotFound
	}
	return res.Error
}

// TouchDevice records that a notification reached the device.
func (s *Store) TouchDevice(id string) error {
	return s.db.Model(&PushDevice{}).Where("id = ?", id).Update("last_used_at", time.Now()).Error
}

// Snoozes lists a user's snoozes on a host that have not woken.
func (s *Store) Snoozes(userID, hostID string) ([]Snooze, error) {
	var sn []Snooze
	return sn, s.db.Where("user_id = ? AND host_id = ? AND (until IS NULL OR until > ?)", userID, hostID, time.Now()).Order("created_at").Find(&sn).Error
}

// SetSnooze adds a snooze or moves the wake time of the same one.
func (s *Store) SetSnooze(sn *Snooze) error {
	// SQLite compares times as text, so they are kept in the zone the queries use, as tokens' are.
	if sn.Until != nil {
		local := sn.Until.Local()
		sn.Until = &local
	}
	return s.db.Save(sn).Error
}

func (s *Store) ClearSnooze(userID, hostID, kind, targetID string) error {
	return s.db.Where("user_id = ? AND host_id = ? AND kind = ? AND target_id = ?", userID, hostID, kind, targetID).Delete(&Snooze{}).Error
}

// DropSnoozes forgets everyone's snoozes of a project or thread, as when it is deleted.
func (s *Store) DropSnoozes(hostID, kind, targetID string) error {
	return s.db.Where("host_id = ? AND kind = ? AND target_id = ?", hostID, kind, targetID).Delete(&Snooze{}).Error
}

// UsersOf is who can see a host, the inverse of HostsFor: its owner and members, or every
// account for a shared host open to all.
func (s *Store) UsersOf(hostID string) ([]string, error) {
	h, err := s.Host(hostID)
	if err != nil {
		return nil, err
	}
	var ids []string
	if h.Owner == "" && !h.Restricted {
		return ids, s.db.Model(&User{}).Pluck("id", &ids).Error
	}
	if err := s.db.Model(&HostMember{}).Where("host_id = ?", hostID).Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}
	if h.Owner != "" {
		ids = append(ids, h.Owner)
	}
	return ids, nil
}
