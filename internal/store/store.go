// Package store is inaba-server's database: users, tokens and hosts.
// The server and the inaba-server CLI both work on it directly.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/Alex4386/inaba/internal/db"
	"github.com/Alex4386/inaba/internal/rpc"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrExists    = errors.New("already exists")
	ErrLastAdmin = errors.New("the last administrator")
)

type User struct {
	ID   string `gorm:"primaryKey"`
	Name string `gorm:"index"`
	// PasswordHash is set for users who sign in with a password.
	PasswordHash string
	// OIDCSubject is "<issuer> <sub>" for users who sign in through OIDC.
	OIDCSubject string `gorm:"index;column:oidc_subject"`
	// Admin accounts have every permission and manage the other accounts.
	Admin bool
	// Permissions is the comma-separated list of what the account may do beyond the
	// baseline, from the Perm constants.
	Permissions string
	// AvatarVersion changes with the account's picture, kept in Avatar; 0 when there is none.
	AvatarVersion int64
	// DisplayName is what to call the person; Name when empty.
	DisplayName string
	// TOTPSecret turns on two-step sign-in; TOTPPending is one being set up.
	// shortcut: kept as the server keeps passwords' hashes, in its database file; encrypt them at rest if the file may travel.
	TOTPSecret  string
	TOTPPending string
	// Stealth keeps an administrator out of what others see of who is where.
	Stealth   bool
	CreatedAt time.Time
}

