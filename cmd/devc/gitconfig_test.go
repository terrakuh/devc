package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderGitConfigEmpty(t *testing.T) {
	assert.Equal(t, "", renderGitConfig(nil))
}

func TestRenderGitConfigGroupsBySection(t *testing.T) {
	out := renderGitConfig([][2]string{
		{"user.name", "Ada Lovelace"},
		{"user.email", "ada@example.com"},
		{"gpg.format", "ssh"},
		{"commit.gpgsign", "true"},
	})
	assert.Contains(t, out, "# Managed by devc")
	assert.Contains(t, out, "[user]\n\tname = Ada Lovelace\n\temail = ada@example.com\n")
	assert.Contains(t, out, "[gpg]\n\tformat = ssh\n")
	assert.Contains(t, out, "[commit]\n\tgpgsign = true\n")
	// user comes before gpg (first-seen section order).
	assert.Less(t, strings.Index(out, "[user]"), strings.Index(out, "[gpg]"))
}
