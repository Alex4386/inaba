// Package mcp is the MCP servers module: MCP servers a host gives its threads' agents.
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"
	"go.uber.org/fx"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
	"github.com/Alex4386/inaba/pkg/provider"
)

// Module is the MCP servers module, for a host to run.
var Module = module.Module{
	Name:        "mcp",
	Title:       "MCP servers",
	Description: "MCP servers to give the agents of this host's threads, for every thread or for chosen projects.",
	Default:     true,
	Option: fx.Options(
		fx.Provide(newService),
		module.Provide((*service).contribution),
	),
}

type row struct {
	ID   string `gorm:"primaryKey"`
	Data []byte
}

func (row) TableName() string { return "mcp_servers" }

type service struct {
	inabav1connect.UnimplementedMcpServiceHandler
	db *gorm.DB

	mu      sync.Mutex
	servers map[string]*inabav1.McpServer
}

func newService(store module.Store) (*service, error) {
	s := &service{db: store.DB, servers: map[string]*inabav1.McpServer{}}
	if s.db == nil {
		return s, nil
	}
	if err := s.db.AutoMigrate(&row{}); err != nil {
		return nil, err
	}
	var rows []row
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		m := &inabav1.McpServer{}
		if err := proto.Unmarshal(r.Data, m); err != nil {
			return nil, err
		}
		s.servers[m.Id] = m
	}
	return s, nil
}

func (s *service) contribution() module.Contribution {
	return module.Contribution{
		Services:   func(server *connect.Server) { inabav1connect.RegisterMcpServiceHandler(server, s) },
		MCPServers: s.forThread,
	}
}

// forThread is the servers that apply to the thread, as an agent takes them.
func (s *service) forThread(thread *inabav1.Thread) []provider.MCPServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provider.MCPServer
	for _, m := range s.sorted() {
		if !m.Enabled || (len(m.ProjectIds) > 0 && !slices.Contains(m.ProjectIds, thread.GetProjectId())) {
			continue
		}
		out = append(out, provider.MCPServer{Name: m.Name, Command: m.Command, Args: m.Args, Env: m.Env, URL: m.Url, Headers: m.Headers})
	}
	return out
}

// sorted is the servers oldest first; the caller holds s.mu.
func (s *service) sorted() []*inabav1.McpServer {
	out := make([]*inabav1.McpServer, 0, len(s.servers))
	for _, m := range s.servers {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b *inabav1.McpServer) int { return a.CreatedAt.AsTime().Compare(b.CreatedAt.AsTime()) })
	return out
}

// check reports what makes a server unusable, or one name used twice.
func (s *service) check(m *inabav1.McpServer) error {
	bad := func(msg string) error { return connect.NewError(connect.CodeInvalidArgument, msg) }
	if strings.TrimSpace(m.Name) == "" {
		return bad("an MCP server needs a name")
	}
	if (m.Command == "") == (m.Url == "") {
		return bad("an MCP server is a command or an address: set one of them")
	}
	if m.Url != "" {
		if u, err := url.Parse(m.Url); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bad("the address must be http or https")
		}
	}
	for _, other := range s.servers {
		if other.Id != m.Id && other.Name == m.Name {
			return connect.NewError(connect.CodeAlreadyExists, "there is already an MCP server named "+m.Name)
		}
	}
	return nil
}

func (s *service) save(m *inabav1.McpServer) {
	if s.db == nil {
		return
	}
	data, _ := proto.Marshal(m)
	if err := s.db.Save(&row{ID: m.Id, Data: data}).Error; err != nil {
		slog.Error("could not save an MCP server", "server", m.Id, "error", err.Error())
	}
}

func (s *service) ListMcpServers(ctx context.Context, _ *inabav1.ListMcpServersRequest) (*inabav1.ListMcpServersResponse, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &inabav1.ListMcpServersResponse{}
	for _, m := range s.sorted() {
		res.Servers = append(res.Servers, proto.Clone(m).(*inabav1.McpServer))
	}
	return res, nil
}

func (s *service) CreateMcpServer(ctx context.Context, req *inabav1.McpServer) (*inabav1.McpServer, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	m := proto.Clone(req).(*inabav1.McpServer)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	m.Id, m.CreatedAt = hex.EncodeToString(b), timestamppb.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(m); err != nil {
		return nil, err
	}
	s.servers[m.Id] = m
	s.save(m)
	return proto.Clone(m).(*inabav1.McpServer), nil
}

func (s *service) UpdateMcpServer(ctx context.Context, req *inabav1.McpServer) (*inabav1.McpServer, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.servers[req.Id]
	if old == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no MCP server "+req.Id)
	}
	m := proto.Clone(req).(*inabav1.McpServer)
	m.CreatedAt = old.CreatedAt
	if err := s.check(m); err != nil {
		return nil, err
	}
	s.servers[m.Id] = m
	s.save(m)
	return proto.Clone(m).(*inabav1.McpServer), nil
}

func (s *service) DeleteMcpServer(ctx context.Context, req *inabav1.DeleteMcpServerRequest) (*inabav1.DeleteMcpServerResponse, error) {
	if err := rpc.May(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.servers[req.Id] == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no MCP server "+req.Id)
	}
	delete(s.servers, req.Id)
	if s.db != nil {
		_ = s.db.Delete(&row{ID: req.Id}).Error
	}
	return &inabav1.DeleteMcpServerResponse{}, nil
}
