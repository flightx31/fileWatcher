package fileWatcher

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestFileWatcher_CallbackRouting(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)

	w, err := Init(done, fs, testLogger, WatcherCallbacks{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	testPath := "/tmp/test.txt"
	w.archiveWatchesMap.Set(testPath, Archive)

	var archiveCalled bool
	w.OnArchiveCallback = func(e FileWatcherEvent) {
		if e.Path == testPath {
			archiveCalled = true
		}
	}

	var standardCalled bool
	w.OnStandardCallback = func(e FileWatcherEvent) {
		standardCalled = true
	}

	w.sendEvent(FileWatcherEvent{Path: testPath, Event: "EDIT_FILE"})

	if !archiveCalled {
		t.Error("Archive callback was not called")
	}
	if standardCalled {
		t.Error("Standard callback was called for Archive file")
	}
}

func TestFileWatcher_RecursiveCallbackRouting(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)

	w, err := Init(done, fs, testLogger, WatcherCallbacks{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	dirPath := "/tmp/my-dir"
	filePath := "/tmp/my-dir/file.txt"
	w.lowLatencyWatchesMap.Set(dirPath, LowLatency)

	var lowLatencyCalled bool
	w.OnLowLatencyCallback = func(e FileWatcherEvent) {
		if e.Path == filePath {
			lowLatencyCalled = true
		}
	}

	w.sendEvent(FileWatcherEvent{Path: filePath, Event: "CREATE_FILE"})

	if !lowLatencyCalled {
		t.Error("LowLatency callback was not called for file in LowLatency directory")
	}
}

func TestFileWatcher_RenameTracking(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)

	w, err := Init(done, fs, testLogger, WatcherCallbacks{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	oldPath := "/tmp/old.txt"
	newPath := "/tmp/new.txt"
	w.streamingWatchesMap.Set(oldPath, Streaming)

	event := FileWatcherEvent{
		Path:         newPath,
		PreviousPath: oldPath,
		Event:        "RENAME_FILE",
	}

	w.sendEvent(event)

	// Check if map was updated
	if val, ok := w.GetFileType(newPath); !ok || val != Streaming {
		t.Errorf("Maps were not updated after rename. Got type %v, ok %v", val, ok)
	}
	if w.Contains(oldPath) {
		t.Error("Old path still exists in maps after rename")
	}
}

func TestFileWatcher_CurrentDirCallbackRouting(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)

	w, err := Init(done, fs, testLogger, WatcherCallbacks{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	w.standardWatchesMap.Set(".", Standard)

	var standardCalled bool
	w.OnStandardCallback = func(e FileWatcherEvent) {
		standardCalled = true
	}

	w.sendEvent(FileWatcherEvent{Path: "some_file.txt", Event: "CREATE_FILE"})

	if !standardCalled {
		t.Error("Standard callback was not called for file in current directory (.)")
	}
}

func TestFileWatcher_ArchivePolling(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)

	w, err := Init(done, fs, testLogger, WatcherCallbacks{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	// Use a very short interval for testing
	w.SetArchivePollingInterval(50 * time.Millisecond)

	testPath := "/archive/file.txt"
	_ = afero.WriteFile(fs, testPath, []byte("initial content"), 0644)

	var archiveCalled bool
	w.OnArchiveCallback = func(e FileWatcherEvent) {
		if e.Path == testPath && e.Event == e.EditFileEvent() {
			archiveCalled = true
		}
	}

	err = w.AddArchiveFile(testPath)
	if err != nil {
		t.Fatalf("AddArchiveFile failed: %v", err)
	}

	// Wait for initial metadata capture
	time.Sleep(20 * time.Millisecond)

	// Change the file
	_ = afero.WriteFile(fs, testPath, []byte("updated content"), 0644)

	// Wait for polling (at least one interval)
	time.Sleep(150 * time.Millisecond)

	if !archiveCalled {
		t.Error("Archive callback was not called after file change via polling")
	}
}

func TestFileWatcher_StandardPolling(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)

	w, err := Init(done, fs, testLogger, WatcherCallbacks{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	// Short intervals for testing
	w.SetStandardPollingInterval(100 * time.Millisecond)
	w.SetStandardFastPollingInterval(20 * time.Millisecond)
	w.SetStandardAggressiveness(2)

	testPath := "/standard/file.txt"
	_ = afero.WriteFile(fs, testPath, []byte("initial content"), 0644)

	var standardChangeCount int
	w.OnStandardCallback = func(e FileWatcherEvent) {
		if e.Path == testPath && e.Event == e.EditFileEvent() {
			standardChangeCount++
		}
	}

	err = w.AddStandardFile(testPath)
	if err != nil {
		t.Fatalf("AddStandardFile failed: %v", err)
	}

	// Wait for initial metadata capture
	time.Sleep(50 * time.Millisecond)

	// 1. Check slow polling
	_ = afero.WriteFile(fs, testPath, []byte("change 1"), 0644)
	time.Sleep(200 * time.Millisecond) // Wait for slow poll

	if standardChangeCount != 1 {
		t.Errorf("Expected 1 change from slow poll, got %d", standardChangeCount)
	}

	// 2. Make it "hot"
	_ = afero.WriteFile(fs, testPath, []byte("change 2"), 0644)
	time.Sleep(200 * time.Millisecond)

	if standardChangeCount != 2 {
		t.Errorf("Expected 2 changes total, got %d", standardChangeCount)
	}

	// Now it should be hot (ChangeCount >= 2)
	// Fast poll is 20ms.

	startCount := standardChangeCount
	_ = afero.WriteFile(fs, testPath, []byte("change 3"), 0644)
	time.Sleep(100 * time.Millisecond) // Should be picked up by fast poll

	if standardChangeCount <= startCount {
		t.Errorf("Expected change 3 to be picked up by fast poll, count stayed at %d", standardChangeCount)
	}
}

func TestFileWatcher_StreamingData(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)
	w, err := Init(done, fs, testLogger, WatcherCallbacks{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	testPath := "/stream.log"
	initialData := []byte("initial content")
	_ = afero.WriteFile(fs, testPath, initialData, 0644)

	dataReceived := make(chan []byte, 10)
	var callbackCalled bool
	w.OnStreamingDataCallback = func(data <-chan []byte, path string) {
		if path == testPath {
			callbackCalled = true
			for b := range data {
				dataReceived <- b
			}
		}
	}

	err = w.AddStreamingFile(testPath)
	if err != nil {
		t.Fatalf("AddStreamingFile failed: %v", err)
	}

	// Wait for callback to be triggered
	time.Sleep(20 * time.Millisecond)
	if !callbackCalled {
		t.Error("Streaming data callback was not called")
	}

	// Check that initial content was NOT streamed (should start from end)
	select {
	case <-dataReceived:
		t.Error("Initial content should not be streamed")
	default:
	}

	// Append data
	appendData := []byte(" new data")
	f, _ := fs.OpenFile(testPath, os.O_APPEND|os.O_WRONLY, 0644)
	_, _ = f.Write(appendData)
	_ = f.Close()

	// Manually trigger read (since fsnotify doesn't work with MemMapFs)
	w.readStreamingData(testPath)

	select {
	case data := <-dataReceived:
		if string(data) != string(appendData) {
			t.Errorf("Expected %s, got %s", string(appendData), string(data))
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timed out waiting for streamed data")
	}

	// Test cleanup on remove
	w.Remove(testPath)
	time.Sleep(20 * time.Millisecond)
	// Channel should be closed, which would break the loop in the callback
	// and we could verify it, but here it's enough to check if it's removed from maps.
	if w.Contains(testPath) {
		t.Error("File still exists in maps after removal")
	}
}
