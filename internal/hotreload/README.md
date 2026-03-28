# Package `hotreload`

**Import path:** `intelligent-lb/internal/hotreload`

The `hotreload` package enables zero-downtime configuration updates. When the config file changes on disk, it triggers a callback that rebuilds the load balancer's runtime components without restarting the process or interrupting in-flight requests. Inspired by Traefik's file provider which watches for config changes.

---

## File Structure

```
hotreload/
└── watcher.go  — Watcher struct, NewWatcher, run(), Stop()
```

---

## `Watcher` struct

```go
type Watcher struct {
    watcher  *fsnotify.Watcher  // underlying fsnotify watcher
    path     string              // config file path being watched
    onReload ReloadFunc          // callback invoked on change
    done     chan struct{}        // close to stop the event loop goroutine
    mu       sync.Mutex          // protects onReload from concurrent invocation
}
```

### `ReloadFunc`
```go
type ReloadFunc func(path string) error
```
The callback to invoke when the config changes. Receives the file path (can be ignored if fixed). Should:
1. Parse the new config file.
2. Rebuild service, router, and dashboard components.
3. Return an error if the reload fails (the error is logged; the old config remains active).

---

## `NewWatcher(path string, onReload ReloadFunc) (*Watcher, error)`

```go
fw, err := fsnotify.NewWatcher()          // creates an OS-level file watch
fw.Add(path)                              // registers the file for watching
go w.run()                                // starts the event loop in a goroutine
log.Printf("[HOTRELOAD] Watching config file: %s", path)
```

If `fsnotify.NewWatcher()` or `fw.Add(path)` fails, the function returns an error without starting any goroutine.

---

## `run()` — Event Loop

The event loop is the core of the watcher. It runs in a goroutine for the process lifetime.

```go
func (w *Watcher) run() {
    var timer *time.Timer
    var timerMu sync.Mutex

    resetTimer := func() {
        timerMu.Lock(); defer timerMu.Unlock()
        if timer != nil { timer.Stop() }
        timer = time.AfterFunc(300*time.Millisecond, func() {
            w.mu.Lock(); defer w.mu.Unlock()
            log.Printf("[HOTRELOAD] Config file changed, reloading: %s", w.path)
            if err := w.onReload(w.path); err != nil {
                log.Printf("[HOTRELOAD] Reload failed: %v", err)
            } else {
                log.Printf("[HOTRELOAD] Config reloaded successfully")
            }
        })
    }

    for {
        select {
        case event, ok := <-w.watcher.Events:
            if !ok { return }
            if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
                resetTimer()
            }
            if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
                time.AfterFunc(100*time.Millisecond, func() { _ = w.watcher.Add(w.path) })
                resetTimer()
            }
        case err, ok := <-w.watcher.Errors:
            if !ok { return }
            log.Printf("[HOTRELOAD] Watcher error: %v", err)
        case <-w.done:
            return
        }
    }
}
```

### Why 300ms Debounce?

Many text editors save files in multiple steps:
1. Write partial content (triggers `Write` event)
2. Rename temp file to target (triggers `Rename`/`Create`)

Without debouncing, the callback would fire 2–3 times per save. The 300ms timer ensures the callback fires only once after the editor finishes all file operations.

If another event arrives within 300ms, `timer.Stop()` cancels the pending callback and a new timer starts (effectively restarting the debounce window).

### Editor Compatibility

| Editor Save Pattern | Events Generated | Watcher Response |
|---|---|---|
| Direct write (nano, echo redirect) | `Write` | `resetTimer()` |
| Write + rename (Vim, VS Code) | `Write` then `Rename` | `resetTimer()` twice; one callback |
| Remove + create (some editors) | `Remove` then `Create` | Re-watch after 100ms + `resetTimer()` |

The 100ms `time.AfterFunc` for `Remove`/`Rename` re-adds the file path to the watcher. Without this, the watcher loses track of the new file after it's recreated.

### Concurrency Safety

- `timerMu` protects `timer` (the pending `*time.Timer`) from concurrent `resetTimer()` calls if events arrive rapidly.
- `w.mu` protects `w.onReload` — ensures only one reload runs at a time. If the config file changes again while a reload is in progress, the second reload will wait (via `mu.Lock()`).

---

## `Stop()`

```go
func (w *Watcher) Stop() {
    close(w.done)          // signals the run() goroutine to exit
    w.watcher.Close()      // closes the fsnotify watcher and its channels
}
```

Closing `w.done` causes the `select` in `run()` to select `case <-w.done: return`. Closing the fsnotify watcher causes `w.watcher.Events` and `w.watcher.Errors` channels to close, which also causes the goroutine to exit (both paths are safe).

---

## Log Output Reference

| Situation | Log Message |
|---|---|
| Watcher created | `[HOTRELOAD] Watching config file: /path/to/config.json` |
| File change detected | `[HOTRELOAD] Config file changed, reloading: /path/to/config.json` |
| Reload succeeded | `[HOTRELOAD] Config reloaded successfully` |
| Reload failed | `[HOTRELOAD] Reload failed: <error message>` |
| fsnotify error | `[HOTRELOAD] Watcher error: <error message>` |

---

## Full Usage Example

```go
watcher, err := hotreload.NewWatcher(configPath, func(path string) error {
    // 1. Parse the new config
    newCfg, err := config.Load(path)
    if err != nil {
        return fmt.Errorf("config parse error: %w", err)
    }

    // 2. Build new service manager
    newSvcMgr := service.NewManager(newCfg)

    // 3. Preserve metrics from old manager
    newSvcMgr.ImportMetrics(oldSvcMgr)

    // 4. Stop old health check goroutines
    oldSvcMgr.Stop()
    oldSvcMgr = newSvcMgr

    // 5. Rebuild HTTP router
    rebuildRouter(newCfg, newSvcMgr)

    // 6. Update dashboard
    dashboardHub.SetProvider(newSvcMgr)

    return nil
})
if err != nil {
    log.Fatalf("Failed to start config watcher: %v", err)
}
defer watcher.Stop()
```

---

## Dependencies

| Package | Role |
|---|---|
| `github.com/fsnotify/fsnotify` | Cross-platform filesystem event notifications (uses inotify on Linux, FSEvents on macOS, ReadDirectoryChangesW on Windows) |
| `sync` | `Mutex` protecting concurrent reload invocations; `timerMu` protecting timer state |
| `time` | 300ms debounce timer via `time.AfterFunc`; 100ms re-watch delay |
| `log` | Structured event logging |
