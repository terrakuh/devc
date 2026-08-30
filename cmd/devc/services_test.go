package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/terrakuh/devc/config"
)

func TestComposeServices(t *testing.T) {
	compose := &env{spec: &config.Spec{
		Name:    "shop",
		Kind:    config.KindCompose,
		Compose: &config.ComposeSpec{Service: "workspace"},
	}}
	single := &env{spec: &config.Spec{Name: "shop", Kind: config.KindImage}}

	t.Run("compose passes services through", func(t *testing.T) {
		svcs, err := composeServices(compose, []string{"db", "cache"})
		require.NoError(t, err)
		assert.Equal(t, []string{"db", "cache"}, svcs)
	})

	t.Run("no services is always fine", func(t *testing.T) {
		svcs, err := composeServices(single, nil)
		require.NoError(t, err)
		assert.Empty(t, svcs)
	})

	t.Run("single container rejects services", func(t *testing.T) {
		_, err := composeServices(single, []string{"db"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "single container")
		assert.Contains(t, err.Error(), "db", "the error should name what was asked for")
	})
}