// Passkey is a WebAuthn credential an account signs in with.
type Passkey struct {
	// ID is the credential id, base64url.
	ID     string `gorm:"primaryKey"`
	UserID string `gorm:"index"`
	Name   string
	// Credential is the library's credential record as JSON, sign counter included.
	Credential []byte
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Avatar is an account's picture, apart from User so signing a request in reads no image.
type Avatar struct {
	UserID string `gorm:"primaryKey"`
	Type   string
	Data   []byte
}

// Permissions an administrator can grant. Without any, an account can use the hosts
// and projects that exist and run threads in them.
const (
	// PermSharedHosts lets its holder add and look after shared hosts, which the server enforces.
	PermSharedHosts = "shared-hosts"
	// The rest are enforced by hosts, which know them by these names.
	PermProjects        = rpc.PermProjects
	PermThreadsAnywhere = rpc.PermThreadsAnywhere
	PermAutomations     = rpc.PermAutomations
	PermHostSettings    = rpc.PermHostSettings
	PermHousekeeping    = rpc.PermHousekeeping
)

// AllPermissions is every permission there is, which is what an administrator has.
var AllPermissions = []string{PermSharedHosts, PermProjects, PermThreadsAnywhere, PermAutomations, PermHostSettings, PermHousekeeping}

// granularMark ends every permission list saved since the permissions were split, so one
// saved before it (where "hosts" and "projects" each covered several) can be told apart.
const granularMark = "granular"

// storedPermissions is how a permission list is saved.
func storedPermissions(permissions []string) string {
	return strings.Join(append(slices.Clone(permissions), granularMark), ",")
}

// Granted lists what the account may do, an administrator's being everything.
func (u *User) Granted() []string {
	if u.Admin {
		return AllPermissions
	}
	if u.Permissions == "" {
		return nil
	}
	stored := strings.Split(u.Permissions, ",")
	if i := slices.Index(stored, granularMark); i >= 0 {
		return slices.Delete(stored, i, i+1)
	}
	// Saved before the split: read it as granting everything it used to, so no account loses access.
	var granted []string
	for _, p := range stored {
		switch p {
		case rpc.PermHosts:
			granted = append(granted, PermSharedHosts, PermHostSettings, PermHousekeeping)
		case PermProjects:
			granted = append(granted, PermProjects, PermThreadsAnywhere, PermAutomations)
		default:
			granted = append(granted, p)
		}
	}
	return granted
}

func (u *User) Can(permission string) bool {
	return slices.Contains(u.Granted(), permission)
}

const (
	KindSession = "session"
	KindAPI     = "api"
	// KindHost is a host's long-lived secret: its refresh token, or for a host set up
	// with `inaba-server host add`, what it connects with directly.
	KindHost = "host"
	// KindHostAccess is a short-lived token a host connects with, from its refresh token.
	KindHostAccess = "host-access"
	// KindHostInvite is a link that lets whoever opens it into a host, owned by the host's id and
	// named for the user who made it.
	KindHostInvite = "host-invite"
)

// hostKinds are the tokens a host holds, owned by its id.
var hostKinds = []string{KindHost, KindHostAccess, KindHostInvite}

type Token struct {
	ID   string `gorm:"primaryKey"`
	Kind string `gorm:"index"`
	// Hash is the SHA-256 of the secret; the secret itself is never stored.
	Hash string `gorm:"uniqueIndex"`
	// Owner is a user id, or a host id for KindHost.
	Owner string `gorm:"index;default:''"`
	Name  string
	// Role is what a KindHostInvite makes whoever accepts it; "" (older invites) means RoleMember.
	Role string `gorm:"default:''"`
	// Note tells a KindHostInvite apart, such as whom it was sent to.
	Note      string
	CreatedAt time.Time
	// ExpiresAt is nil for a token that does not expire.
	ExpiresAt *time.Time
}

type Host struct {
	ID   string `gorm:"primaryKey"`
	Name string
	// Owner is the user a personal host belongs to, whom alone it serves; "" for a
	// host shared with every account.
	Owner string `gorm:"index;default:''"`
	// OS and Arch are what the host last said it runs on, as Go names them.
	OS   string
	Arch string
	// Icon names a web UI icon; Image is a data: URL picture shown in its place.
	// shortcut: the picture rides along in every host listing; serve it on its own if pictures grow past a few hosts.
	Icon  string
	Image string
	// LastSeen is when the host last connected or was last connected; nil for one that never has.
	LastSeen *time.Time
	// System is what the host said of itself when it last connected, as a marshaled inaba.v1.SystemInfo.
	System []byte
	// Restricted keeps a shared host to its members; a personal host always is. Hosts from
	// before the column existed have it NULL, which reads as open.
	Restricted bool `gorm:"default:false"`
	CreatedAt  time.Time
}

// Member roles on a host: a member works in its projects; a manager also looks after its settings.
const (
	RoleMember  = "member"
	RoleManager = "manager"
)

type Store struct {
	db   *gorm.DB
	path string
}

func Open(path string) (*Store, error) {
	g, err := db.Open(path, &User{}, &Token{}, &Host{}, &Avatar{}, &Passkey{}, &HostMember{}, &PushDevice{}, &Channel{}, &Snooze{}, &VAPIDKey{}, &ThreadSeen{})
	if err != nil {
		return nil, err
	}
	return &Store{db: g, path: path}, nil
}

// Upkeep is what the store holds that housekeeping is about.
type Upkeep struct {
	DatabaseBytes uint64
	ExpiredTokens int64
	ActiveSignIns int64
}

func (s *Store) Upkeep() (Upkeep, error) {
	u := Upkeep{DatabaseBytes: db.Size(s.path)}
	now := time.Now()
	if err := s.db.Model(&Token{}).Where("expires_at IS NOT NULL AND expires_at <= ?", now).Count(&u.ExpiredTokens).Error; err != nil {
		return u, err
	}
	if err := s.db.Model(&Token{}).Where("kind = ? AND (expires_at IS NULL OR expires_at > ?)", KindSession, now).Count(&u.ActiveSignIns).Error; err != nil {
		return u, err
	}
	// Snoozes that woke no longer mute anything; looking after the server is a good time to drop them.
	return u, s.db.Where("until IS NOT NULL AND until <= ?", now).Delete(&Snooze{}).Error
}

// DeleteExpiredTokens removes sign-ins and tokens past their expiry, which no longer work.
func (s *Store) DeleteExpiredTokens() error {
	return s.db.Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now()).Delete(&Token{}).Error
}

func (s *Store) Compact() error { return db.Compact(s.db) }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RandomPassword makes a password for an account whose owner did not choose one.
func RandomPassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// first returns the first row matching the query, or ErrNotFound.
func first[T any](q *gorm.DB) (*T, error) {
	var rows []T
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return &rows[0], nil
}

func (s *Store) Users() ([]User, error) {
	var users []User
	return users, s.db.Order("created_at").Find(&users).Error
}

func (s *Store) User(id string) (*User, error) {
	return first[User](s.db.Where("id = ?", id))
}

// LocalUser finds the password account with that name.
func (s *Store) LocalUser(name string) (*User, error) {
	return first[User](s.db.Where("name = ? AND password_hash <> ''", name))
}

