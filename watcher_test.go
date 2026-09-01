package fileWatcher

import (
	"testing"
	"time"
	"github.com/spf13/afero"
)

type mockLogger struct{}
func (l *mockLogger) Panic(args ...any) {}
func (l *mockLogger) Error(args ...any) {}
func (l *mockLogger) Warn(args ...any)  {}
func (l *mockLogger) Info(args ...any)  {}
func (l *mockLogger) Debug(args ...any) {}
func (l *mockLogger) Trace(args ...any) {}
func (l *mockLogger) Print(args ...any) {}

func TestFileWatcher_CallbackRouting(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)
	
	w, err := Init(done, fs, &mockLogger{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	// Drain events to prevent blocking
	go func() {
		for range w.Events {
		}
	}()

	testPath := "/tmp/test.txt"
	w.ArchiveWatchesMap.Set(testPath, Archive)

	var archiveCalled bool
	w.RegisterArchiveCallback(func(e FileWatcherEvent) {
		if e.Path == testPath {
			archiveCalled = true
		}
	})

	var standardCalled bool
	w.RegisterStandardCallback(func(e FileWatcherEvent) {
		standardCalled = true
	})

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
	
	w, err := Init(done, fs, &mockLogger{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	// Drain events
	go func() {
		for range w.Events {
		}
	}()

	dirPath := "/tmp/my-dir"
	filePath := "/tmp/my-dir/file.txt"
	w.LowLatencyWatchesMap.Set(dirPath, LowLatency)

	var lowLatencyCalled bool
	w.RegisterLowLatencyCallback(func(e FileWatcherEvent) {
		if e.Path == filePath {
			lowLatencyCalled = true
		}
	})

	w.sendEvent(FileWatcherEvent{Path: filePath, Event: "CREATE_FILE"})

	if !lowLatencyCalled {
		t.Error("LowLatency callback was not called for file in LowLatency directory")
	}
}

func TestFileWatcher_RenameTracking(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)
	
	w, err := Init(done, fs, &mockLogger{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	// Drain events
	go func() {
		for range w.Events {
		}
	}()

	oldPath := "/tmp/old.txt"
	newPath := "/tmp/new.txt"
	w.StreamingWatchesMap.Set(oldPath, Streaming)

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
	
	w, err := Init(done, fs, &mockLogger{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	// Drain events
	go func() {
		for range w.Events {
		}
	}()

	w.StandardWatchesMap.Set(".", Standard)

	var standardCalled bool
	w.RegisterStandardCallback(func(e FileWatcherEvent) {
		standardCalled = true
	})

	w.sendEvent(FileWatcherEvent{Path: "some_file.txt", Event: "CREATE_FILE"})

	if !standardCalled {
		t.Error("Standard callback was not called for file in current directory (.)")
	}
}

func TestFileWatcher_ArchivePolling(t *testing.T) {
	fs := afero.NewMemMapFs()
	done := make(chan bool)
	
	w, err := Init(done, fs, &mockLogger{})
	if err != nil {
		t.Fatalf("Failed to init: %v", err)
	}
	defer func() { done <- true }()

	// Use a very short interval for testing
	w.SetArchivePollingInterval(50 * time.Millisecond)

	// Drain events
	go func() {
		for range w.Events {
		}
	}()

	testPath := "/archive/file.txt"
	_ = afero.WriteFile(fs, testPath, []byte("initial content"), 0644)

	var archiveCalled bool
	w.RegisterArchiveCallback(func(e FileWatcherEvent) {
		if e.Path == testPath && e.Event == e.EditFileEvent() {
			archiveCalled = true
		}
	})

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
