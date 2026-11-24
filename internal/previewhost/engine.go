package previewhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

const (
	LabelPreviewHost = "com.ourplatform.preview-host"
	LabelPreviewID   = "com.ourplatform.preview-id"
)

func PreviewLabels(runnerID, previewID string) map[string]string {
	return map[string]string{LabelPreviewHost: runnerID, LabelPreviewID: previewID}
}

func ResourcePrefix(runnerID, previewID string) string {
	digest := sha256.Sum256([]byte(runnerID + "\x00" + previewID))
	return "our-preview-" + hex.EncodeToString(digest[:])[:12]
}

type NetworkSpec struct {
	Name     string
	Labels   map[string]string
	Internal bool
}

type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type ContainerSpec struct {
	Name         string
	Image        string
	Network      string
	NetworkAlias string
	Labels       map[string]string
	Env          map[string]string
	User         string
	CapAdd       []string
	ReadOnly     bool
	Tmpfs        []string
	Mounts       []Mount
	WorkDir      string
	MemoryMB     int
	CPUs         float64
	PidsLimit    int
	PublishPort  int
	Entrypoint   string
	Command      []string
}

type Objects struct {
	Containers []string
	Networks   []string
	Volumes    []string
}

func (objects Objects) Empty() bool {
	return len(objects.Containers) == 0 && len(objects.Networks) == 0 && len(objects.Volumes) == 0
}

type Engine interface {
	ImagePresent(ctx context.Context, image string) (bool, error)
	Pull(ctx context.Context, image string) (string, error)
	CreateNetwork(ctx context.Context, spec NetworkSpec) error
	RunDetached(ctx context.Context, spec ContainerSpec) error
	RunOnce(ctx context.Context, spec ContainerSpec) (string, error)
	Exec(ctx context.Context, container string, command []string) (string, error)
	Running(ctx context.Context, container string) (bool, error)
	HostPort(ctx context.Context, container string, port int) (int, error)
	Logs(ctx context.Context, container string) (string, error)
	List(ctx context.Context, labels map[string]string) (Objects, error)
	Remove(ctx context.Context, objects Objects) error
	HostPreviewIDs(ctx context.Context, runnerID string) ([]string, error)
}