// AddLocalUser creates a password account. The name must not be taken by another one.
// AddLocalUser makes a password account. With admin or permissions it has that access
// from the start.
func (s *Store) AddLocalUser(name, password string, admin bool, permissions []string) (*User, error) {
	if _, err := s.LocalUser(name); err == nil {
		return nil, ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &User{ID: newID(), Name: name, PasswordHash: string(hash), Admin: admin, Permissions: storedPermissions(permissions)}
	return u, s.db.Create(u).Error
}

// FindUser takes an account id, or the name of a password account.
func (s *Store) FindUser(ref string) (*User, error) {
	u, err := s.User(ref)
	if errors.Is(err, ErrNotFound) {
		return s.LocalUser(ref)
	}
	return u, err
}

// otherAdmins counts the administrators besides the given account.
func otherAdmins(tx *gorm.DB, id string) (int64, error) {
	var n int64
	return n, tx.Model(&User{}).Where("admin = ? AND id <> ?", true, id).Count(&n).Error
}

// SetAccess replaces what an account may do. It refuses to leave no administrator.
func (s *Store) SetAccess(ref string, admin bool, permissions []string) (*User, error) {
	u, err := s.FindUser(ref)
	if err != nil {
		return nil, err
	}
	return u, s.db.Transaction(func(tx *gorm.DB) error {
		if u.Admin && !admin {
			if n, err := otherAdmins(tx, u.ID); err != nil {
				return err
			} else if n == 0 {
				return ErrLastAdmin
			}
		}
		u.Admin, u.Permissions = admin, storedPermissions(permissions)
		return tx.Model(u).Select("admin", "permissions").Updates(u).Error
	})
}

// SetPassword changes a password account's password and signs it out everywhere.
func (s *Store) SetPassword(ref, password string) error {
	u, err := s.FindUser(ref)
	if err != nil {
		return err
	}
	if u.PasswordHash == "" {
		return ErrNotFound
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(u).Update("password_hash", string(hash)).Error; err != nil {
			return err
		}
		return tx.Where("kind = ? AND owner = ?", KindSession, u.ID).Delete(&Token{}).Error
	})
}

// OIDCUser returns the account for an OIDC identity, creating it on first sign-in.
func (s *Store) OIDCUser(subject, name string) (*User, error) {
	u, err := first[User](s.db.Where("oidc_subject = ?", subject))
	if !errors.Is(err, ErrNotFound) {
		return u, err
	}
	u = &User{ID: newID(), Name: name, OIDCSubject: subject}
	return u, s.db.Create(u).Error
}

// DeleteUser removes a user and every token they hold. It refuses to remove the
// last administrator.
func (s *Store) DeleteUser(ref string) error {
	u, err := s.FindUser(ref)
	if err != nil {
		return err
	}
	id := u.ID
	return s.db.Transaction(func(tx *gorm.DB) error {
		if u.Admin {
			if n, err := otherAdmins(tx, id); err != nil {
				return err
			} else if n == 0 {
				return ErrLastAdmin
			}
		}
		if err := tx.Where("user_id = ?", id).Delete(&Avatar{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&Passkey{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&PushDevice{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&Channel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? OR host_id IN (?)", id, tx.Model(&Host{}).Select("id").Where("owner = ?", id)).Delete(&Snooze{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? OR host_id IN (?)", id, tx.Model(&Host{}).Select("id").Where("owner = ?", id)).Delete(&ThreadSeen{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? OR host_id IN (?)", id, tx.Model(&Host{}).Select("id").Where("owner = ?", id)).Delete(&HostMember{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&User{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("kind NOT IN ? AND owner = ?", hostKinds, id).Delete(&Token{}).Error; err != nil {
			return err
		}
		// Their personal hosts go with them, secrets included, so none can connect again.
		owned := tx.Model(&Host{}).Select("id").Where("owner = ?", id)
		if err := tx.Where("kind IN ? AND owner IN (?)", hostKinds, owned).Delete(&Token{}).Error; err != nil {
			return err
		}
		return tx.Where("owner = ?", id).Delete(&Host{}).Error
	})
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// NewToken stores a token and returns it with its secret, which is not kept.
// A zero ttl means it does not expire.
func (s *Store) NewToken(kind, owner, name string, ttl time.Duration) (*Token, string, error) {
	return newToken(s.db, kind, owner, name, ttl)
}

func newToken(tx *gorm.DB, kind, owner, name string, ttl time.Duration) (*Token, string, error) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	secret := "inaba_" + kind + "_" + hex.EncodeToString(b)
	t := &Token{ID: newID(), Kind: kind, Hash: hashSecret(secret), Owner: owner, Name: name}
	if ttl > 0 {
		expires := time.Now().Add(ttl)
		t.ExpiresAt = &expires
	}
	return t, secret, tx.Create(t).Error
}

// LookupToken returns the unexpired token for a secret.
func (s *Store) LookupToken(secret string) (*Token, error) {
	if secret == "" {
		return nil, ErrNotFound
	}
	return first[Token](s.db.Where("hash = ? AND (expires_at IS NULL OR expires_at > ?)", hashSecret(secret), time.Now()))
}

// Tokens lists tokens of a kind. An empty owner lists every owner's.
func (s *Store) Tokens(kind, owner string) ([]Token, error) {
	q := s.db.Where("kind = ?", kind).Order("created_at")
	if owner != "" {
		q = q.Where("owner = ?", owner)
	}
	var tokens []Token
	return tokens, q.Find(&tokens).Error
}

// DeleteToken removes a user's token. An empty owner removes it whoever owns it.
func (s *Store) DeleteToken(id, owner string) error {
	q := s.db.Where("id = ? AND kind NOT IN ?", id, hostKinds)
	if owner != "" {
		q = q.Where("owner = ?", owner)
	}
	res := q.Delete(&Token{})
	if res.Error == nil && res.RowsAffected == 0 {
		return ErrNotFound
	}
	return res.Error
}

// NewInvite makes an invite link into a host, by a user, that grants a role; see NewToken.
func (s *Store) NewInvite(hostID, by, role, note string, ttl time.Duration) (t *Token, secret string, err error) {
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if t, secret, err = newToken(tx, KindHostInvite, hostID, by, ttl); err != nil {
			return err
		}
		t.Role, t.Note = role, note
		return tx.Model(t).Updates(map[string]any{"role": role, "note": note}).Error
	})
	return t, secret, err
}

// DeleteInvite ends one of a host's invite links.
func (s *Store) DeleteInvite(hostID, id string) error {
	res := s.db.Where("id = ? AND kind = ? AND owner = ?", id, KindHostInvite, hostID).Delete(&Token{})
	if res.Error == nil && res.RowsAffected == 0 {
		return ErrNotFound
	}
	return res.Error
}

// AddHost registers a host and returns the secret it connects with.
// shortcut: the host keeps this secret for good; exchange a one-time enrollment code
// for it instead if secrets start travelling through chat or shell history.
func (s *Store) AddHost(name, owner string) (*Host, string, error) {
	h := &Host{ID: newID(), Name: name, Owner: owner}
	var secret string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(h).Error; err != nil {
			return err
		}
		var err error
		_, secret, err = newToken(tx, KindHost, h.ID, "", 0)
		return err
	})
	return h, secret, err
}

