package collector

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestTailLogBoundedRead exercises the end-read window (tailReadCap) with a
// shrunk cap so the test doesn't need to write hundreds of KB of fixture
// data: a file bigger than the cap must still yield the correct last n
// lines, with the partial line at the start of the read window dropped.
func TestTailLogBoundedRead(t *testing.T) {
	orig := tailReadCap
	tailReadCap = 50 // bytes: small enough to force a mid-file seek
	defer func() { tailReadCap = orig }()

	dir := t.TempDir()
	path := filepath.Join(dir, "collector.log")

	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "line"+strconv.Itoa(i))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if int64(len(content)) <= tailReadCap {
		t.Fatalf("fixture (%d bytes) must exceed tailReadCap (%d) for this test to be meaningful", len(content), tailReadCap)
	}

	got, err := TailLog(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := "line28\nline29\nline30\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestTailLogBoundedReadNMoreThanAvailable asks for more lines than fit in
// the (shrunk) read window and should get back only what's available,
// without error and without any partial leading line.
func TestTailLogBoundedReadNMoreThanAvailable(t *testing.T) {
	orig := tailReadCap
	tailReadCap = 20 // bytes: only a couple of short lines fit
	defer func() { tailReadCap = orig }()

	dir := t.TempDir()
	path := filepath.Join(dir, "collector.log")

	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "l"+strconv.Itoa(i))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if int64(len(content)) <= tailReadCap {
		t.Fatalf("fixture (%d bytes) must exceed tailReadCap (%d) for this test to be meaningful", len(content), tailReadCap)
	}

	got, err := TailLog(path, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("want some available lines, got empty string")
	}
	if strings.HasPrefix(got, "l1\n") {
		t.Fatalf("got %q, want the earliest (partial) line dropped", got)
	}
	if !strings.HasSuffix(got, "l30\n") {
		t.Fatalf("got %q, want it to end with the last written line", got)
	}
}
