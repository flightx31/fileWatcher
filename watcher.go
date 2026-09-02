// Package fileWatcher is used to thinly wrap fs.notify watcher in order to add the ability to query if something has
// been watched yet or not.
package fileWatcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/spf13/afero"
)

type Logger interface {
	Panic(args ...any)
	Error(args ...any)
	Warn(args ...any)
	Info(args ...any)
	Debug(args ...any)
	Trace(args ...any)
	Print(args ...any)
}

var log Logger

func SetLogger(l Logger) {
	log = l
}

var fs afero.Fs

func SetFs(newFs afero.Fs) {
	fs = newFs
}

// FileType represents the classification of a watched file.
type FileType int

const (
	// Standard is a file with standard sync priority.
	Standard FileType = iota
	// Archive is a file that is not expected to change and has lowest sync priority.
	Archive
	// LowLatency is a file that needs high sync priority and uses fsnotify.
	LowLatency
	// Streaming is a file that is only watched at the end while data is streaming.
	Streaming
)

func (t FileType) String() string {
	switch t {
	case Standard:
		return "Standard"
	case Archive:
		return "Archive"
	case LowLatency:
		return "LowLatency"
	case Streaming:
		return "Streaming"
	default:
		return "Unknown"
	}
}

// FileChangeCallback is a function that can be registered to receive file change events.
type FileChangeCallback func(event FileWatcherEvent)

type FileWatcher struct {
	Watcher              *fsnotify.Watcher
	StandardWatchesMap   cmap.ConcurrentMap[string, FileType]
	ArchiveWatchesMap    cmap.ConcurrentMap[string, FileType]
	LowLatencyWatchesMap cmap.ConcurrentMap[string, FileType]
	StreamingWatchesMap  cmap.ConcurrentMap[string, FileType]
	archiveInterval        time.Duration
	archiveMetadataMap     cmap.ConcurrentMap[string, *FileMetadata]
	standardInterval       time.Duration
	standardFastInterval   time.Duration
	standardAggressiveness float64
	standardMetadataMap    cmap.ConcurrentMap[string, *StandardMetadata]
	standardLockedFiles    cmap.ConcurrentMap[string, bool]
	Events                 chan FileWatcherEvent
	Errors               chan error
	onStandardCallback   FileChangeCallback
	onArchiveCallback    FileChangeCallback
	onLowLatencyCallback FileChangeCallback
	onStreamingCallback  FileChangeCallback
	mu                   sync.RWMutex
}

type FileMetadata struct {
	Path    string
	Size    int64
	ModTime time.Time
	Hash    string
	IsDir   bool
}

type StandardMetadata struct {
	FileMetadata
	ChangeCount    int
	LastChangeTime time.Time
}

type FileWatcherEvent struct {
	Path         string
	PreviousPath string
	Event        string
}

func (e FileWatcherEvent) RenameFolderEvent() string {
	return "RENAME_FOLDER"
}

func (e FileWatcherEvent) IsRenameFolderEvent() bool {
	return e.Event == e.RenameFolderEvent()
}

func (e FileWatcherEvent) DeleteFolderEvent() string {
	return "DELETE_FOLDER"
}

func (e FileWatcherEvent) IsDeleteFolderEvent() bool {
	return e.Event == e.DeleteFolderEvent()
}

func (e FileWatcherEvent) CreateFolderEvent() string {
	return "CREATE_FOLDER"
}

func (e FileWatcherEvent) IsCreateFolderEvent() bool {
	return e.Event == e.CreateFolderEvent()
}

func (e FileWatcherEvent) CreateFileEvent() string {
	return "CREATE_FILE"
}

func (e FileWatcherEvent) IsCreateFileEvent() bool {
	return e.Event == e.CreateFileEvent()
}

func (e FileWatcherEvent) DeleteFileEvent() string {
	return "DELETE_FILE"
}

func (e FileWatcherEvent) IsDeleteFileEvent() bool {
	return e.Event == e.DeleteFileEvent()
}

func (e FileWatcherEvent) RenameFileEvent() string {
	return "RENAME_FILE"
}

func (e FileWatcherEvent) IsRenameFileEvent() bool {
	return e.Event == e.RenameFileEvent()
}

func (e FileWatcherEvent) EditFileEvent() string {
	return "EDIT_FILE"
}

func (e FileWatcherEvent) IsEditFileEvent() bool {
	return e.Event == e.EditFileEvent()
}

func (e FileWatcherEvent) ChModEvent() string {
	return "CHMOD"
}