// Hosts returns every host, personal ones included.
func (s *Store) Hosts() ([]Host, error) {
	var hosts []Host
	return hosts, s.db.Order("created_at").Find(&hosts).Error
}

// HostsFor returns the hosts a user can use: the shared ones and their own.
func (s *Store) HostsFor(user string) ([]Host, error) {
	var hosts []Host
	invited := s.db.Model(&HostMember{}).Select("host_id").Where("user_id = ?", user)
	return hosts, s.db.Where("((owner = '' OR owner IS NULL) AND (restricted IS NULL OR NOT restricted)) OR owner = ? OR id IN (?)", user, invited).Order("created_at").Find(&hosts).Error
}

// HostMember is someone a personal host's owner let use it, by an invite link.
type HostMember struct {
	HostID string `gorm:"primaryKey"`
	UserID string `gorm:"primaryKey"`
	// Role is RoleMember or RoleManager.
	Role      string `gorm:"default:'member'"`
	CreatedAt time.Time
}

// MemberRole is a user's role on a host they were let into, "" for none.
func (s *Store) MemberRole(hostID, userID string) string {
	var m HostMember
	if err := s.db.Where("host_id = ? AND user_id = ?", hostID, userID).Limit(1).Find(&m).Error; err != nil || m.UserID == "" {
		return ""
	}
	if m.Role == "" {
		return RoleMember
	}
	return m.Role
}

// IsMember says whether a user was let into a host.
func (s *Store) IsMember(hostID, userID string) bool { return s.MemberRole(hostID, userID) != "" }

// AddMember lets a user use a host as a member; someone already in keeps their role.
func (s *Store) AddMember(hostID, userID string) error {
	return s.db.Where(HostMember{HostID: hostID, UserID: userID}).FirstOrCreate(&HostMember{HostID: hostID, UserID: userID, Role: RoleMember}).Error
}

// SetMember lets a user into a host with a role, or changes the role of one already in.
func (s *Store) SetMember(hostID, userID, role string) error {
	if err := s.AddMember(hostID, userID); err != nil {
		return err
	}
	return s.db.Model(&HostMember{}).Where("host_id = ? AND user_id = ?", hostID, userID).Update("role", role).Error
}

// MembershipsOf lists the hosts a user was let into.
func (s *Store) MembershipsOf(userID string) ([]HostMember, error) {
	var m []HostMember
	return m, s.db.Where("user_id = ?", userID).Find(&m).Error
}

