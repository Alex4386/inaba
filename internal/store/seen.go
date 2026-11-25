package store

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ThreadSeen is how far one account has read a thread of a host, by the seq of the last
// event it saw, for unread markers. Only ids, a number and a time; nothing the thread says.
type ThreadSeen struct {
	UserID    string `gorm:"primaryKey"`
	HostID    string `gorm:"primaryKey"`
	ThreadID  string `gorm:"primaryKey"`
	Seq       uint64
	UpdatedAt time.Time
}

// MarkSeen raises a user's read mark on a thread to seq and returns the mark as kept. A
// lower seq leaves it where it is, so tabs reporting out of order do not unread a thread.
func (s *Store) MarkSeen(userID, hostID, threadID string, seq uint64) (uint64, error) {
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "host_id"}, {Name: "thread_id"}},
		DoUpdates: clause.Set{{Column: clause.Column{Name: "seq"}, Value: gorm.Expr("MAX(seq, excluded.seq)")}, {Column: clause.Column{Name: "updated_at"}, Value: time.Now()}},
	}).Create(&ThreadSeen{UserID: userID, HostID: hostID, ThreadID: threadID, Seq: seq}).Error
	if err != nil {
		return 0, err
	}
	var kept ThreadSeen
	err = s.db.Where("user_id = ? AND host_id = ? AND thread_id = ?", userID, hostID, threadID).Take(&kept).Error
	return kept.Seq, err
}

// Seen lists a user's read marks on a host's threads.
func (s *Store) Seen(userID, hostID string) ([]ThreadSeen, error) {
	var seen []ThreadSeen
	return seen, s.db.Where("user_id = ? AND host_id = ?", userID, hostID).Find(&seen).Error
}