func (e FileWatcherEvent) IsChModEvent() bool {
	return e.Event == e.ChModEvent()
}

func Init(done chan bool, newFs afero.Fs, l Logger) (*FileWatcher, error) {
	SetLogger(l)
	SetFs(newFs)
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	res := FileWatcher{}
	res.Watcher = fsWatcher
	res.StandardWatchesMap = cmap.New[FileType]()
	res.ArchiveWatchesMap = cmap.New[FileType]()
	res.LowLatencyWatchesMap = cmap.New[FileType]()
	res.StreamingWatchesMap = cmap.New[FileType]()
	res.archiveInterval = 30 * time.Second
	res.archiveMetadataMap = cmap.New[*FileMetadata]()
	res.standardInterval = 10 * time.Second
	res.standardFastInterval = 1 * time.Second
	res.standardAggressiveness = 1.0
	res.standardMetadataMap = cmap.New[*StandardMetadata]()
	res.standardLockedFiles = cmap.New[bool]()
	res.Errors = make(chan error)
	res.Events = make(chan FileWatcherEvent)

	go res.watchFileChangeEvents(done)
	go res.watchArchiveFiles(done)
	go res.watchStandardFiles(done)
	go res.watchStandardFastFiles(done)

	return &res, nil
}

func resetStack(s []fsnotify.Event) {
	s[0] = fsnotify.Event{}
	s[1] = fsnotify.Event{}
}

// watchFileChangeEvents watches for fsNotify events, and converts those events into more useful events,
// sometimes grouping multiple events into a single event.
//
// Delete a folder - cache: [remove|rename, empty] - single event, clear cache
// REMOVE|RENAME - removed folder path

// Delete a file - cache: [rename, empty] - single event, clear cache
// RENAME - removed file path

// Rename a folder - cache: [remove|rename, create] - double event, clear cache
// CREATE - has the path of the renamed folder
// REMOVE|RENAME - has old folder path

// Rename a file - cache: [rename, create] - double event, clear cache
// CREATE - has the path of the renamed file
// RENAME - has the old file path

// Create a file or folder - cache: [create, ???] - double event, keep cache, and check for second event after certain amount of time. Then clear cache.
// CREATE - has path of newly created item

// Edit a file - cache: [create, remove] - double event, clear cache
// REMOVE - has the path of the file being edited
// CREATE - has the path of the file being edited
func (w *FileWatcher) watchFileChangeEvents(done chan bool) {
	eventsList := make([]fsnotify.Event, 2)
	onlyCreateEvent := false
	delayChan := make(chan bool)
	e := FileWatcherEvent{}

	for {
		select {
		case event := <-w.Watcher.Events:

			if strings.Index(event.Name, ".DS_Store") > 0 {
				break
			}

			if event.Has(fsnotify.Chmod) {
				// send chmod events along down the chain right away
				e.Event = e.ChModEvent()
				e.Path = event.Name
				w.sendEvent(e)
				break
			}

			// move first entry to last spot
			eventsList[1] = eventsList[0]
			// copy current event to first spot
			eventsList[0] = event

			if !eventsList[0].Has(fsnotify.Create) {
				onlyCreateEvent = false
			}

			deleteFolder := eventsList[0].Has(fsnotify.Rename) && eventsList[0].Has(fsnotify.Remove)
			deleteFile := eventsList[0].Has(fsnotify.Rename) && !eventsList[0].Has(fsnotify.Remove)
			renameFolder := eventsList[0].Has(fsnotify.Rename) && eventsList[0].Has(fsnotify.Remove) && eventsList[1].Has(fsnotify.Create)
			renameFile := eventsList[0].Has(fsnotify.Rename) && eventsList[1].Has(fsnotify.Create)
			editFile := eventsList[0].Has(fsnotify.Create) && eventsList[1].Has(fsnotify.Remove)
			rapidDelete := eventsList[0].Has(fsnotify.Remove) && eventsList[1].Has(fsnotify.Create)

			if renameFolder {
				e.Event = e.RenameFolderEvent()
				e.Path = eventsList[1].Name
				e.PreviousPath = eventsList[0].Name
				w.sendEvent(e)
				resetStack(eventsList)
			} else if renameFile {
				e.Event = e.RenameFileEvent()
				e.Path = eventsList[1].Name
				e.PreviousPath = eventsList[0].Name
				w.sendEvent(e)
				resetStack(eventsList)
			} else if editFile {
				e.Event = e.EditFileEvent()
				e.Path = eventsList[0].Name
				e.PreviousPath = ""
				w.sendEvent(e)
				resetStack(eventsList)
			} else if rapidDelete {
				if eventsList[0].Name == eventsList[1].Name {
					log.Debug("File " + eventsList[0].Name + "Was rapidly created and then removed")
				} else {
					log.Warn("Unexpected series of events: ", eventsList)
				}

				resetStack(eventsList)
			} else if deleteFolder {
				e.Event = e.DeleteFolderEvent()
				e.Path = eventsList[0].Name
				e.PreviousPath = ""
				w.sendEvent(e)
				resetStack(eventsList)
			} else if deleteFile {
				e.Event = e.DeleteFileEvent()
				e.Path = eventsList[0].Name
				e.PreviousPath = ""
				w.sendEvent(e)
				resetStack(eventsList)
			} else if eventsList[0].Has(fsnotify.Create) {
				onlyCreateEvent = true
				go eventDelay(delayChan)
			} else if eventsList[0].Has(fsnotify.Remove) && !eventsList[0].Has(fsnotify.Rename) {
				// do nothing
			} else {
				log.Warn("Unknown event " + event.String())
			}
		case <-delayChan:
			// special create event handling
			if onlyCreateEvent {
				fileInfo, err := os.Stat(eventsList[0].Name)
				if os.IsNotExist(err) {
					log.Error("File " + eventsList[0].Name + " is missing")
				}

				if fileInfo.IsDir() {
					e.Event = e.CreateFolderEvent()
				} else {
					e.Event = e.CreateFileEvent()
				}

				e.Path = eventsList[0].Name
				e.PreviousPath = ""
				resetStack(eventsList)
				w.sendEvent(e)
				onlyCreateEvent = false
			}
		case err := <-w.Watcher.Errors:
			w.Errors <- err
		case <-done:
			err := w.Close()
			if err != nil {
				log.Error(err)
			}
			return
		}
	}
}