// Members lists who was let into a host, oldest first.
func (s *Store) Members(hostID string) ([]HostMember, error) {
	var m []HostMember
	return m, s.db.Where("host_id = ?", hostID).Order("created_at").Find(&m).Error
}

func (s *Store) RemoveMember(hostID, userID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("host_id = ? AND user_id = ?", hostID, userID).Delete(&HostMember{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		// Their snoozes and read marks there mean nothing once they are out.
		if err := tx.Where("host_id = ? AND user_id = ?", hostID, userID).Delete(&Snooze{}).Error; err != nil {
			return err
		}
		return tx.Where("host_id = ? AND user_id = ?", hostID, userID).Delete(&ThreadSeen{}).Error
	})
}

func (s *Store) Host(id string) (*Host, error) {
	var h Host
	err := s.db.Where("id = ?", id).Take(&h).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &h, err
}

// UpdateHost changes the given columns of a host, such as name, icon and image.
func (s *Store) UpdateHost(id string, changes map[string]any) error {
	res := s.db.Model(&Host{}).Where("id = ?", id).Updates(changes)
	if res.Error == nil && res.RowsAffected == 0 {
		return ErrNotFound
	}
	return res.Error
}

// SetHostPlatform records what a host runs on.
// SawHost records that a host is or was just connected, and what it said of itself when given.
func (s *Store) SawHost(id string, system []byte) error {
	changes := map[string]any{"last_seen": time.Now()}
	if system != nil {
		changes["system"] = system
	}
	return s.db.Model(&Host{}).Where("id = ?", id).Updates(changes).Error
}

func (s *Store) SetHostPlatform(id, os, arch string) error {
	return s.db.Model(&Host{}).Where("id = ?", id).Updates(map[string]any{"os": os, "arch": arch}).Error
}

// DeleteHost removes a host and its secret.
func (s *Store) DeleteHost(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&Host{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("host_id = ?", id).Delete(&HostMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("host_id = ?", id).Delete(&Snooze{}).Error; err != nil {
			return err
		}
		if err := tx.Where("host_id = ?", id).Delete(&ThreadSeen{}).Error; err != nil {
			return err
		}
		return tx.Where("kind IN ? AND owner = ?", hostKinds, id).Delete(&Token{}).Error
	})
}

// SetAvatar replaces a user's picture; empty data removes it.
func (s *Store) SetAvatar(userID, contentType string, data []byte) (*User, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		version := int64(0)
		if len(data) > 0 {
			if err := tx.Save(&Avatar{UserID: userID, Type: contentType, Data: data}).Error; err != nil {
				return err
			}
			version = time.Now().UnixNano()
		} else if err := tx.Where("user_id = ?", userID).Delete(&Avatar{}).Error; err != nil {
			return err
		}
		res := tx.Model(&User{}).Where("id = ?", userID).Update("avatar_version", version)
		if res.Error == nil && res.RowsAffected == 0 {
			return ErrNotFound
		}
		return res.Error
	})
	if err != nil {
		return nil, err
	}
	return s.User(userID)
}

// Avatar returns a user's picture.
func (s *Store) Avatar(userID string) (*Avatar, error) {
	return first[Avatar](s.db.Where("user_id = ?", userID))
}

// UpdateUser saves changed columns of a user, such as display_name or totp_secret.
func (s *Store) UpdateUser(id string, changes map[string]any) (*User, error) {
	res := s.db.Model(&User{}).Where("id = ?", id).Updates(changes)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return s.User(id)
}

// ChangePassword sets a new password and ends every other browser sign-in of the
// account, keeping the one given.
func (s *Store) ChangePassword(userID, password, keepTokenID string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&User{}).Where("id = ?", userID).Update("password_hash", string(hash)).Error; err != nil {
			return err
		}
		return tx.Where("kind = ? AND owner = ? AND id <> ?", KindSession, userID, keepTokenID).Delete(&Token{}).Error
	})
}

func (s *Store) Passkeys(userID string) ([]Passkey, error) {
	var keys []Passkey
	return keys, s.db.Where("user_id = ?", userID).Order("created_at").Find(&keys).Error
}

func (s *Store) Passkey(id string) (*Passkey, error) {
	return first[Passkey](s.db.Where("id = ?", id))
}

// SavePasskey adds a passkey or records its use.
func (s *Store) SavePasskey(k *Passkey) error { return s.db.Save(k).Error }

// DeletePasskey removes one of a user's passkeys.
func (s *Store) DeletePasskey(userID, id string) error {
	res := s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&Passkey{})
	if res.Error == nil && res.RowsAffected == 0 {
		return ErrNotFound
	}
	return res.Error
}
