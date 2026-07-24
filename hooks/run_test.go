package hooks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/terrakuh/devc/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is an Executor that records the argvs it ran.
type recorder struct {
	mu    sync.Mutex
	calls [][]string
	fail  map[string]error // keyed by argv[len-1] to fail specific commands
}

func (r *recorder) exec(_ context.Context, argv []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, argv)
	if r.fail != nil && len(argv) > 0 {
		if err, ok := r.fail[argv[len(argv)-1]]; ok {
			return err
		}
	}
	return nil
}

func shellCmd(s string) config.Command { return config.Command{Kind: config.CommandShell, Shell: s} }
func argvCmd(a ...string) config.Command {
	return config.Command{Kind: config.CommandArgv, Argv: a}
}

func TestRunShellForm(t *testing.T) {
	r := &recorder{}
	require.NoError(t, Run(context.Background(), shellCmd("echo hi"), r.exec))
	require.Len(t, r.calls, 1)
	assert.Equal(t, []string{"/bin/sh", "-c", "echo hi"}, r.calls[0])
}

func TestRunArgvForm(t *testing.T) {
	r := &recorder{}
	require.NoError(t, Run(context.Background(), argvCmd("go", "version"), r.exec))
	require.Len(t, r.calls, 1)
	assert.Equal(t, []string{"go", "version"}, r.calls[0])
}

func TestRunNoneIsNoop(t *testing.T) {
	r := &recorder{}
	require.NoError(t, Run(context.Background(), config.Command{Kind: config.CommandNone}, r.exec))
	assert.Empty(t, r.calls)
}

func TestRunNamedParallelAllRun(t *testing.T) {
	r := &recorder{}
	cmd := config.Command{Kind: config.CommandNamed, Named: map[string]config.Command{
		"a": shellCmd("echo a"),
		"b": argvCmd("echo", "b"),
		"c": shellCmd("echo c"),
	}}
	require.NoError(t, Run(context.Background(), cmd, r.exec))
	assert.Len(t, r.calls, 3, "every named entry must run")
}

func TestRunNamedFailsIfAnyFails(t *testing.T) {
	r := &recorder{fail: map[string]error{"boom": errors.New("kaboom")}}
	cmd := config.Command{Kind: config.CommandNamed, Named: map[string]config.Command{
		"ok":  shellCmd("echo ok"),
		"bad": shellCmd("boom"),
	}}
	err := Run(context.Background(), cmd, r.exec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestRunNamedConcurrent(t *testing.T) {
	// Confirm entries actually run concurrently: each blocks until all have
	// started. With serial execution this would deadlock (caught by timeout).
	const n = 3
	var started atomic.Int32
	release := make(chan struct{})
	exec := func(_ context.Context, _ []string) error {
		started.Add(1)
		<-release
		return nil
	}
	cmd := config.Command{Kind: config.CommandNamed, Named: map[string]config.Command{
		"a": shellCmd("a"), "b": shellCmd("b"), "c": shellCmd("c"),
	}}
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cmd, exec) }()

	assert.Eventually(t, func() bool { return started.Load() == n }, time.Second, 5*time.Millisecond,
		"all entries should start before any finishes")
	close(release)
	require.NoError(t, <-done)
}
