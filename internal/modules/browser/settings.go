package browser

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"connectrpc.com/connect/v2"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/rpc"
)

// settingsRow keeps the host's browser settings, one row.
type settingsRow struct {
	ID   string `gorm:"primaryKey"`
	Data []byte
}

func (settingsRow) TableName() string { return "browser_settings" }

const (
	maxFlags    = 32
	maxFlagLen  = 1024
	maxProjects = 256
	// projectKey prefixes the key of a browser a project's threads share; thread keys are thread ids.
	projectKey = "project-"
)

var flagName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// reserved are the switches inaba sets to drive the browser; changing them would cut it off.
var reserved = []string{"user-data-dir", "remote-debugging-"}

func loadSettings(db *gorm.DB) (*inabav1.BrowserSettings, error) {
	set := &inabav1.BrowserSettings{}
	if db == nil {
		return set, nil
	}
	if err := db.AutoMigrate(&settingsRow{}); err != nil {
		return nil, err
	}
	var r settingsRow
	if err := db.Limit(1).Find(&r, "id = ?", "settings").Error; err != nil {
		return nil, err
	}
	if len(r.Data) > 0 {
		if err := proto.Unmarshal(r.Data, set); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func (s *service) GetBrowserSettings(ctx context.Context, _ *inabav1.GetBrowserSettingsRequest) (*inabav1.BrowserSettings, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.Clone(s.settings).(*inabav1.BrowserSettings), nil
}

func (s *service) SaveBrowserSettings(ctx context.Context, req *inabav1.BrowserSettings) (*inabav1.BrowserSettings, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	set := proto.Clone(req).(*inabav1.BrowserSettings)
	if set.Defaults == nil {
		set.Defaults = &inabav1.BrowserProfile{}
	}
	if len(set.Projects) > maxProjects {
		return nil, connect.NewError(connect.CodeInvalidArgument, "settings for at most 256 projects")
	}
	if err := cleanProfile(set.Defaults); err != nil {
		return nil, err
	}
	for id, p := range set.Projects {
		if id == "" || len(id) > 64 || p == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a project's settings need its id")
		}
		if err := cleanProfile(p); err != nil {
			return nil, err
		}
		// A project left with nothing of its own takes the host's settings; no need to keep it.
		if len(p.Flags) == 0 && p.Sharing == inabav1.BrowserSharing_BROWSER_SHARING_UNSPECIFIED {
			delete(set.Projects, id)
		}
	}
	data, err := proto.Marshal(set)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		if err := s.db.Save(&settingsRow{ID: "settings", Data: data}).Error; err != nil {
			return nil, connect.NewError(connect.CodeInternal, "could not save the browser settings: "+err.Error())
		}
	}
	s.settings = set
	return proto.Clone(set).(*inabav1.BrowserSettings), nil
}

// cleanProfile checks a profile's flags and writes them the one way: no leading dashes, no blanks.
func cleanProfile(p *inabav1.BrowserProfile) error {
	var out []string
	for _, f := range p.Flags {
		f = strings.TrimLeft(strings.TrimSpace(f), "-")
		if f == "" {
			continue
		}
		name, value, _ := strings.Cut(f, "=")
		if !flagName.MatchString(name) {
			return connect.NewError(connect.CodeInvalidArgument, "not a Chrome switch: "+f)
		}
		for _, r := range reserved {
			if name == r || (strings.HasSuffix(r, "-") && strings.HasPrefix(name, r)) {
				return connect.NewError(connect.CodeInvalidArgument, "inaba sets --"+name+" itself, to drive the browser")
			}
		}
		if len(f) > maxFlagLen || strings.ContainsFunc(value, unicode.IsControl) {
			return connect.NewError(connect.CodeInvalidArgument, "a switch's value is one line of at most 1024 characters")
		}
		out = append(out, f)
	}
	if len(out) > maxFlags {
		return connect.NewError(connect.CodeInvalidArgument, "at most 32 switches")
	}
	p.Flags = out
	return nil
}

// plan is how a thread's browser starts: which browser it is, by key, and its switches.
type plan struct {
	key   string
	flags []string
}

// planFor works out the thread's browser from the host's settings and its project's. Called with s.mu held.
func (s *service) planFor(t *inabav1.Thread) plan {
	def := s.settings.GetDefaults()
	proj := s.settings.GetProjects()[t.ProjectId]
	sharing := def.GetSharing()
	if proj.GetSharing() != inabav1.BrowserSharing_BROWSER_SHARING_UNSPECIFIED {
		sharing = proj.GetSharing()
	}
	p := plan{key: t.Id, flags: mergeFlags(def.GetFlags(), proj.GetFlags())}
	if sharing == inabav1.BrowserSharing_BROWSER_SHARING_PROJECT && t.ProjectId != "" {
		p.key = projectKey + t.ProjectId
	}
	return p
}

// mergeFlags puts the project's switches after the host's, a project's replacing the host's of the same name.
func mergeFlags(host, project []string) []string {
	mine := map[string]bool{}
	for _, f := range project {
		name, _, _ := strings.Cut(f, "=")
		mine[name] = true
	}
	var out []string
	for _, f := range host {
		if name, _, _ := strings.Cut(f, "="); !mine[name] {
			out = append(out, f)
		}
	}
	return append(out, project...)
}

// withFlags sets the switches on a launcher, after its own, so they win.
func withFlags(l *launcher.Launcher, switches []string) *launcher.Launcher {
	for _, f := range switches {
		name, value, hasValue := strings.Cut(f, "=")
		if hasValue {
			l = l.Set(flags.Flag(name), value)
		} else {
			l = l.Set(flags.Flag(name))
		}
	}
	return l
}