func eventDelay(channel chan bool) {
	log.Trace("eventDelay() function starting")
	// 125 milliseconds because it's still a pretty long delay from the computers' perspective, but
	// barely noticeable from a human perspective.
	time.Sleep(time.Millisecond * 125)
	channel <- true
}

// RegisterStandardCallback sets the callback for Standard file type events.
func (w *FileWatcher) RegisterStandardCallback(cb FileChangeCallback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onStandardCallback = cb
}

// RegisterArchiveCallback sets the callback for Archive file type events.
func (w *FileWatcher) RegisterArchiveCallback(cb FileChangeCallback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onArchiveCallback = cb
}

// RegisterLowLatencyCallback sets the callback for LowLatency file type events.
func (w *FileWatcher) RegisterLowLatencyCallback(cb FileChangeCallback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onLowLatencyCallback = cb
}

// RegisterStreamingCallback sets the callback for Streaming file type events.
func (w *FileWatcher) RegisterStreamingCallback(cb FileChangeCallback) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onStreamingCallback = cb
}

func (w *FileWatcher) sendEvent(e FileWatcherEvent) {
	w.Events <- e
	w.mu.RLock()
	defer w.mu.RUnlock()

	// Identify the path to look up the file type.
	// For renames, the type was associated with the previous path.
	lookupPath := e.Path
	if e.PreviousPath != "" {
		lookupPath = e.PreviousPath
	}

	if t, ok := w.getFileType(lookupPath); ok {
		// Type-specific callback
		var cb FileChangeCallback
		switch t {
		case Standard:
			cb = w.onStandardCallback
		case Archive:
			cb = w.onArchiveCallback
		case LowLatency:
			cb = w.onLowLatencyCallback
		case Streaming:
			cb = w.onStreamingCallback
		}

		if cb != nil {
			cb(e)
		}

		// Update maps to maintain tracking accuracy.
		if e.IsRenameFileEvent() || e.IsRenameFolderEvent() {
			w.removeFromMap(e.PreviousPath, t)
			w.addToMap(e.Path, t)
		} else if e.IsDeleteFileEvent() || e.IsDeleteFolderEvent() {
			w.removeFromMap(e.Path, t)
		}
	}
}

// AddStandardFile starts watching a file or directory as a Standard file type.
func (w *FileWatcher) AddStandardFile(path string) error {
	return w.addWithType(path, Standard)
}

// AddArchiveFile starts watching a file or directory as an Archive file type.
func (w *FileWatcher) AddArchiveFile(path string) error {
	return w.addWithType(path, Archive)
}

