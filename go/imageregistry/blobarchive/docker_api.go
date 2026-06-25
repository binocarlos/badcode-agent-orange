package blobarchive

// docker_api.go — narrow dockerAPI interface for the blobarchive adapter, plus
// the real implementation wrapper.
//
// Blobarchive needs: ImageSave (full-archive path), ImageLoad (Materialize),
// ImageList (Resolve check — future), ImageRemove (cleanup). The fake (in
// blobarchive_test.go) implements only this interface, keeping hermetic tests
// daemon-free.

import (
	"context"
	"io"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// dockerAPI is the narrow subset of the moby client used by blobarchive.
type dockerAPI interface {
	ImageSave(ctx context.Context, imageIDs []string) (io.ReadCloser, error)
	ImageLoad(ctx context.Context, input io.Reader, quiet bool) (dockertypes.ImageLoadResponse, error)
	ImageList(ctx context.Context, options dockertypes.ImageListOptions) ([]dockertypes.ImageSummary, error)
	ImageRemove(ctx context.Context, imageID string, options dockertypes.ImageRemoveOptions) ([]dockertypes.ImageDeleteResponseItem, error)
}

// realDockerClient wraps *client.Client and satisfies dockerAPI.
type realDockerClient struct {
	c *client.Client
}

func (r *realDockerClient) ImageSave(ctx context.Context, imageIDs []string) (io.ReadCloser, error) {
	return r.c.ImageSave(ctx, imageIDs)
}
func (r *realDockerClient) ImageLoad(ctx context.Context, input io.Reader, quiet bool) (dockertypes.ImageLoadResponse, error) {
	return r.c.ImageLoad(ctx, input, quiet)
}
func (r *realDockerClient) ImageList(ctx context.Context, options dockertypes.ImageListOptions) ([]dockertypes.ImageSummary, error) {
	return r.c.ImageList(ctx, options)
}
func (r *realDockerClient) ImageRemove(ctx context.Context, imageID string, options dockertypes.ImageRemoveOptions) ([]dockertypes.ImageDeleteResponseItem, error) {
	return r.c.ImageRemove(ctx, imageID, options)
}
