package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/flightx31/fileWatcher"
	"github.com/spf13/afero"
)

func main() {
	fmt.Println("Starting FileWatcher Example...")

	// Initialize with OsFs for real file system interaction.
	fs := afero.NewOsFs()
	done := make(chan bool)

	w, err := fileWatcher.Init(done, fs, slog.Default(), fileWatcher.WatcherCallbacks{
		OnStandard: func(e fileWatcher.FileWatcherEvent) {
			fmt.Printf(">>> [Standard Event] %s: %s\n", e.Event, e.Path)
		},
		OnArchive: func(e fileWatcher.FileWatcherEvent) {
			fmt.Printf(">>> [Archive Event] %s: %s\n", e.Event, e.Path)
		},
		OnLowLatency: func(e fileWatcher.FileWatcherEvent) {
			fmt.Printf(">>> [LowLatency Event] %s: %s\n", e.Event, e.Path)
		},
		OnStreaming: func(e fileWatcher.FileWatcherEvent) {
			fmt.Printf(">>> [Streaming Event] %s: %s\n", e.Event, e.Path)
		},
		OnStreamingData: func(data <-chan []byte, filePath string) {
			fmt.Printf(">>> [Streaming Data] Listening for bytes on: %s\n", filePath)
			for chunk := range data {
				fmt.Printf(">>> [Streaming Data] %s received %d bytes: %q\n", filePath, len(chunk), string(chunk))
			}
			fmt.Printf(">>> [Streaming Data] Channel closed for: %s\n", filePath)
		},
	})
	if err != nil {
		fmt.Printf("Failed to initialize watcher: %v\n", err)
		os.Exit(1)
	}

	// 3. Setup temporary environment for the demo.
	tempDir, err := os.MkdirTemp("", "fw_demo_*")
	if err != nil {
		fmt.Printf("Failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	stdPath := filepath.Join(tempDir, "standard.txt")
	arcPath := filepath.Join(tempDir, "archive.txt")
	llDir := filepath.Join(tempDir, "ll_dir")
	strPath := filepath.Join(tempDir, "streaming.log")

	// Create initial state
	_ = os.WriteFile(stdPath, []byte("standard file init\n"), 0644)
	_ = os.WriteFile(arcPath, []byte("archive file init\n"), 0644)
	_ = os.Mkdir(llDir, 0755)
	_ = os.WriteFile(strPath, []byte("stream file init\n"), 0644)

	// 4. Add paths to the watcher with their specific types.
	// NOTE: Callbacks should be registered BEFORE adding files if you want to catch initial setup events.
	fmt.Println("Registering paths with different types...")
	_ = w.AddStandardFile(stdPath)
	_ = w.AddArchiveFile(arcPath)
	_ = w.AddLowLatencyFile(llDir)
	_ = w.AddStreamingFile(strPath)

	// Speed up polling for the demo.
	w.SetStandardPollingInterval(2 * time.Second)
	w.SetArchivePollingInterval(4 * time.Second)

	// 5. Demonstrate each type in action.
	fmt.Println("--- Demo started (watching for 10 seconds) ---")

	// LowLatency: Should trigger almost immediately via fsnotify.
	go func() {
		time.Sleep(1 * time.Second)
		fmt.Println("[Action] Creating file in LowLatency directory...")
		_ = os.WriteFile(filepath.Join(llDir, "new_fast_file.txt"), []byte("fast data"), 0644)
	}()

	// Streaming: Follows the end of file and sends bytes to the data channel.
	go func() {
		time.Sleep(2 * time.Second)
		fmt.Println("[Action] Appending to Streaming file...")
		f, _ := os.OpenFile(strPath, os.O_APPEND|os.O_WRONLY, 0644)
		_, _ = f.WriteString("new streaming data line\n")
		_ = f.Close()
	}()

	// Standard: Detected by polling (check every 2s).
	go func() {
		time.Sleep(3 * time.Second)
		fmt.Println("[Action] Modifying Standard file...")
		_ = os.WriteFile(stdPath, []byte("standard file updated\n"), 0644)
	}()

	// Archive: Detected by slow polling (check every 4s).
	go func() {
		time.Sleep(4 * time.Second)
		fmt.Println("[Action] Modifying Archive file...")
		_ = os.WriteFile(arcPath, []byte("archive file updated\n"), 0644)
	}()

	// Wait for events to be processed.
	time.Sleep(10 * time.Second)

	fmt.Println("--- Demo finished ---")
	done <- true // Signal watcher to stop
	time.Sleep(500 * time.Millisecond)
}
