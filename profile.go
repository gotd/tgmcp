package main

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

func (s *server) handleUpdateProfile(ctx context.Context, _ *mcp.CallToolRequest, in updateProfileInput) (*mcp.CallToolResult, updateProfileOutput, error) {
	if in.FirstName == nil && in.LastName == nil && in.About == nil {
		return nil, updateProfileOutput{}, errors.New("at least one of first_name, last_name or about is required")
	}
	req := &tg.AccountUpdateProfileRequest{}
	if in.FirstName != nil {
		req.SetFirstName(*in.FirstName)
	}
	if in.LastName != nil {
		req.SetLastName(*in.LastName)
	}
	if in.About != nil {
		req.SetAbout(*in.About)
	}
	if _, err := s.api.AccountUpdateProfile(ctx, req); err != nil {
		return nil, updateProfileOutput{}, errors.Wrap(err, "update profile")
	}
	return nil, updateProfileOutput{OK: true}, nil
}

func (s *server) handleUpdateProfilePhoto(ctx context.Context, _ *mcp.CallToolRequest, in updateProfilePhotoInput) (*mcp.CallToolResult, updateProfilePhotoOutput, error) {
	if in.Path == "" {
		return nil, updateProfilePhotoOutput{}, errors.New("path is required")
	}
	root := s.fileRootVal
	if root == "" {
		return nil, updateProfilePhotoOutput{}, errors.New("TG_FILE_ROOT not configured")
	}
	abs, err := safeJoin(root, in.Path)
	if err != nil {
		return nil, updateProfilePhotoOutput{}, err
	}
	file, err := uploader.NewUploader(s.api).FromPath(ctx, abs)
	if err != nil {
		return nil, updateProfilePhotoOutput{}, errors.Wrap(err, "upload photo")
	}
	if _, err := s.api.PhotosUploadProfilePhoto(ctx, &tg.PhotosUploadProfilePhotoRequest{File: file}); err != nil {
		return nil, updateProfilePhotoOutput{}, errors.Wrap(err, "update profile photo")
	}
	return nil, updateProfilePhotoOutput{OK: true}, nil
}
