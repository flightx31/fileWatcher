// Package fileWatcher is used to thinly wrap fs.notify watcher in order to add the ability to query if something has
// been watched yet or not.
package fileWatcher

import (
	"fmt"
	"github.com/fsnotify/fsnotify"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/spf13/afero"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Logger interface {
	Panic(args ...interface{})
	Error(args ...interface{})
	Warn(args ...interface{})
	Info(args ...interface{})
	Debug(args ...interface{})
	Trace(args ...interface{})
	Print(args ...interface{})
}

var log Logger

func SetLogger(l Logger) {
	log = l
}

var fs afero.Fs

func SetFs(newFs afero.Fs) {
	fs = newFs
}

type FileWatcher struct {
	Watcher    *fsnotify.Watcher
	WatchedMap cmap.ConcurrentMap[string, string]
	Events     chan FileWatcherEvent
	Errors     chan error
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
	// concurrent map: https://github.com/orcaman/concurrent-map
	wMap := cmap.New[string]()
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	res := FileWatcher{}
	res.Watcher = fsWatcher
	res.WatchedMap = wMap
	res.Errors = make(chan error)
	res.Events = make(chan FileWatcherEvent)

	go res.watchFileChangeEvents(done)

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
				w.Events <- e
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
				w.Events <- e
				resetStack(eventsList)
			} else if renameFile {
				e.Event = e.RenameFileEvent()
				e.Path = eventsList[1].Name
				e.PreviousPath = eventsList[0].Name
				w.Events <- e
				resetStack(eventsList)
			} else if editFile {
				e.Event = e.EditFileEvent()
				e.Path = eventsList[0].Name
				e.PreviousPath = ""
				w.Events <- e
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
				w.Events <- e
				resetStack(eventsList)
			} else if deleteFile {
				e.Event = e.DeleteFileEvent()
				e.Path = eventsList[0].Name
				e.PreviousPath = ""
				w.Events <- e
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
				w.Events <- e
				onlyCreateEvent = false
			}
		case err := <-w.Watcher.Errors:
			w.Errors <- err
		case <-done:
			err := w.Close()
			if err != nil {
				_ = fmt.Errorf(err.Error())
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

func (w *FileWatcher) Add(path string) error {
	_, alreadyWatching := w.WatchedMap.Get(path)
	if !alreadyWatching {
		fileInfo, err := os.Stat(path)

		if os.IsNotExist(err) {
			return err
		}

		if fileInfo.IsDir() {
			// watch the directory
			w.WatchedMap.Set(path, path)
			return w.Watcher.Add(path)
		} else {
			// check if we are already watching the directory the file is in
			directory := filepath.Dir(path)
			_, watchingContainingDir := w.WatchedMap.Get(directory)

			if !watchingContainingDir {
				// not watching the directory the file is in, watch the file itself.
				w.WatchedMap.Set(path, path)
				return w.Watcher.Add(path)
			}
		}
	}
	return nil
}

func (w *FileWatcher) Remove(path string) error {
	_, ok := w.WatchedMap.Get(path)
	if ok {
		err := w.Watcher.Remove(path)

		if err != nil {
			return err
		}

		w.WatchedMap.Remove(path)
	}
	return nil
}

func (w *FileWatcher) Contains(path string) bool {
	_, ok := w.WatchedMap.Get(path)
	return ok
}

func (w *FileWatcher) Close() error {
	return w.Watcher.Close()
}
