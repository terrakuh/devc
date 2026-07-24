// Package container turns a resolved config.Spec into container-runtime
// operations: build, create, start, reuse, exec, and inspection. Everything is
// argv construction over a runtime.Runner, so it is exercised end-to-end with a
// FakeRunner and no real container daemon.
package container

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/terrakuh/devc/config"
)

// Label keys stamped on every object devc creates, so workspaces can be found
// and their drift detected without any local state file.
const (
	LabelID         = "com.github.terrakuh.devc.id"
	LabelName       = "com.github.terrakuh.devc.name"
	LabelLocal      = "com.github.terrakuh.devc.local"
	LabelConfigHash = "com.github.terrakuh.devc.config-hash"
)

// ContainerName is the deterministic container name for a workspace.
func ContainerName(spec *config.Spec) string { return "devc-" + spec.ID }

// Labels returns the label set for a workspace's container.
func Labels(spec *config.Spec) map[string]string {
	return map[string]string{
		LabelID:         spec.ID,
		LabelName:       spec.Name,
		LabelLocal:      spec.LocalWorkspaceFolder,
		LabelConfigHash: ConfigHash(spec),
	}
}

// ConfigHash is a stable digest of the resolved Spec. A change means the
// devcontainer.json changed in a way that warrants recreating the container.
// The ID and ConfigPath are excluded so the hash reflects behaviour, not the
// workspace's location.
func ConfigHash(spec *config.Spec) string {
	clone := *spec
	clone.ID = ""
	clone.ConfigPath = ""
	clone.LocalWorkspaceFolder = ""
	b, err := json.Marshal(clone)
	if err != nil {
		// Spec is plain data; marshalling cannot realistically fail.
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// labelArgs renders a label map as repeated --label k=v flags, in a stable
// order (LabelID, LabelName, LabelLocal, LabelConfigHash) for deterministic argv.
func labelArgs(labels map[string]string) []string {
	order := []string{LabelID, LabelName, LabelLocal, LabelConfigHash}
	args := make([]string, 0, len(order)*2)
	for _, k := range order {
		if v, ok := labels[k]; ok {
			args = append(args, "--label", k+"="+v)
		}
	}
	return args
}