// AddLowLatencyFile starts watching a file or directory as a LowLatency file type.
func (w *FileWatcher) AddLowLatencyFile(path string) error {
	return w.addWithType(path, LowLatency)
}

// AddStreamingFile starts watching a file or directory as a Streaming file type.
func (w *FileWatcher) AddStreamingFile(path string) error {
	return w.addWithType(path, Streaming)
}

// ConvertToFileType updates the tracked file type for a given path.
func (w *FileWatcher) ConvertToFileType(path string, newType FileType) error {
	w.removeFromAllMaps(path)
	w.addToMap(path, newType)
	return nil
}

func (w *FileWatcher) addWithType(path string, fileType FileType) error {
	if !w.Contains(path) {
		fileInfo, err := fs.Stat(path)

		if os.IsNotExist(err) {
			return err
		}

		w.addToMap(path, fileType)

		if fileType == Archive {
			w.updateArchiveMetadata(path)
			return nil
		}
		if fileType == Standard {
			w.updateStandardMetadata(path)
			return nil
		}

		if fileInfo.IsDir() {
			// watch the directory
			return w.Watcher.Add(path)
		} else {
			// check if we are already watching the directory the file is in
			directory := filepath.Dir(path)
			if !w.Contains(directory) {
				// not watching the directory the file is in, watch the file itself.
				return w.Watcher.Add(path)
			}
		}
	}
	return nil
}

func (w *FileWatcher) Add(path string) error {
	return w.AddStandardFile(path)
}

func (w *FileWatcher) Remove(path string) error {
	if w.Contains(path) {
		if _, ok := w.ArchiveWatchesMap.Get(path); !ok {
			if _, ok := w.StandardWatchesMap.Get(path); !ok {
				err := w.Watcher.Remove(path)
				if err != nil {
					log.Error(err)
				}
			}
		}

		w.removeFromAllMaps(path)
	}
	return nil
}

func (w *FileWatcher) GetFileType(path string) (FileType, bool) {
	return w.getFileType(path)
}

func (w *FileWatcher) getFileType(path string) (FileType, bool) {
	current := path
	for {
		if _, ok := w.StandardWatchesMap.Get(current); ok {
			return Standard, true
		}
		if _, ok := w.ArchiveWatchesMap.Get(current); ok {
			return Archive, true
		}
		if _, ok := w.LowLatencyWatchesMap.Get(current); ok {
			return LowLatency, true
		}
		if _, ok := w.StreamingWatchesMap.Get(current); ok {
			return Streaming, true
		}
		parent := filepath.Dir(current)
		if parent == current || parent == "" {
			break
		}
		current = parent
	}
	return 0, false
}

func (w *FileWatcher) Contains(path string) bool {
	if _, ok := w.StandardWatchesMap.Get(path); ok {
		return true
	}
	if _, ok := w.ArchiveWatchesMap.Get(path); ok {
		return true
	}
	if _, ok := w.LowLatencyWatchesMap.Get(path); ok {
		return true
	}
	if _, ok := w.StreamingWatchesMap.Get(path); ok {
		return true
	}
	return false
}

func (w *FileWatcher) addToMap(path string, t FileType) {
	switch t {
	case Standard:
		w.StandardWatchesMap.Set(path, t)
	case Archive:
		w.ArchiveWatchesMap.Set(path, t)
	case LowLatency:
		w.LowLatencyWatchesMap.Set(path, t)
	case Streaming:
		w.StreamingWatchesMap.Set(path, t)
	}
}

func (w *FileWatcher) removeFromMap(path string, t FileType) {
	switch t {
	case Standard:
		w.StandardWatchesMap.Remove(path)
	case Archive:
		w.ArchiveWatchesMap.Remove(path)
	case LowLatency:
		w.LowLatencyWatchesMap.Remove(path)
	case Streaming:
		w.StreamingWatchesMap.Remove(path)
	}
}

func (w *FileWatcher) removeFromAllMaps(path string) {
	w.StandardWatchesMap.Remove(path)
	w.ArchiveWatchesMap.Remove(path)
	w.LowLatencyWatchesMap.Remove(path)
	w.StreamingWatchesMap.Remove(path)
	w.archiveMetadataMap.Remove(path)
	w.standardMetadataMap.Remove(path)
	w.standardLockedFiles.Remove(path)
}

func (w *FileWatcher) Close() error {
	return w.Watcher.Close()
}
