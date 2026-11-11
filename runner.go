package minicirunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default runtime limits and statuses.
const (
	DefaultMaxParallel = 4
	DefaultMaxLogBytes = 1 << 20   // 1 MiB
	MaxWorkspaceBytes  = 512 << 20 // 512 MiB
	LogTruncatedMarker = "[log truncated]"
	StatusSuccess      = "success"
	StatusFailure      = "failure"
	StatusSkipped      = "skipped"
	StatusTimedOut     = "timed_out"
	StatusCancelled    = "cancelled"
	EventJobStarted    = "job_started"
	EventJobFinished   = "job_finished"
	EventCacheHit      = "cache_hit"
	EventCacheMiss     = "cache_miss"
	EventCacheSaved    = "cache_saved"
	EventArtifactSaved = "artifact_saved"
)

// Options configures a pipeline execution.
type Options struct {
	Runtime      Runtime
	SourceDir    string
	WorkDir      string
	CacheDir     string
	ArtifactsDir string
	MaxParallel  int
	MaxLogBytes  int
	Logs         func(LogLine)
	OnEvent      func(Event)
}

// Report summarizes the outcome of a pipeline run.
type Report struct {
	OK   bool
	Jobs []JobResult
}

// JobResult records the final status of a single expanded job.
type JobResult struct {
	ID       string
	Status   string
	ExitCode int
	Duration time.Duration
}

// LogLine is a single line of output produced by a job.
type LogLine struct {
	Job  string
	Text string
}

// Event represents a lifecycle notification emitted during pipeline execution.
type Event struct {
	Kind   string
	Job    string
	Detail string
}

// Run executes all jobs in the pipeline respecting dependencies, concurrency limits,
// caches, artifacts, and timeouts.
func Run(ctx context.Context, p *Pipeline, opts Options) (*Report, error) {
	if p == nil {
		return nil, newError(ErrCodeRuntime, "pipeline is nil")
	}
	if opts.Runtime == nil {
		return nil, newError(ErrCodeRuntime, "Options.Runtime is required")
	}
	if opts.SourceDir == "" {
		opts.SourceDir = "."
	}
	if opts.MaxParallel <= 0 {
		opts.MaxParallel = DefaultMaxParallel
	}
	if opts.MaxLogBytes <= 0 {
		opts.MaxLogBytes = DefaultMaxLogBytes
	}

	if opts.WorkDir == "" {
		tmp, err := os.MkdirTemp("", "minicirunner-work-*")
		if err != nil {
			return nil, newErrorf(ErrCodeRuntime, "failed to create temporary workdir: %v", err)
		}
		defer os.RemoveAll(tmp)
		opts.WorkDir = tmp
	}

	r := &engine{
		ctx:     ctx,
		p:       p,
		opts:    opts,
		results: make(map[string]*JobResult, len(p.Jobs)),
		state:   make(map[string]jobState, len(p.Jobs)),
		jobsMap: make(map[string]*Job, len(p.Jobs)),
	}
	for _, j := range p.Jobs {
		r.jobsMap[j.ID] = j
		r.state[j.ID] = statePending
	}
	return r.execute()
}

type jobState int

const (
	statePending jobState = iota
	stateRunning
	stateDone
)

type engine struct {
	ctx      context.Context
	p        *Pipeline
	opts     Options
	mu       sync.Mutex
	cbMu     sync.Mutex
	cacheMu  sync.Mutex
	results  map[string]*JobResult
	state    map[string]jobState
	jobsMap  map[string]*Job
	fatalErr error
}

func (e *engine) emitEvent(ev Event) {
	if e.opts.OnEvent == nil {
		return
	}
	e.cbMu.Lock()
	defer e.cbMu.Unlock()
	e.opts.OnEvent(ev)
}

func (e *engine) emitLog(line LogLine) {
	if e.opts.Logs == nil {
		return
	}
	e.cbMu.Lock()
	defer e.cbMu.Unlock()
	e.opts.Logs(line)
}

