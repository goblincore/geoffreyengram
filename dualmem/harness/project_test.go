package harness

import (
	"context"
	"errors"
	"testing"
)

func TestResolveProjectExplicitNamespaceTakesPrecedence(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		t.Fatal("GitCommonDir called for explicit namespace")
		return "", nil
	}
	got, err := ResolveProject(context.Background(), Event{
		CWD:     "/tmp/worktree",
		Project: ProjectRef{Root: "/projects/repo", Namespace: "team:shared"},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "team:shared" {
		t.Fatalf("namespace = %q, want team:shared", got.Namespace)
	}
}

func TestResolveProjectExplicitRootPrecedesCWD(t *testing.T) {
	opts := DefaultResolveOptions()
	var requested string
	opts.GitCommonDir = func(_ context.Context, directory string) (string, error) {
		requested = directory
		return "/projects/geoffreyengram/.git", nil
	}
	got, err := ResolveProject(context.Background(), Event{
		CWD:     "/tmp/unrelated",
		Project: ProjectRef{Root: "/projects/geoffreyengram/packages/cli"},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if requested != "/projects/geoffreyengram/packages/cli" {
		t.Fatalf("GitCommonDir directory = %q", requested)
	}
	if got.Root != "/projects/geoffreyengram" || got.Name != "geoffreyengram" || got.Namespace != "claude:geoffreyengram" {
		t.Fatalf("unexpected identity: %#v", got)
	}
}

func TestResolveProjectMainRepositoryUsesCommonRoot(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.GitCommonDir = func(_ context.Context, directory string) (string, error) {
		if directory != "/projects/geoffreyengram" {
			t.Fatalf("GitCommonDir directory = %q", directory)
		}
		return "/projects/geoffreyengram/.git", nil
	}
	got, err := ResolveProject(context.Background(), Event{CWD: "/projects/geoffreyengram"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "/projects/geoffreyengram" || got.Namespace != "claude:geoffreyengram" || got.Projectless {
		t.Fatalf("unexpected identity: %#v", got)
	}
}

func TestResolveProjectWorktreeUsesCommonRoot(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		return "/projects/geoffreyengram/.git", nil
	}
	got, err := ResolveProject(context.Background(), Event{CWD: "/tmp/feature-worktree"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "claude:geoffreyengram" {
		t.Fatalf("namespace = %q", got.Namespace)
	}
}

func TestResolveProjectUsesConfiguredProjectAfterGitLookupFails(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.ConfiguredProject = "configured-project"
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		return "", errors.New("not a repository")
	}
	got, err := ResolveProject(context.Background(), Event{CWD: "/tmp/projectless"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "configured-project" || got.Namespace != "claude:configured-project" || got.Projectless {
		t.Fatalf("unexpected identity: %#v", got)
	}
}

func TestResolveProjectRejectsProjectlessDirectoryByDefault(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		return "", errors.New("not a repository")
	}
	if _, err := ResolveProject(context.Background(), Event{CWD: "/tmp/projectless"}, opts); err == nil {
		t.Fatal("ResolveProject succeeded for projectless directory")
	}
}

func TestResolveProjectAllowsConfiguredDirectoryFallback(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.AllowDirectoryFallback = true
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		return "", errors.New("not a repository")
	}
	got, err := ResolveProject(context.Background(), Event{CWD: "/tmp/projectless"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "/tmp/projectless" || got.Name != "projectless" || got.Namespace != "claude:projectless" || !got.Projectless {
		t.Fatalf("unexpected identity: %#v", got)
	}
}
