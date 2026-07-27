package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type fingerprint struct {
	Size    int64 `json:"size"`
	ModTime int64 `json:"mod_time_unix_nano"`
}

type state struct {
	Processed map[string]fingerprint `json:"processed"`
}

type config struct {
	WatchDir             string
	OutputDir            string
	StateDir             string
	PollInterval         time.Duration
	StableScans          int
	DeleteZip            bool
	MaxUncompressedBytes int64
	MaxEntryBytes        int64
	MaxEntries           int
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	for _, dir := range []string{cfg.WatchDir, cfg.OutputDir, cfg.StateDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("create directory %q: %v", dir, err)
		}
	}

	statePath := filepath.Join(cfg.StateDir, "processed.json")
	st, err := loadState(statePath)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("watching %s; extracting to %s; polling every %s", cfg.WatchDir, cfg.OutputDir, cfg.PollInterval)
	if cfg.DeleteZip {
		log.Printf("successful source ZIPs will be deleted")
	}
	if cfg.MaxUncompressedBytes > 0 {
		log.Printf("maximum uncompressed archive size: %d bytes", cfg.MaxUncompressedBytes)
	} else {
		log.Printf("maximum uncompressed archive size: unlimited")
	}
	log.Printf("maximum entries: %d; maximum bytes per entry: %d", cfg.MaxEntries, cfg.MaxEntryBytes)

	candidates := make(map[string]fingerprint)
	stableCounts := make(map[string]int)
	for {
		if err := scan(ctx, cfg, st, candidates, stableCounts, statePath); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("scan error: %v", err)
		}
		select {
		case <-ctx.Done():
			log.Printf("stopping")
			return
		case <-time.After(cfg.PollInterval):
		}
	}
}

func loadConfig() (config, error) {
	watchDir := envString("WATCH_DIR", "/watch")
	outputDir := envString("OUTPUT_DIR", "/output")
	stateDir := envString("STATE_DIR", "/state")
	pollInterval, err := time.ParseDuration(envString("POLL_INTERVAL", "5s"))
	if err != nil || pollInterval <= 0 {
		return config{}, fmt.Errorf("POLL_INTERVAL must be a positive duration (example: 5s), got %q", os.Getenv("POLL_INTERVAL"))
	}
	stableScans, err := strconv.Atoi(envString("STABLE_SCANS", "2"))
	if err != nil || stableScans < 1 {
		return config{}, fmt.Errorf("STABLE_SCANS must be a positive integer, got %q", os.Getenv("STABLE_SCANS"))
	}
	maxBytes, err := strconv.ParseInt(envString("MAX_UNCOMPRESSED_BYTES", "10737418240"), 10, 64)
	if err != nil || maxBytes < 0 {
		return config{}, fmt.Errorf("MAX_UNCOMPRESSED_BYTES must be zero or a positive integer, got %q", os.Getenv("MAX_UNCOMPRESSED_BYTES"))
	}
	maxEntryBytes, err := strconv.ParseInt(envString("MAX_ENTRY_BYTES", "2147483648"), 10, 64)
	if err != nil || maxEntryBytes < 0 {
		return config{}, fmt.Errorf("MAX_ENTRY_BYTES must be zero or a positive integer, got %q", os.Getenv("MAX_ENTRY_BYTES"))
	}
	maxEntries, err := strconv.Atoi(envString("MAX_ENTRIES", "10000"))
	if err != nil || maxEntries < 1 {
		return config{}, fmt.Errorf("MAX_ENTRIES must be a positive integer, got %q", os.Getenv("MAX_ENTRIES"))
	}
	return config{
		WatchDir:             watchDir,
		OutputDir:            outputDir,
		StateDir:             stateDir,
		PollInterval:         pollInterval,
		StableScans:          stableScans,
		DeleteZip:            envBool("DELETE_ZIP", false),
		MaxUncompressedBytes: maxBytes,
		MaxEntryBytes:        maxEntryBytes,
		MaxEntries:           maxEntries,
	}, nil
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Printf("invalid %s=%q; using %t", key, value, fallback)
		return fallback
	}
	return parsed
}

func loadState(filename string) (state, error) {
	st := state{Processed: make(map[string]fingerprint)}
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read state file: %w", err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse state file %q: %w", filename, err)
	}
	if st.Processed == nil {
		st.Processed = make(map[string]fingerprint)
	}
	return st, nil
}

