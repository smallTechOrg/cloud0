package testenv

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRepoRoot_FindsGoMod(t *testing.T) {
	root := RepoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s: %v", root, err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "testenv")); err != nil {
		t.Fatalf("root %s does not contain internal/testenv", root)
	}
}

func TestTempDB_IsInsideTempDirAndDoesNotExistYet(t *testing.T) {
	p := TempDB(t)
	if filepath.Base(p) != "cloud0-test.db" {
		t.Fatalf("unexpected name %s", p)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("temp db should not exist yet: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(p)); err != nil {
		t.Fatalf("temp dir should exist: %v", err)
	}
}

func TestFreePort_ReturnsABindablePort(t *testing.T) {
	port := FreePort(t)
	if port < 1024 || port > 65535 {
		t.Fatalf("port %d out of range", port)
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("port %d not bindable: %v", port, err)
	}
	_ = l.Close()
}

func TestLoadDotEnv_DoesNotOverrideExistingValues(t *testing.T) {
	t.Setenv("CLOUD0_PORT", "12345")
	LoadDotEnv(t)
	if got := os.Getenv("CLOUD0_PORT"); got != "12345" {
		t.Fatalf("LoadDotEnv overrode CLOUD0_PORT: %q", got)
	}
}
