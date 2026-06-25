package ociregistry

// docker_api.go — narrow dockerAPI interface for the ociregistry adapter, plus
// the real implementation wrapper.
//
// ociregistry needs: ContainerCommit (snapshot), ImagePush (Persist),
// ImagePull (EnsurePresent/Materialize), ImageTag (tag before push),
// ImageInspectWithRaw (Resolve — check if ref exists locally).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// dockerAPI is the narrow subset of the moby client used by ociregistry.
type dockerAPI interface {
	ContainerCommit(ctx context.Context, container string, options dockertypes.ContainerCommitOptions) (dockertypes.IDResponse, error)
	ImageTag(ctx context.Context, image, ref string) error
	ImagePush(ctx context.Context, ref string, options dockertypes.ImagePushOptions) (io.ReadCloser, error)
	ImagePull(ctx context.Context, ref string, options dockertypes.ImagePullOptions) (io.ReadCloser, error)
	ImageInspectWithRaw(ctx context.Context, imageID string) (dockertypes.ImageInspect, []byte, error)
}

// realDockerClient wraps *client.Client and satisfies dockerAPI.
type realDockerClient struct {
	c *client.Client
}

func (r *realDockerClient) ContainerCommit(ctx context.Context, container string, options dockertypes.ContainerCommitOptions) (dockertypes.IDResponse, error) {
	return r.c.ContainerCommit(ctx, container, options)
}
func (r *realDockerClient) ImageTag(ctx context.Context, image, ref string) error {
	return r.c.ImageTag(ctx, image, ref)
}
func (r *realDockerClient) ImagePush(ctx context.Context, ref string, options dockertypes.ImagePushOptions) (io.ReadCloser, error) {
	return r.c.ImagePush(ctx, ref, options)
}
func (r *realDockerClient) ImagePull(ctx context.Context, ref string, options dockertypes.ImagePullOptions) (io.ReadCloser, error) {
	return r.c.ImagePull(ctx, ref, options)
}
func (r *realDockerClient) ImageInspectWithRaw(ctx context.Context, imageID string) (dockertypes.ImageInspect, []byte, error) {
	return r.c.ImageInspectWithRaw(ctx, imageID)
}

// encodeRegistryAuth base64-encodes a minimal registry auth JSON for the X-Registry-Auth
// header. The moby daemon REQUIRES this header to be present and valid base64 JSON on
// ImagePush/ImagePull — even for an unauthenticated registry. With empty credentials we
// therefore return an anonymous auth config (base64 of "{}"), NOT an empty string; an
// empty string makes the client omit the header and the daemon rejects the request with
// "missing X-Registry-Auth". This is what lets the local registry:2 (no auth) work.
func encodeRegistryAuth(username, password string) string {
	if username == "" && password == "" {
		return base64.URLEncoding.EncodeToString([]byte("{}"))
	}
	auth := map[string]string{"username": username, "password": password}
	b, _ := json.Marshal(auth)
	return base64.URLEncoding.EncodeToString(b)
}
