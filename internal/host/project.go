package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"google.golang.org/protobuf/encoding/protojson"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/db"
	"github.com/Alex4386/inaba/internal/rpc"
)

// Project is a folder on this host that threads are grouped under.
type Project struct {
	ID   string `gorm:"primaryKey"`
	Name string
	Cwd  string
	// Icon is a picture as a data: URL; Pinned puts the project first.
	Icon   string
	Pinned bool
	// Archived puts the project away, out of the sidebar until archived ones are shown.
	Archived bool `gorm:"default:false"`
	// NewThread is the NewThreadDefaults message as JSON, in the column it always had.
	NewThread string `gorm:"column:new_thread"`
	CreatedAt time.Time
}

func (p *Project) message() *inabav1.Project {
	m := &inabav1.Project{Id: p.ID, Name: p.Name, Cwd: p.Cwd, CreatedAt: timestamppb.New(p.CreatedAt), Icon: p.Icon, Pinned: p.Pinned, Archived: p.Archived}
	if p.NewThread != "" {
		d := &inabav1.NewThreadDefaults{}
		if protojson.Unmarshal([]byte(p.NewThread), d) == nil {
			m.NewThread = d
		}
	}
	return m
}

// Projects is the host's saved project list. The running host and the inaba CLI
// both work on the same file, so a change by one is seen by the other at once.
// Projects outlive the host process; the threads under them do not.
type Projects struct{ db *gorm.DB }

func OpenProjects(path string) (*Projects, error) {
	g, err := db.Open(path, &Project{})
	if err != nil {
		return nil, err
	}
	return &Projects{db: g}, nil
}

// Add saves a project for a folder, named after the folder unless a name is given.
func (p *Projects) Add(name, cwd string) (*Project, error) {
	if err := checkDir(cwd); err != nil {
		return nil, err
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	project := &Project{ID: hex.EncodeToString(b), Name: name, Cwd: filepath.Clean(cwd)}
	if project.Name == "" {
		project.Name = filepath.Base(project.Cwd)
	}
	return project, p.db.Create(project).Error
}

func (p *Projects) List() ([]Project, error) {
	var projects []Project
	return projects, p.db.Order("pinned DESC, created_at").Find(&projects).Error
}

// Get returns the project, or nil when there is none with that id.
func (p *Projects) Get(id string) (*Project, error) {
	var projects []Project
	if err := p.db.Where("id = ?", id).Limit(1).Find(&projects).Error; err != nil || len(projects) == 0 {
		return nil, err
	}
	return &projects[0], nil
}

// Update saves changed columns of a project.
func (p *Projects) Update(id string, changes map[string]any) (bool, error) {
	res := p.db.Model(&Project{}).Where("id = ?", id).Updates(changes)
	return res.RowsAffected > 0, res.Error
}

// Delete reports whether there was a project to delete.
func (p *Projects) Delete(id string) (bool, error) {
	res := p.db.Where("id = ?", id).Delete(&Project{})
	return res.RowsAffected > 0, res.Error
}

// LoadProjects opens the project database the host keeps its projects in.
func (h *Host) LoadProjects(path string) error {
	projects, err := OpenProjects(path)
	h.projects = projects
	return err
}

var errNoProjects = connect.NewError(connect.CodeFailedPrecondition, "this host was started without a project database")

func (h *Host) CreateProject(ctx context.Context, req *inabav1.CreateProjectRequest) (*inabav1.Project, error) {
	if err := may(ctx, rpc.PermProjects); err != nil {
		return nil, err
	}
	if h.projects == nil {
		return nil, errNoProjects
	}
	project, err := h.projects.Add(req.Name, req.Cwd)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, "could not save the project").WithCause(err)
	}
	return project.message(), nil
}

var validPicture = regexp.MustCompile(`^data:image/(png|jpeg|webp);base64,[A-Za-z0-9+/=]+$`)

