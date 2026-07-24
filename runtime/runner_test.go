package runtime

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRecordsAndReplays(t *testing.T) {
	f := NewFake()
	f.OutputFunc = func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			return []byte("container-id\n"), nil
		}
		return nil, nil
	}
	ctx := context.Background()

	out, err := f.Output(ctx, "run", "--detach", "img")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "container-id\n" {
		t.Fatalf("output = %q", out)
	}
	if err := f.Run(ctx, []string{"stop", "x"}, IO{}); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("calls = %v", f.CallStrings())
	}
	if f.FindCall("stop") == nil {
		t.Fatalf("stop not recorded: %v", f.CallStrings())
	}
}

func TestFakeInspectNoObject(t *testing.T) {
	f := NewFake() // no OutputFunc
	var v map[string]any
	err := f.Inspect(context.Background(), "nope", &v)
	if !errors.Is(err, ErrNoSuchObject) {
		t.Fatalf("expected ErrNoSuchObject, got %v", err)
	}
}

func TestDetectExplicitMissing(t *testing.T) {
	if _, err := Detect("definitely-not-a-runtime-xyz"); err == nil {
		t.Fatal("expected error for missing explicit runtime")
	}
}

func TestIsNoSuchObject(t *testing.T) {
	cases := map[string]bool{
		"Error: no such container foo": true,
		"unable to find image bar":     true,
		"something else entirely":      false,
		"connection refused":           false,
	}
	for msg, want := range cases {
		if got := isNoSuchObject(errors.New(msg)); got != want {
			t.Errorf("isNoSuchObject(%q) = %v want %v", msg, got, want)
		}
	}
}