func (e *engine) execute() (*Report, error) {
	doneCh := make(chan string, len(e.p.Jobs))
	running := 0

	for {
		e.mu.Lock()
		// Propagate skips in waves until no more jobs can be skipped immediately.
		changed := true
		for changed {
			changed = false
			for _, j := range e.p.Jobs {
				if e.state[j.ID] != statePending {
					continue
				}
				if e.shouldSkip(j) {
					e.state[j.ID] = stateDone
					e.results[j.ID] = &JobResult{
						ID:       j.ID,
						Status:   StatusSkipped,
						ExitCode: 0,
						Duration: 0,
					}
					changed = true
				}
			}
		}

		// If context is cancelled, any remaining pending jobs become skipped/cancelled.
		if e.ctx.Err() != nil {
			for _, j := range e.p.Jobs {
				if e.state[j.ID] == statePending {
					e.state[j.ID] = stateDone
					e.results[j.ID] = &JobResult{
						ID:       j.ID,
						Status:   StatusSkipped,
						ExitCode: 0,
						Duration: 0,
					}
				}
			}
		}

		// Start ready jobs in sorted ID order (p.Jobs is already sorted by ID).
		for _, j := range e.p.Jobs {
			if running >= e.opts.MaxParallel {
				break
			}
			if e.state[j.ID] == statePending && e.isReady(j) {
				e.state[j.ID] = stateRunning
				running++
				job := j
				go func() {
					res, fatal := e.runJob(job)
					e.mu.Lock()
					// Never rewrite a finished job's status.
					if e.results[job.ID] == nil {
						e.results[job.ID] = res
					}
					e.state[job.ID] = stateDone
					if fatal != nil && e.fatalErr == nil {
						e.fatalErr = fatal
					}
					e.mu.Unlock()
					doneCh <- job.ID
				}()
			}
		}

		allDone := len(e.results) == len(e.p.Jobs) && running == 0
		if allDone {
			e.mu.Unlock()
			break
		}
		if running == 0 {
			// No jobs running and not all done: remaining pending jobs are unschedulable (e.g. dependencies skipped/failed)
			for _, j := range e.p.Jobs {
				if e.state[j.ID] == statePending {
					e.state[j.ID] = stateDone
					e.results[j.ID] = &JobResult{
						ID:       j.ID,
						Status:   StatusSkipped,
						ExitCode: 0,
						Duration: 0,
					}
				}
			}
			e.mu.Unlock()
			break
		}
		e.mu.Unlock()

		<-doneCh
		running--
	}

	e.mu.Lock()
	fatal := e.fatalErr
	e.mu.Unlock()
	if fatal != nil {
		return nil, fatal
	}

	results := make([]JobResult, 0, len(e.p.Jobs))
	allOK := true
	for _, j := range e.p.Jobs {
		r := e.results[j.ID]
		if r.Status != StatusSuccess {
			allOK = false
		}
		results = append(results, *r)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].ID < results[j].ID
	})
	return &Report{
		OK:   allOK,
		Jobs: results,
	}, nil
}

func (e *engine) shouldSkip(j *Job) bool {
	for _, need := range j.Needs {
		if e.state[need] == stateDone {
			if r := e.results[need]; r != nil && r.Status != StatusSuccess {
				return true
			}
		}
	}
	return false
}

func (e *engine) isReady(j *Job) bool {
	for _, need := range j.Needs {
		if e.state[need] != stateDone {
			return false
		}
		if r := e.results[need]; r == nil || r.Status != StatusSuccess {
			return false
		}
	}
	return true
}

