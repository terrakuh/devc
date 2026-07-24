package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/terrakuh/devc/config"
)

func TestFolderURI(t *testing.T) {
	tests := []struct {
		name string
		spec config.Spec
		want string
	}{
		{
			name: "typical",
			spec: config.Spec{Name: "devc", ContainerWorkspaceFolder: "/workspaces/devc"},
			want: "vscode-remote://ssh-remote+devc.devc/workspaces/devc",
		},
		{
			name: "distinct name and folder",
			spec: config.Spec{Name: "shop", ContainerWorkspaceFolder: "/src/app"},
			want: "vscode-remote://ssh-remote+devc.shop/src/app",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, folderURI(&tt.spec))
		})
	}
}
