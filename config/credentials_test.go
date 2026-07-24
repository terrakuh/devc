package config

import "testing"

func TestResolveCredentialsDefaultsOff(t *testing.T) {
	dir := writeConfig(t, `{"image": "fedora:44"}`)
	spec, _, err := LoadSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Credentials.ForwardAgent || spec.Credentials.SyncGitConfig {
		t.Fatalf("credentials should default off, got %+v", spec.Credentials)
	}
}

func TestResolveCredentialsFromCustomizations(t *testing.T) {
	dir := writeConfig(t, `{
		"image": "fedora:44",
		"customizations": {
			"devc": {"credentials": {"forwardAgent": true, "syncGitConfig": true}}
		}
	}`)
	spec, warnings, err := LoadSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Credentials.ForwardAgent || !spec.Credentials.SyncGitConfig {
		t.Fatalf("credentials = %+v", spec.Credentials)
	}
	if len(warnings) != 0 {
		t.Fatalf("customizations must not warn: %v", warnings)
	}
}