func (e *engine) runJob(j *Job) (*JobResult, error) {
	startTime := time.Now()
	e.emitEvent(Event{Kind: EventJobStarted, Job: j.ID})

	jobWorkDir := filepath.Join(e.opts.WorkDir, sanitizeID(j.ID))
	if err := removeAllWithPermFix(jobWorkDir); err != nil {
		return nil, newErrorf(ErrCodeRuntime, "failed to clean job workdir: %v", err)
	}
	if err := copyDirectoryWithLimit(e.opts.SourceDir, jobWorkDir, MaxWorkspaceBytes); err != nil {
		return nil, err
	}

	hashFn := func(pattern string) (string, error) {
		return computeHashFiles(e.opts.SourceDir, pattern)
	}

	// Evaluate cache key and restore if present.
	var evaluatedCacheKey string
	var cacheEntryDir string
	cached := false
	if j.Cache != nil {
		key, err := evalExpressions(j.Cache.Key, j.Matrix, hashFn)
		if err != nil {
			return nil, err
		}
		evaluatedCacheKey = key
		if e.opts.CacheDir != "" {
			sum := sha256.Sum256([]byte(evaluatedCacheKey))
			cacheHash := hex.EncodeToString(sum[:])
			cacheEntryDir = filepath.Join(e.opts.CacheDir, cacheHash)

			e.cacheMu.Lock()
			if info, err := os.Stat(cacheEntryDir); err == nil && info.IsDir() {
				cached = true
				for _, rel := range j.Cache.Paths {
					src := filepath.Join(cacheEntryDir, filepath.FromSlash(rel))
					dst := filepath.Join(jobWorkDir, filepath.FromSlash(rel))
					if _, statErr := os.Stat(src); statErr == nil {
						if cpErr := copyPath(src, dst, MaxWorkspaceBytes); cpErr != nil {
							e.cacheMu.Unlock()
							return nil, cpErr
						}
					}
				}
			}
			e.cacheMu.Unlock()

			if cached {
				e.emitEvent(Event{Kind: EventCacheHit, Job: j.ID, Detail: evaluatedCacheKey})
			} else {
				e.emitEvent(Event{Kind: EventCacheMiss, Job: j.ID, Detail: evaluatedCacheKey})
			}
		}
	}

	// Evaluate environment variables.
	evalEnv := make(map[string]string, len(j.Env))
	for k, v := range j.Env {
		val, err := evalExpressions(v, j.Matrix, hashFn)
		if err != nil {
			return nil, err
		}
		evalEnv[k] = val
	}

	logWriter := newJobLogWriter(j.ID, e.opts.MaxLogBytes, e.emitLog)
	jobCtx, cancel := context.WithTimeout(e.ctx, j.Timeout)
	defer cancel()

	status := StatusSuccess
	exitCode := 0
	var fatalErr error

	for _, step := range j.Steps {
		cmdStr, err := evalExpressions(step.Run, j.Matrix, hashFn)
		if err != nil {
			fatalErr = err
			break
		}
		spec := StepSpec{
			Image:   j.Image,
			Command: cmdStr,
			Workdir: jobWorkDir,
			Env:     evalEnv,
		}
		code, runErr := e.opts.Runtime.Run(jobCtx, spec, logWriter)
		if e.ctx.Err() != nil {
			status = StatusCancelled
			exitCode = code
			break
		}
		if jobCtx.Err() == context.DeadlineExceeded {
			status = StatusTimedOut
			exitCode = code
			break
		}
		if runErr != nil {
			fatalErr = runErr
			break
		}
		if code != 0 {
			status = StatusFailure
			exitCode = code
			break
		}
	}

	logWriter.Flush()

	if fatalErr != nil {
		return nil, fatalErr
	}

	// Save cache on success only when absent (caches are immutable).
	if status == StatusSuccess && j.Cache != nil && cacheEntryDir != "" && !cached {
		e.cacheMu.Lock()
		if _, err := os.Stat(cacheEntryDir); os.IsNotExist(err) {
			if err := os.MkdirAll(cacheEntryDir, 0o755); err != nil {
				e.cacheMu.Unlock()
				return nil, newErrorf(ErrCodeRuntime, "failed to create cache dir: %v", err)
			}
			for _, rel := range j.Cache.Paths {
				src := filepath.Join(jobWorkDir, filepath.FromSlash(rel))
				dst := filepath.Join(cacheEntryDir, filepath.FromSlash(rel))
				if _, statErr := os.Stat(src); statErr == nil {
					if cpErr := copyPath(src, dst, MaxWorkspaceBytes); cpErr != nil {
						e.cacheMu.Unlock()
						return nil, cpErr
					}
				}
			}
			e.cacheMu.Unlock()
			e.emitEvent(Event{Kind: EventCacheSaved, Job: j.ID, Detail: evaluatedCacheKey})
		} else {
			e.cacheMu.Unlock()
		}
	}

	// Copy artifacts regardless of job status.
	if e.opts.ArtifactsDir != "" && len(j.Artifacts) > 0 {
		jobArtDir := filepath.Join(e.opts.ArtifactsDir, sanitizeArtifactJobID(j.ID))
		for _, rel := range j.Artifacts {
			src := filepath.Join(jobWorkDir, filepath.FromSlash(rel))
			if _, statErr := os.Stat(src); statErr == nil {
				dst := filepath.Join(jobArtDir, filepath.FromSlash(rel))
				if cpErr := copyPath(src, dst, MaxWorkspaceBytes); cpErr != nil {
					return nil, cpErr
				}
				e.emitEvent(Event{Kind: EventArtifactSaved, Job: j.ID, Detail: rel})
			}
		}
	}

	duration := time.Since(startTime)
	e.emitEvent(Event{Kind: EventJobFinished, Job: j.ID, Detail: status})
	return &JobResult{
		ID:       j.ID,
		Status:   status,
		ExitCode: exitCode,
		Duration: duration,
	}, nil
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	sum := sha256.Sum256([]byte(id))
	return b.String() + "-" + hex.EncodeToString(sum[:4])
}

