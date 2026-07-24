package container

import "github.com/terrakuh/devc/config"

// Action is what `up` must do to bring a workspace container to running state.
type Action int

const (
	// ActionCreate: no container exists; build (if needed) and run.
	ActionCreate Action = iota
	// ActionStart: a stopped container matches; start it.
	ActionStart
	// ActionAttach: a running container matches; nothing to do.
	ActionAttach
	// ActionRecreate: a container exists but its config hash differs; the
	// caller removes and recreates it (only when recreate is allowed).
	ActionRecreate
	// ActionDrift: a container exists, config changed, but recreate was not
	// requested; the caller warns and attaches/starts the existing one.
	ActionDrift
)

// Decide chooses the Action for an existing container (nil = none) given the
// desired spec and whether the user allowed recreation.
func Decide(existing *Info, spec *config.Spec, allowRecreate bool) Action {
	if existing == nil {
		return ActionCreate
	}
	hashMatches := existing.Config.Labels[LabelConfigHash] == ConfigHash(spec)
	if !hashMatches {
		if allowRecreate {
			return ActionRecreate
		}
		return ActionDrift
	}
	if existing.Running() {
		return ActionAttach
	}
	return ActionStart
}
