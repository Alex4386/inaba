package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/store"
)

// maxAvatar bounds an account's picture; it is shown to everyone signed in.
const maxAvatar = 256 << 10

var avatarTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

func (s *Server) UpdateProfile(ctx context.Context, req *inabav1.UpdateProfileRequest) (*inabav1.User, error) {
	p := currentUser(ctx)
	if p == nil || p.user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, "sign in to change your profile")
	}
	u := p.user
	if req.Avatar != nil {
		var contentType string
		var data []byte
		if *req.Avatar != "" {
			head, payload, ok := strings.Cut(*req.Avatar, ";base64,")
			contentType = strings.TrimPrefix(head, "data:")
			var err error
			data, err = base64.StdEncoding.DecodeString(payload)
			if !ok || !strings.HasPrefix(head, "data:") || !avatarTypes[contentType] || err != nil || len(data) == 0 {
				return nil, connect.NewError(connect.CodeInvalidArgument, "a picture is a PNG, JPEG or WebP image, as a data: URL")
			}
			if len(data) > maxAvatar {
				return nil, connect.NewError(connect.CodeInvalidArgument, "a picture is at most 256 KiB")
			}
		}
		var err error
		if u, err = s.store.SetAvatar(u.ID, contentType, data); err != nil {
			return nil, internal("could not save the picture", err)
		}
	}
	if req.Stealth != nil {
		if !u.Admin {
			return nil, connect.NewError(connect.CodePermissionDenied, "stealth mode is for administrators")
		}
		var err error
		if u, err = s.store.UpdateUser(u.ID, map[string]any{"stealth": *req.Stealth}); err != nil {
			return nil, internal("could not save stealth mode", err)
		}
	}
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if len(name) > 64 {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a display name is at most 64 characters")
		}
		var err error
		if u, err = s.store.UpdateUser(u.ID, map[string]any{"display_name": name}); err != nil {
			return nil, internal("could not save the name", err)
		}
	}
	return userMessage(u), nil
}

// serveAvatar sends an account's picture to anyone signed in. Its address carries a
// version, so the browser keeps it until the picture changes.
func (s *Server) serveAvatar(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Avatar(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not read the picture")
		return
	}
	w.Header().Set("Content-Type", a.Type)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(a.Data)
}
