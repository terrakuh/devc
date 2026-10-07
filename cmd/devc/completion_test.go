package main

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func values(cands []string) []string {
	var out []string
	for _, c := range cands {
		v, _, _ := strings.Cut(c, "\t")
		out = append(out, v)
	}
	return out
}

func TestCompletionCommandsMatchUsage(t *testing.T) {
	// Every command the help text lists must complete, and vice versa.
	listed := regexp.MustCompile(`(?m)^  ([a-z-]+)  `).FindAllStringSubmatch(usage, -1)
	var want []string
	for _, m := range listed {
		want = append(want, m[1])
	}
	var got []string
	for _, c := range completionCommands {
		if c.name != "version" { // works, but is not in the help text
			got = append(got, c.name)
		}
	}
	assert.ElementsMatch(t, want, got)
}

func TestCommandFlagsCapturesRealFlagSet(t *testing.T) {
	for _, c := range completionCommands {
		if c.run == nil {
			continue
		}
		fs := commandFlags(c.name)
		require.NotNil(t, fs, "%s: running with -h should only declare its flags", c.name)
		assert.Equal(t, c.name, fs.Name())
	}
	fs := commandFlags("up")
	assert.NotNil(t, fs.Lookup("rebuild"))
	assert.NotNil(t, fs.Lookup("path"), "common flags are included")
}

func TestComplete(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		words []string
		want  []string
	}{
		{"command prefix", []string{"st"}, []string{"stop", "status"}},
		{"shells", []string{"completion", ""}, []string{"bash", "zsh", "fish"}},
		{"flag prefix", []string{"logs", "--fo"}, []string{"--follow", "--forward-agent"}},
		{"separate value", []string{"up", "--selinux", ""}, []string{"auto", "z", "Z", "none"}},
		{"inline value", []string{"up", "--runtime=p"}, []string{"--runtime=podman"}},
		{"bool flag inline value", []string{"down", "--purge=f"}, []string{"--purge=false"}},
		{"bool flag takes no value", []string{"up", "--recreate", "--reb"}, []string{"--rebuild"}},
		{"value word is skipped", []string{"up", "--selinux", "z", "--rec"}, []string{"--recreate"}},
		{"path is left to the shell", []string{"up", "--path", "x"}, []string{directiveFiles}},
		{"inline path too", []string{"up", "--config=x"}, []string{directiveFiles}},
		{"exec command is left to the shell", []string{"exec", "-T", "--", ""}, []string{directiveFiles}},
		{"no flags after a positional", []string{"exec", "--", "-"}, []string{directiveFiles}},
		{"unknown command", []string{"nope", ""}, nil},
		{"unknown flag", []string{"up", "--nope=x"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, values(complete(ctx, tt.words)))
		})
	}
}

func TestCompleteFlagSpelling(t *testing.T) {
	got := values(complete(context.Background(), []string{"exec", "-"}))
	assert.Contains(t, got, "-T", "one-letter flags take one dash")
	assert.Contains(t, got, "-n")
	assert.Contains(t, got, "--service")
	assert.Contains(t, got, "--name")
}