func sanitizeArtifactJobID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func removeAllWithPermFix(path string) error {
	err := os.RemoveAll(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr == nil {
			_ = os.Chmod(p, 0o777)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// computeHashFiles walks sourceDir, matches files against pattern using path.Match
// on relative slash paths, and returns the hex SHA-256 of:
//
//	path + NUL + hex_sha256(content) + "\n"
//
// concatenated in sorted slash-path order.
func computeHashFiles(sourceDir, pattern string) (string, error) {
	var matched []string
	err := filepath.WalkDir(sourceDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(sourceDir, p)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)
		ok, err := path.Match(pattern, slashRel)
		if err != nil {
			return err
		}
		if ok {
			matched = append(matched, slashRel)
		}
		return nil
	})
	if err != nil {
		return "", newErrorf(ErrCodeRuntime, "hashFiles walk failed: %v", err)
	}
	sort.Strings(matched)

	outer := sha256.New()
	for _, slashRel := range matched {
		fullPath := filepath.Join(sourceDir, filepath.FromSlash(slashRel))
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return "", newErrorf(ErrCodeRuntime, "hashFiles read failed: %v", err)
		}
		contentSum := sha256.Sum256(data)
		outer.Write([]byte(slashRel))
		outer.Write([]byte{0})
		outer.Write([]byte(hex.EncodeToString(contentSum[:])))
		outer.Write([]byte{'\n'})
	}
	return hex.EncodeToString(outer.Sum(nil)), nil
}

func copyDirectoryWithLimit(srcDir, dstDir string, maxBytes int64) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return newErrorf(ErrCodeRuntime, "failed to create directory: %v", err)
	}
	absDst, err := filepath.Abs(dstDir)
	if err != nil {
		return newErrorf(ErrCodeRuntime, "resolve dst failed: %v", err)
	}
	var total int64
	return copyRecursive(srcDir, dstDir, absDst, &total, maxBytes)
}

func copyPath(src, dst string, maxBytes int64) error {
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return newErrorf(ErrCodeRuntime, "resolve dst failed: %v", err)
	}
	var total int64
	return copyRecursive(src, dst, absDst, &total, maxBytes)
}

func copyRecursive(src, dst, excludePrefix string, total *int64, maxBytes int64) error {
	absSrc, err := filepath.Abs(src)
	if err == nil && excludePrefix != "" {
		if absSrc == excludePrefix || strings.HasPrefix(absSrc, excludePrefix+string(filepath.Separator)) {
			return nil
		}
	}
	info, err := os.Lstat(src)
	if err != nil {
		return newErrorf(ErrCodeRuntime, "stat failed during copy: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return newErrorf(ErrCodeRuntime, "mkdir failed during copy: %v", err)
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return newErrorf(ErrCodeRuntime, "readdir failed during copy: %v", err)
		}
		for _, entry := range entries {
			if err := copyRecursive(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), excludePrefix, total, maxBytes); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if *total+info.Size() > maxBytes {
		return newErrorf(ErrCodeRuntime, "copy size exceeds limit of %d bytes", maxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return newErrorf(ErrCodeRuntime, "mkdir parent failed: %v", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return newErrorf(ErrCodeRuntime, "open source failed: %v", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return newErrorf(ErrCodeRuntime, "create destination failed: %v", err)
	}
	defer out.Close()

	n, err := io.Copy(out, io.LimitReader(in, maxBytes-*total+1))
	if err != nil {
		return newErrorf(ErrCodeRuntime, "file copy failed: %v", err)
	}
	*total += n
	if *total > maxBytes {
		return newErrorf(ErrCodeRuntime, "copy size exceeds limit of %d bytes", maxBytes)
	}
	return nil
}

type jobLogWriter struct {
	mu        sync.Mutex
	job       string
	maxBytes  int
	usedBytes int
	truncated bool
	buf       bytes.Buffer
	emit      func(LogLine)
}

func newJobLogWriter(job string, maxBytes int, emit func(LogLine)) *jobLogWriter {
	return &jobLogWriter{
		job:      job,
		maxBytes: maxBytes,
		emit:     emit,
	}
}

func (w *jobLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	origLen := len(p)
	if w.truncated {
		return origLen, nil
	}

	remaining := w.maxBytes - w.usedBytes
	if len(p) > remaining {
		if remaining > 0 {
			w.buf.Write(p[:remaining])
			w.usedBytes += remaining
		}
		w.drainCompleteLines()
		if w.buf.Len() > 0 {
			text := strings.TrimSuffix(w.buf.String(), "\r")
			w.buf.Reset()
			w.emit(LogLine{Job: w.job, Text: text})
		}
		w.truncated = true
		w.emit(LogLine{Job: w.job, Text: LogTruncatedMarker})
		return origLen, nil
	}

	w.buf.Write(p)
	w.usedBytes += len(p)
	w.drainCompleteLines()
	return origLen, nil
}

func (w *jobLogWriter) drainCompleteLines() {
	for {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx == -1 {
			break
		}
		line := string(data[:idx])
		line = strings.TrimSuffix(line, "\r")
		w.buf.Next(idx + 1)
		w.emit(LogLine{Job: w.job, Text: line})
	}
}

func (w *jobLogWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return
	}
	w.drainCompleteLines()
	if w.buf.Len() > 0 {
		line := strings.TrimSuffix(w.buf.String(), "\r")
		w.buf.Reset()
		w.emit(LogLine{Job: w.job, Text: line})
	}
}
