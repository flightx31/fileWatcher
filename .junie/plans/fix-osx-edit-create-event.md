---
sessionId: session-261025-100021-1klr
---

# Requirements

### Overview & Goals
When editing files on macOS, filesystem operations often generate `fsnotify.Create` events for existing files due to atomic saves. Unconditionally emitting `CREATE_FILE` causes downstream processes to incorrectly treat existing files as brand new creations. By checking whether a path is already tracked (`w.Contains(path)`), the watcher can emit `EDIT_FILE` instead, maximizing system reliability and event accuracy with minimal computational overhead.

### Scope
- **In Scope:**
  - Checking tracking status (`w.Contains(path)`) in `watchFileChangeEvents()` inside the `delayChan` create handler.
  - Emitting `EDIT_FILE` when a create event targets an already-tracked path.
  - Ensuring new files emitted via `CREATE_FILE` (or folders via `CREATE_FOLDER`) are automatically tracked so subsequent saves register as edits.
  - Unit tests verifying create vs edit event classification for tracked and untracked files.
- **Out of Scope:**
  - Polling mechanisms for Standard or Archive files.
  - Modifying callback function signatures or external API contracts.

### Functional Requirements
- **FR-1:** In `watchFileChangeEvents()` (within the `delayChan` handler), check `w.Contains(eventsList[0].Name)` before classifying a non-directory create event.
- **FR-2:** If the file path is already tracked, assign `e.Event = e.EditFileEvent()`.
- **FR-3:** If the file path is not tracked, assign `e.Event = e.CreateFileEvent()`.
- **FR-4:** In `sendEvent()`, when `IsCreateFileEvent()` or `IsCreateFolderEvent()` occurs, register the path into the active watches map via `w.addToMap(e.Path, t)`.

# Technical Design

### Current Implementation
In `watcher.go`:
- `watchFileChangeEvents()` queues standalone create events in `delayChan`.
- Inside `case <-delayChan:`, if `fileInfo` is not a directory, `e.Event = e.CreateFileEvent()` is set unconditionally.
- In `sendEvent()`, create events do not add newly discovered files to `w.addToMap()`.

### Proposed Changes
1. **Event Resolution in `watchFileChangeEvents()` (`watcher.go`):**
   - In `case <-delayChan:`, check if `fileInfo != nil && fileInfo.IsDir()`.
   - If not a directory, evaluate `w.Contains(eventsList[0].Name)`:
     - If `true`: set `e.Event = e.EditFileEvent()`.
     - If `false`: set `e.Event = e.CreateFileEvent()`.
2. **Track Newly Created Files in `sendEvent()` (`watcher.go`):**
   - Add a branch for `e.IsCreateFileEvent() || e.IsCreateFolderEvent()` to call `w.addToMap(e.Path, t)`.
3. **Unit Tests in `watcher_test.go`:**
   - Add test cases checking that create events on existing tracked files produce `EDIT_FILE`.
   - Add test cases verifying that `CREATE_FILE` tracks new files so subsequent events trigger `EDIT_FILE`.

### Key Decisions
- **Direct Check via `w.Contains`:** Reusing the existing thread-safe tracking lookup maximizes operational efficiency, minimizes code churn, and prevents duplicate data structures.

# Testing

### Validation Approach
Automated unit tests in `watcher_test.go` verifying event classification and lifecycle tracking.

### Key Scenarios
- **Tracked File Save:** A file already registered in `FileWatcher` receiving an `fsnotify.Create` event emits `EDIT_FILE`.
- **New File Creation:** An untracked file receiving an `fsnotify.Create` event emits `CREATE_FILE` and is immediately added to tracking.
- **Subsequent File Modification:** Modifying a previously created and now-tracked file emits `EDIT_FILE`.

# Delivery Steps

### ✓ Step 1: Update create event handling and tracking in watcher.go
Create events on already tracked files emit `EDIT_FILE` instead of `CREATE_FILE`, and newly created files are registered into tracking maps.

- In `watcher.go` (`watchFileChangeEvents`), update `case <-delayChan:` to check `w.Contains(eventsList[0].Name)`.
- Set `e.Event = e.EditFileEvent()` if tracked, and `e.Event = e.CreateFileEvent()` if untracked.
- In `sendEvent()`, add `w.addToMap(e.Path, t)` when `e.IsCreateFileEvent()` or `e.IsCreateFolderEvent()` occurs.

### ✓ Step 2: Add unit tests for create vs edit event handling
Unit tests in `watcher_test.go` verify correct event emission and state transitions.

- Add a test simulating create events on tracked files to confirm `EDIT_FILE` is dispatched.
- Add a test verifying untracked file creation emits `CREATE_FILE` and that subsequent events on that path produce `EDIT_FILE`.