// maxPicture bounds a project's picture as a data: URL; it travels with every project list.
const maxPicture = 128 << 10 * 4 / 3

func (h *Host) UpdateProject(ctx context.Context, req *inabav1.UpdateProjectRequest) (*inabav1.Project, error) {
	if err := may(ctx, rpc.PermProjects); err != nil {
		return nil, err
	}
	if h.projects == nil {
		return nil, errNoProjects
	}
	changes := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 64 {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a name is 1 to 64 characters")
		}
		changes["name"] = name
	}
	if req.Icon != nil {
		if *req.Icon != "" && (len(*req.Icon) > maxPicture || !validPicture.MatchString(*req.Icon)) {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a picture is a PNG, JPEG or WebP image of at most 128 KiB, as a data: URL")
		}
		changes["icon"] = *req.Icon
	}
	if req.Pinned != nil {
		changes["pinned"] = *req.Pinned
	}
	if req.Archived != nil {
		changes["archived"] = *req.Archived
	}
	if req.NewThread != nil {
		b, err := protojson.Marshal(req.NewThread)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
		}
		changes["new_thread"] = string(b)
	}
	if len(changes) > 0 {
		found, err := h.projects.Update(req.ProjectId, changes)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, "could not save the change").WithCause(err)
		}
		if !found {
			return nil, connect.NewError(connect.CodeNotFound, "no project "+req.ProjectId)
		}
	}
	p, err := h.projects.Get(req.ProjectId)
	if err != nil || p == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no project "+req.ProjectId)
	}
	return p.message(), nil
}

func (h *Host) ListProjects(context.Context, *inabav1.ListProjectsRequest) (*inabav1.ListProjectsResponse, error) {
	res := &inabav1.ListProjectsResponse{}
	if h.projects == nil {
		return res, nil
	}
	projects, err := h.projects.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "could not read the projects").WithCause(err)
	}
	for i := range projects {
		res.Projects = append(res.Projects, projects[i].message())
	}
	return res, nil
}

// DeleteProject forgets the project. Its threads keep running; clients show a thread
// whose project is gone as one without a project.
func (h *Host) DeleteProject(ctx context.Context, req *inabav1.DeleteProjectRequest) (*inabav1.DeleteProjectResponse, error) {
	if err := may(ctx, rpc.PermProjects); err != nil {
		return nil, err
	}
	if h.projects == nil {
		return nil, errNoProjects
	}
	found, err := h.projects.Delete(req.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, "could not save the change").WithCause(err)
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, "no project "+req.ProjectId)
	}
	return &inabav1.DeleteProjectResponse{}, nil
}

// ListDirectory lets a caller who may add projects browse for the folder.
func (h *Host) ListDirectory(ctx context.Context, req *inabav1.ListDirectoryRequest) (*inabav1.ListDirectoryResponse, error) {
	if err := may(ctx, rpc.PermProjects); err != nil {
		return nil, err
	}
	path := expandHome(req.Path)
	if err := checkDir(path); err != nil {
		return nil, err
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, "the host cannot read that folder")
	}
	res := &inabav1.ListDirectoryResponse{Path: path}
	if parent := filepath.Dir(path); parent != path {
		res.Parent = parent
	}
	for _, e := range entries {
		// Follow links, so a linked folder can be chosen like any other.
		info, err := os.Stat(filepath.Join(path, e.Name()))
		if err != nil || !info.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		_, gitErr := os.Stat(filepath.Join(path, e.Name(), ".git"))
		res.Entries = append(res.Entries, &inabav1.ListDirectoryResponse_Entry{Name: e.Name(), Git: gitErr == nil})
	}
	return res, nil
}

// expandHome reads "", "~" and "~/…" (or "~\\…" on Windows) against the host's home folder, as a shell would.
func expandHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "" || path == "~" {
		return home
	}
	if len(path) > 1 && path[0] == '~' && os.IsPathSeparator(path[1]) {
		return filepath.Join(home, path[2:])
	}
	return path
}
