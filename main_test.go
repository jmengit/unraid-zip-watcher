package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeZipPath(t *testing.T) {
	valid := map[string]string{
		"folder/file.txt":  "folder/file.txt",
		"folder\\file.txt": "folder/file.txt",
		"./file.txt":       "file.txt",
	}
	for input, want := range valid {
		got, err := safeZipPath(input)
		if err != nil || got != want {
			t.Fatalf("safeZipPath(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "/etc/passwd", "//server/share/file", "C:/Windows/system.ini", "../outside", "a/../../outside", `..\\outside`} {
		if _, err := safeZipPath(input); err == nil {
			t.Fatalf("safeZipPath(%q) accepted an unsafe path", input)
		}
	}
}

func TestExtractArchiveRejectsLimits(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "large.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	entry, err := zw.Create("large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("1234567890")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(zipPath, tmp, 5, 5, 100); err == nil {
		t.Fatal("extractArchive accepted an archive over the byte limit")
	}

	if err := extractArchive(zipPath, tmp, 0, 0, 0); err == nil {
		t.Fatal("extractArchive accepted an archive when max entries is zero")
	}
}

func TestExtractArchive(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "sample.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	entry, err := zw.Create("nested/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractArchive(zipPath, tmp, 0, 0, 10000); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "sample", "nested", "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("extracted content = %q; want hello", got)
	}
}
