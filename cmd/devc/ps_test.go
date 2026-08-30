package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterRows(t *testing.T) {
	rows := []serviceRow{
		{Service: "cache", State: "not created"},
		{Service: "db", State: "running"},
		{Service: "workspace", State: "running", Workspace: true},
	}

	t.Run("no names keeps everything", func(t *testing.T) {
		out, err := filterRows(rows, nil)
		require.NoError(t, err)
		assert.Equal(t, rows, out)
	})

	t.Run("named services come back in the order asked for", func(t *testing.T) {
		out, err := filterRows(rows, []string{"workspace", "cache"})
		require.NoError(t, err)
		assert.Equal(t, []string{"workspace", "cache"}, serviceNames(out))
	})

	t.Run("a service with no container still shows", func(t *testing.T) {
		out, err := filterRows(rows, []string{"cache"})
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "not created", out[0].State)
	})

	t.Run("an unknown service is an error, not an empty table", func(t *testing.T) {
		_, err := filterRows(rows, []string{"dbb"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no service "dbb"`)
		assert.Contains(t, err.Error(), "cache, db, workspace", "the error should list what is available")
	})
}
