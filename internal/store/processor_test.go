package store

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessorLockSurvivesStoreAndPathAliases(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, rawPath := filepath.Join(dir, "db"), filepath.Join(dir, "raw")
	s, err := Open(ctx, dbPath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alias := filepath.Join(dir, "alias")
	if err = os.Symlink(dbPath, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	other, err := Open(ctx, alias, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	release, acquired, err := s.LockProcessor(ctx, "../stage/not-a-path", 1)
	if err != nil || !acquired {
		t.Fatalf("first owner: %v %v", acquired, err)
	}
	defer release()
	_, acquired, err = other.LockProcessor(ctx, "../stage/not-a-path", 1)
	if err != nil || acquired {
		t.Fatalf("second owner entered same stage: %v %v", acquired, err)
	}
	next, acquired, err := other.LockProcessor(ctx, "../stage/not-a-path", 2)
	if err != nil || !acquired {
		t.Fatalf("independent attempt blocked: %v %v", acquired, err)
	}
	next()
	release()
	release()
	next, acquired, err = other.LockProcessor(ctx, "../stage/not-a-path", 1)
	if err != nil || !acquired {
		t.Fatalf("released stage cannot recover: %v %v", acquired, err)
	}
	next()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err = s.LockProcessor(ctx, "stage", 1); err == nil || acquired {
		t.Fatal("closed Store admitted processor")
	}
}

// The child process must actually exit before another Store can take over its exact stage lock.
func TestProcessorLockAcrossProcessExit(t *testing.T) {
	if os.Getenv("NEUX_PROCESSOR_LOCK_CHILD") == "1" {
		s, err := Open(context.Background(), os.Getenv("NEUX_PROCESSOR_DB"), os.Getenv("NEUX_PROCESSOR_RAW"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		release, acquired, err := s.LockProcessor(context.Background(), "stage", 1)
		if err != nil || !acquired {
			t.Fatalf("child lock: %v %v", acquired, err)
		}
		defer release()
		fmt.Println("processor locked")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return
	}
	dir := t.TempDir()
	dbPath, rawPath := filepath.Join(dir, "db"), filepath.Join(dir, "raw")
	s, err := Open(context.Background(), dbPath, rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessorLockAcrossProcessExit$")
	cmd.Env = append(os.Environ(), "NEUX_PROCESSOR_LOCK_CHILD=1", "NEUX_PROCESSOR_DB="+dbPath, "NEUX_PROCESSOR_RAW="+rawPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "processor locked\n" {
		t.Fatalf("child did not acquire stage: %q %v", line, err)
	}
	_, acquired, err := s.LockProcessor(context.Background(), "stage", 1)
	if err != nil || acquired {
		t.Fatalf("live child owner was stolen: %v %v", acquired, err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	release, acquired, err := s.LockProcessor(context.Background(), "stage", 1)
	if err != nil || !acquired {
		t.Fatalf("dead child stage was not recoverable: %v %v", acquired, err)
	}
	release()
}