func saveState(filename string, st state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".processed-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary state file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := os.Rename(tmpName, filename); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func scan(ctx context.Context, cfg config, st state, candidates map[string]fingerprint, stableCounts map[string]int, statePath string) error {
	entries, err := os.ReadDir(cfg.WatchDir)
	if err != nil {
		return fmt.Errorf("read watch directory: %w", err)
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			log.Printf("stat %q: %v", entry.Name(), err)
			continue
		}
		zipPath := filepath.Join(cfg.WatchDir, entry.Name())
		seen[zipPath] = true
		fp := fingerprint{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		if old, ok := st.Processed[zipPath]; ok && old == fp {
			delete(candidates, zipPath)
			delete(stableCounts, zipPath)
			continue
		}
		if old, ok := candidates[zipPath]; !ok || old != fp {
			candidates[zipPath] = fp
			stableCounts[zipPath] = 1
			log.Printf("detected %q; waiting for a stable file", entry.Name())
			continue
		} else {
			stableCounts[zipPath]++
		}
		if stableCounts[zipPath] < cfg.StableScans {
			continue
		}

		log.Printf("extracting %q", entry.Name())
		if err := extractArchive(zipPath, cfg.OutputDir, cfg.MaxUncompressedBytes, cfg.MaxEntryBytes, cfg.MaxEntries); err != nil {
			log.Printf("failed to extract %q: %v", entry.Name(), err)
			continue
		}
		st.Processed[zipPath] = fp
		if err := saveState(statePath, st); err != nil {
			return fmt.Errorf("save state after %q: %w", entry.Name(), err)
		}
		delete(candidates, zipPath)
		delete(stableCounts, zipPath)
		log.Printf("extracted %q", entry.Name())
		if cfg.DeleteZip {
			if err := os.Remove(zipPath); err != nil {
				log.Printf("extracted %q but could not delete source: %v", entry.Name(), err)
			} else {
				log.Printf("deleted %q", entry.Name())
			}
		}
	}
	for zipPath := range candidates {
		if !seen[zipPath] {
			delete(candidates, zipPath)
			delete(stableCounts, zipPath)
		}
	}
	return nil
}

func extractArchive(zipPath, outputDir string, maxBytes, maxEntryBytes int64, maxEntries int) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer r.Close()
	if len(r.File) > maxEntries {
		return fmt.Errorf("archive contains %d entries; maximum is %d", len(r.File), maxEntries)
	}

	archiveName := strings.TrimSuffix(filepath.Base(zipPath), filepath.Ext(zipPath))
	if archiveName == "" || archiveName == "." || archiveName == ".." {
		archiveName = "archive"
	}
	if strings.ContainsAny(archiveName, `/\\`) {
		return fmt.Errorf("invalid archive filename")
	}
	destination := filepath.Join(outputDir, archiveName)
	tempDir, err := os.MkdirTemp(outputDir, "."+archiveName+".extracting-")
	if err != nil {
		return fmt.Errorf("create extraction directory: %w", err)
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()

	var total int64
	for _, file := range r.File {
		if file.UncompressedSize64 > uint64(1<<63-1) {
			return fmt.Errorf("entry %q is too large", file.Name)
		}
		entrySize := int64(file.UncompressedSize64)
		if maxEntryBytes > 0 && entrySize > maxEntryBytes {
			return fmt.Errorf("entry %q exceeds maximum size of %d bytes", file.Name, maxEntryBytes)
		}
		if maxBytes > 0 && entrySize > maxBytes-total {
			return fmt.Errorf("archive exceeds maximum uncompressed size of %d bytes", maxBytes)
		}
		cleanName, err := safeZipPath(file.Name)
		if err != nil {
			return fmt.Errorf("unsafe entry %q: %w", file.Name, err)
		}
		target := filepath.Join(tempDir, filepath.FromSlash(cleanName))
		rel, err := filepath.Rel(tempDir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("entry %q escapes destination", file.Name)
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed (%q)", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("create directory %q: %w", cleanName, err)
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("unsupported entry type for %q", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("create parent for %q: %w", cleanName, err)
		}
		perm := mode.Perm() & 0777
		if perm == 0 {
			perm = 0644
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return fmt.Errorf("create %q: %w", cleanName, err)
		}
		src, err := file.Open()
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("open %q: %w", cleanName, err)
		}
		remaining := int64(-1)
		if maxBytes > 0 {
			remaining = maxBytes - total
		}
		written, copyErr := copyWithLimit(out, src, remaining)
		srcCloseErr := src.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("write %q: %w", cleanName, copyErr)
		}
		if srcCloseErr != nil {
			return fmt.Errorf("close archive entry %q: %w", cleanName, srcCloseErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %q: %w", cleanName, closeErr)
		}
		total += written
	}

	if _, err := os.Stat(destination); err == nil {
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("replace existing output directory: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	if err := os.Rename(tempDir, destination); err != nil {
		return fmt.Errorf("finalize extraction: %w", err)
	}
	keepTemp = true
	return nil
}

func copyWithLimit(dst io.Writer, src io.Reader, remaining int64) (int64, error) {
	if remaining < 0 {
		return io.Copy(dst, src)
	}
	// Read one byte beyond the limit so an archive that exceeds the limit is
	// rejected even when its first entry ends exactly at the boundary.
	n, err := io.Copy(dst, io.LimitReader(src, remaining+1))
	if err != nil {
		return n, err
	}
	if n > remaining {
		return n, fmt.Errorf("maximum uncompressed size exceeded")
	}
	return n, nil
}

func safeZipPath(name string) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("empty or NUL-containing path")
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "//") || (len(name) >= 2 && name[1] == ':') {
		return "", fmt.Errorf("absolute or empty path")
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path traversal")
	}
	return clean, nil
}
