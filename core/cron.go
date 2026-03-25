package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// CronJob represents a persisted scheduled task.
type CronJob struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	SessionKey  string    `json:"session_key"`
	CronExpr    string    `json:"cron_expr"`
	Prompt      string    `json:"prompt"`
	Exec        string    `json:"exec,omitempty"`     // shell command; mutually exclusive with Prompt
	WorkDir     string    `json:"work_dir,omitempty"` // working directory for exec; empty = agent work_dir
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	Silent      *bool     `json:"silent,omitempty"`       // suppress start notification; nil = use global default
	Mute        bool      `json:"mute,omitempty"`         // suppress ALL messages (start + result); job runs silently
	SessionMode string    `json:"session_mode,omitempty"` // "" or "reuse" = share active session; "new_per_run" = fresh session each run
	TimeoutMins *int      `json:"timeout_mins,omitempty"` // nil = default 30m wait; 0 = no limit; >0 = minutes
	CreatedAt   time.Time `json:"created_at"`
	LastRun     time.Time `json:"last_run,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// IsShellJob returns true if the job runs a shell command directly.
func (j *CronJob) IsShellJob() bool {
	return j.Exec != ""
}

const defaultCronJobTimeout = 30 * time.Minute

// ExecutionTimeout returns how long the scheduler waits for the job goroutine to finish.
// nil TimeoutMins uses 30 minutes. *TimeoutMins == 0 means wait without a time limit.
// *TimeoutMins > 0 means that many minutes.
func (j *CronJob) ExecutionTimeout() time.Duration {
	if j.TimeoutMins == nil {
		return defaultCronJobTimeout
	}
	if *j.TimeoutMins <= 0 {
		return 0
	}
	return time.Duration(*j.TimeoutMins) * time.Minute
}

// UsesNewSessionPerRun reports whether each cron run should use a new engine session
// instead of reusing the active session for the session_key.
func (j *CronJob) UsesNewSessionPerRun() bool {
	return NormalizeCronSessionMode(j.SessionMode) == "new_per_run"
}

// NormalizeCronSessionMode maps CLI/API aliases to canonical values ("", "new_per_run").
// Returns the original string if unrecognized (caller should validate).
func NormalizeCronSessionMode(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	switch low {
	case "", "reuse":
		return ""
	case "new_per_run", "new-per-run":
		return "new_per_run"
	default:
		return s
	}
}

func validateCronJob(j *CronJob) error {
	mode := NormalizeCronSessionMode(j.SessionMode)
	if mode != "" && mode != "new_per_run" {
		return fmt.Errorf("invalid session_mode %q (want reuse, new_per_run, or new-per-run)", j.SessionMode)
	}
	if j.TimeoutMins != nil && *j.TimeoutMins < 0 {
		return fmt.Errorf("timeout_mins must be >= 0")
	}
	return nil
}

// CronStore persists cron jobs to a JSON file.
type CronStore struct {
	path string
	mu   sync.Mutex
	jobs []*CronJob
}

func NewCronStore(dataDir string) (*CronStore, error) {
	dir := filepath.Join(dataDir, "crons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "jobs.json")
	s := &CronStore{path: path}
	s.load()
	return s, nil
}

func (s *CronStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &s.jobs); err != nil {
		slog.Error("cron: failed to load jobs", "path", s.path, "error", err)
	}
}

func (s *CronStore) save() error {
	data, err := json.MarshalIndent(s.jobs, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.path, data, 0o644)
}

func (s *CronStore) Add(job *CronJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
	return s.save()
}

func (s *CronStore) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.ID == id {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			if err := s.save(); err != nil {
				slog.Warn("cron: failed to save after remove", "error", err)
			}
			return true
		}
	}
	return false
}

func (s *CronStore) SetEnabled(id string, enabled bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.Enabled = enabled
			if err := s.save(); err != nil {
				slog.Warn("cron: failed to save after set enabled", "error", err)
			}
			return true
		}
	}
	return false
}

func (s *CronStore) SetMute(id string, mute bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.Mute = mute
			if err := s.save(); err != nil {
				slog.Warn("cron: save after mute toggle", "error", err)
			}
			return true
		}
	}
	return false
}

func (s *CronStore) ToggleMute(id string) (newState bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.Mute = !j.Mute
			if err := s.save(); err != nil {
				slog.Warn("cron: failed to save after toggle mute", "error", err)
			}
			return j.Mute, true
		}
	}
	return false, false
}

func (s *CronStore) MarkRun(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.LastRun = time.Now()
			if err != nil {
				j.LastError = err.Error()
			} else {
				j.LastError = ""
			}
			if saveErr := s.save(); saveErr != nil {
				slog.Warn("cron: failed to save after mark run", "error", saveErr)
			}
			return
		}
	}
}

func (s *CronStore) List() []*CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*CronJob, len(s.jobs))
	copy(out, s.jobs)
	return out
}

func (s *CronStore) ListByProject(project string) []*CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*CronJob
	for _, j := range s.jobs {
		if j.Project == project {
			out = append(out, j)
		}
	}
	return out
}

func (s *CronStore) ListBySessionKey(sessionKey string) []*CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*CronJob
	for _, j := range s.jobs {
		if j.SessionKey == sessionKey {
			out = append(out, j)
		}
	}
	return out
}

func (s *CronStore) Get(id string) *CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// Update modifies a specific field of a cron job. Returns false if job not found.
// readOnlyFields contains fields that cannot be modified: id, created_at.
func (s *CronStore) Update(id string, field string, value any) bool {
	readOnlyFields := map[string]bool{"id": true, "created_at": true, "last_run": true, "last_error": true}
	if readOnlyFields[field] {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			if err := updateJobField(j, field, value); err != nil {
				return false
			}
			if saveErr := s.save(); saveErr != nil {
				slog.Warn("cron: failed to save after update", "error", saveErr)
			}
			return true
		}
	}
	return false
}

// updateJobField sets a field on a CronJob by reflection. Returns error for unknown fields.
func updateJobField(job *CronJob, field string, value any) error {
	switch field {
	case "project":
		if v, ok := value.(string); ok {
			job.Project = v
			return nil
		}
	case "session_key":
		if v, ok := value.(string); ok {
			job.SessionKey = v
			return nil
		}
	case "cron_expr":
		if v, ok := value.(string); ok {
			job.CronExpr = v
			return nil
		}
	case "prompt":
		if v, ok := value.(string); ok {
			job.Prompt = v
			return nil
		}
	case "exec":
		if v, ok := value.(string); ok {
			job.Exec = v
			return nil
		}
	case "work_dir":
		if v, ok := value.(string); ok {
			job.WorkDir = v
			return nil
		}
	case "description":
		if v, ok := value.(string); ok {
			job.Description = v
			return nil
		}
	case "enabled":
		if v, ok := value.(bool); ok {
			job.Enabled = v
			return nil
		}
	case "silent":
		if v, ok := value.(bool); ok {
			job.Silent = &v
			return nil
		}
	case "mute":
		if v, ok := value.(bool); ok {
			job.Mute = v
			return nil
		}
	case "session_mode":
		if v, ok := value.(string); ok {
			job.SessionMode = v
			return nil
		}
	case "timeout_mins":
		if v, ok := value.(float64); ok {
			n := int(v)
			job.TimeoutMins = &n
			return nil
		}
		if v, ok := value.(int); ok {
			job.TimeoutMins = &v
			return nil
		}
	}
	// Fallback: try to set string field via reflection
	if v, ok := value.(string); ok {
		rv := reflect.ValueOf(job).Elem()
		f := rv.FieldByName(toExportedFieldName(field))
		if f.IsValid() && f.Kind() == reflect.String && f.CanSet() {
			f.SetString(v)
			return nil
		}
	}
	return fmt.Errorf("unknown or invalid field: %s", field)
}

// toExportedFieldName converts snake_case to Go exported field name (e.g., "session_key" -> "SessionKey")
func toExportedFieldName(s string) string {
	result := make([]byte, 0, len(s))
	upperNext := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			if c >= 'a' && c <= 'z' {
				c -= 32
			}
			upperNext = false
		}
		result = append(result, c)
	}
	return string(result)
}

// CronScheduler runs cron jobs by injecting synthetic messages into engines.
type CronScheduler struct {
	store         *CronStore
	cron          *cron.Cron
	engines       map[string]*Engine // project name → engine
	mu            sync.RWMutex
	entries       map[string]cron.EntryID // job ID → cron entry
	defaultSilent bool                    // global default for suppressing cron start notifications
}

func NewCronScheduler(store *CronStore) *CronScheduler {
	return &CronScheduler{
		store:   store,
		cron:    cron.New(),
		engines: make(map[string]*Engine),
		entries: make(map[string]cron.EntryID),
	}
}

func (cs *CronScheduler) RegisterEngine(name string, e *Engine) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.engines[name] = e
}

func (cs *CronScheduler) SetDefaultSilent(silent bool) {
	cs.defaultSilent = silent
}

// IsSilent returns whether the cron job should suppress the start notification.
func (cs *CronScheduler) IsSilent(job *CronJob) bool {
	if job.Silent != nil {
		return *job.Silent
	}
	return cs.defaultSilent
}

func (cs *CronScheduler) Start() error {
	jobs := cs.store.List()
	for _, job := range jobs {
		if job.Enabled {
			if err := cs.scheduleJob(job); err != nil {
				slog.Warn("cron: failed to schedule job", "id", job.ID, "error", err)
			}
		}
	}
	cs.cron.Start()
	slog.Info("cron: scheduler started", "jobs", len(jobs))
	return nil
}

func (cs *CronScheduler) Stop() {
	cs.cron.Stop()
}

func (cs *CronScheduler) AddJob(job *CronJob) error {
	if err := validateCronJob(job); err != nil {
		return err
	}
	job.SessionMode = NormalizeCronSessionMode(job.SessionMode)
	if _, err := cron.ParseStandard(job.CronExpr); err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", job.CronExpr, err)
	}
	if err := cs.store.Add(job); err != nil {
		return err
	}
	if job.Enabled {
		return cs.scheduleJob(job)
	}
	return nil
}

func (cs *CronScheduler) RemoveJob(id string) bool {
	cs.mu.Lock()
	if entryID, ok := cs.entries[id]; ok {
		cs.cron.Remove(entryID)
		delete(cs.entries, id)
	}
	cs.mu.Unlock()
	return cs.store.Remove(id)
}

func (cs *CronScheduler) EnableJob(id string) error {
	if !cs.store.SetEnabled(id, true) {
		return fmt.Errorf("job %q not found", id)
	}
	job := cs.store.Get(id)
	if job != nil {
		return cs.scheduleJob(job)
	}
	return nil
}

func (cs *CronScheduler) DisableJob(id string) error {
	if !cs.store.SetEnabled(id, false) {
		return fmt.Errorf("job %q not found", id)
	}
	cs.mu.Lock()
	if entryID, ok := cs.entries[id]; ok {
		cs.cron.Remove(entryID)
		delete(cs.entries, id)
	}
	cs.mu.Unlock()
	return nil
}

// UpdateJob modifies a field of a cron job and reschedules if necessary.
// Returns error if job not found, field is read-only, or value is invalid.
func (cs *CronScheduler) UpdateJob(id string, field string, value any) error {
	job := cs.store.Get(id)
	if job == nil {
		return fmt.Errorf("job %q not found", id)
	}

	// Validate cron expression if updating cron_expr
	if field == "cron_expr" {
		expr, ok := value.(string)
		if !ok {
			return fmt.Errorf("cron_expr must be a string")
		}
		if _, err := cron.ParseStandard(expr); err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", expr, err)
		}
	}

	// Check if reschedule is needed
	needsReschedule := field == "cron_expr" || field == "enabled"

	if needsReschedule {
		// Remove current schedule
		cs.mu.Lock()
		if entryID, ok := cs.entries[id]; ok {
			cs.cron.Remove(entryID)
			delete(cs.entries, id)
		}
		cs.mu.Unlock()
	}

	// Update the field
	if !cs.store.Update(id, field, value) {
		return fmt.Errorf("failed to update field %q (may be read-only or invalid type)", field)
	}

	// Reschedule if needed
	if needsReschedule {
		updatedJob := cs.store.Get(id)
		if updatedJob != nil && updatedJob.Enabled {
			if err := cs.scheduleJob(updatedJob); err != nil {
				return fmt.Errorf("reschedule failed: %w", err)
			}
		}
	}

	return nil
}

func (cs *CronScheduler) Store() *CronStore {
	return cs.store
}

// NextRun returns the next scheduled run time for a job, or zero if not scheduled.
func (cs *CronScheduler) NextRun(jobID string) time.Time {
	cs.mu.RLock()
	entryID, ok := cs.entries[jobID]
	cs.mu.RUnlock()
	if !ok {
		return time.Time{}
	}
	for _, e := range cs.cron.Entries() {
		if e.ID == entryID {
			return e.Next
		}
	}
	return time.Time{}
}

func (cs *CronScheduler) scheduleJob(job *CronJob) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Remove existing schedule if any
	if old, ok := cs.entries[job.ID]; ok {
		cs.cron.Remove(old)
	}

	jobID := job.ID
	entryID, err := cs.cron.AddFunc(job.CronExpr, func() {
		cs.executeJob(jobID)
	})
	if err != nil {
		return err
	}
	cs.entries[jobID] = entryID
	return nil
}

func (cs *CronScheduler) executeJob(jobID string) {
	job := cs.store.Get(jobID)
	if job == nil || !job.Enabled {
		return
	}

	cs.mu.RLock()
	engine, ok := cs.engines[job.Project]
	cs.mu.RUnlock()

	if !ok {
		slog.Error("cron: project not found", "job", jobID, "project", job.Project)
		cs.store.MarkRun(jobID, fmt.Errorf("project %q not found", job.Project))
		return
	}

	slog.Info("cron: executing job", "id", jobID, "project", job.Project, "prompt", truncateStr(job.Prompt, 60))

	done := make(chan error, 1)
	go func() {
		done <- engine.ExecuteCronJob(job)
	}()

	var err error
	timeout := job.ExecutionTimeout()
	if timeout > 0 {
		select {
		case err = <-done:
		case <-time.After(timeout):
			err = fmt.Errorf("job timed out after %v", timeout)
		}
	} else {
		err = <-done
	}

	cs.store.MarkRun(jobID, err)

	if err != nil {
		slog.Error("cron: job failed", "id", jobID, "error", err)
	} else {
		slog.Info("cron: job completed", "id", jobID)
	}
}

// mutePlatform wraps a Platform and discards all outgoing messages.
// Used for muted cron jobs that should execute without sending chat messages.
type mutePlatform struct {
	Platform
}

func (m *mutePlatform) Reply(_ context.Context, _ any, _ string) error { return nil }
func (m *mutePlatform) Send(_ context.Context, _ any, _ string) error  { return nil }

func GenerateCronID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("generate cron id: %w", err))
	}
	return hex.EncodeToString(b)
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

var cronWeekdays = map[Language][7]string{
	LangEnglish:            {"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
	LangChinese:            {"周日", "周一", "周二", "周三", "周四", "周五", "周六"},
	LangTraditionalChinese: {"週日", "週一", "週二", "週三", "週四", "週五", "週六"},
	LangJapanese:           {"日曜", "月曜", "火曜", "水曜", "木曜", "金曜", "土曜"},
	LangSpanish:            {"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"},
}

var cronMonths = map[Language][13]string{
	LangEnglish:            {"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	LangChinese:            {"", "1月", "2月", "3月", "4月", "5月", "6月", "7月", "8月", "9月", "10月", "11月", "12月"},
	LangTraditionalChinese: {"", "1月", "2月", "3月", "4月", "5月", "6月", "7月", "8月", "9月", "10月", "11月", "12月"},
	LangJapanese:           {"", "1月", "2月", "3月", "4月", "5月", "6月", "7月", "8月", "9月", "10月", "11月", "12月"},
	LangSpanish:            {"", "ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"},
}

func cronLangNames(lang Language) (weekdays [7]string, months [13]string) {
	if w, ok := cronWeekdays[lang]; ok {
		weekdays = w
	} else {
		weekdays = cronWeekdays[LangEnglish]
	}
	if m, ok := cronMonths[lang]; ok {
		months = m
	} else {
		months = cronMonths[LangEnglish]
	}
	return
}

func isZhLikeLang(lang Language) bool {
	return lang == LangChinese || lang == LangTraditionalChinese || lang == LangJapanese
}

// parseStep parses a cron step field like "*/5" and returns (5, true).
func parseStep(field string) (int, bool) {
	if !strings.HasPrefix(field, "*/") {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(field[2:], "%d", &n); err == nil && n > 0 {
		return n, true
	}
	return 0, false
}

// CronExprToHuman converts a standard 5-field cron expression to a human-readable string.
func CronExprToHuman(expr string, lang Language) string {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return expr
	}
	minute, hour, dom, month, dow := fields[0], fields[1], fields[2], fields[3], fields[4]
	weekdays, months := cronLangNames(lang)
	cjk := isZhLikeLang(lang)
	allWild := dom == "*" && month == "*" && dow == "*"

	// Pure interval: */N * * * * → "Every N minutes"
	if minStep, ok := parseStep(minute); ok && hour == "*" && allWild {
		switch lang {
		case LangChinese:
			return fmt.Sprintf("每%d分钟", minStep)
		case LangTraditionalChinese:
			return fmt.Sprintf("每%d分鐘", minStep)
		case LangJapanese:
			return fmt.Sprintf("%d分ごと", minStep)
		case LangSpanish:
			return fmt.Sprintf("Cada %d min", minStep)
		default:
			return fmt.Sprintf("Every %d min", minStep)
		}
	}

	// Hour interval: M */N * * * → "Every N hours (:MM)"
	if hourStep, ok := parseStep(hour); ok && allWild {
		m := padZero(minute)
		if minute == "*" {
			m = "00"
		}
		switch lang {
		case LangChinese:
			return fmt.Sprintf("每%d小时 (:%s)", hourStep, m)
		case LangTraditionalChinese:
			return fmt.Sprintf("每%d小時 (:%s)", hourStep, m)
		case LangJapanese:
			return fmt.Sprintf("%d時間ごと (:%s)", hourStep, m)
		case LangSpanish:
			return fmt.Sprintf("Cada %d h (:%s)", hourStep, m)
		default:
			return fmt.Sprintf("Every %d h (:%s)", hourStep, m)
		}
	}

	var parts []string

	// Weekday
	if dow != "*" {
		if n, err := strconv.Atoi(dow); err == nil && n >= 0 && n <= 6 {
			if cjk {
				parts = append(parts, weekdays[n])
			} else {
				parts = append(parts, "Every "+weekdays[n])
			}
		} else {
			parts = append(parts, "weekday("+dow+")")
		}
	}

	// Month
	if month != "*" {
		if n, err := strconv.Atoi(month); err == nil && n >= 1 && n <= 12 {
			parts = append(parts, months[n])
		}
	}

	// Day of month
	if dom != "*" {
		if cjk {
			parts = append(parts, dom+"日")
		} else {
			parts = append(parts, "day "+dom)
		}
	}

	// Time
	if hour != "*" && minute != "*" {
		if minStep, ok := parseStep(minute); ok {
			switch lang {
			case LangChinese, LangTraditionalChinese:
				parts = append(parts, fmt.Sprintf("%s时 每%d分钟", padZero(hour), minStep))
			case LangJapanese:
				parts = append(parts, fmt.Sprintf("%s時 %d分ごと", padZero(hour), minStep))
			default:
				parts = append(parts, fmt.Sprintf("hour %s every %d min", padZero(hour), minStep))
			}
		} else {
			parts = append(parts, fmt.Sprintf("%s:%s", padZero(hour), padZero(minute)))
		}
	} else if hour != "*" {
		if cjk {
			parts = append(parts, hour+"時")
		} else {
			parts = append(parts, "hour "+hour)
		}
	} else if minute != "*" {
		if minStep, ok := parseStep(minute); ok {
			switch lang {
			case LangChinese:
				parts = append(parts, fmt.Sprintf("每%d分钟", minStep))
			case LangTraditionalChinese:
				parts = append(parts, fmt.Sprintf("每%d分鐘", minStep))
			case LangJapanese:
				parts = append(parts, fmt.Sprintf("%d分ごと", minStep))
			default:
				parts = append(parts, fmt.Sprintf("every %d min", minStep))
			}
		} else {
			switch lang {
			case LangChinese, LangTraditionalChinese:
				parts = append(parts, "每小时第"+minute+"分")
			case LangJapanese:
				parts = append(parts, "毎時"+minute+"分")
			default:
				parts = append(parts, "minute "+minute+" of every hour")
			}
		}
	}

	// Frequency hint
	if allWild {
		switch lang {
		case LangChinese, LangTraditionalChinese:
			return "每天 " + strings.Join(parts, " ")
		case LangJapanese:
			return "毎日 " + strings.Join(parts, " ")
		case LangSpanish:
			return "Diario " + strings.Join(parts, " ")
		default:
			return "Daily at " + strings.Join(parts, " ")
		}
	}
	if dow != "*" && month == "*" && dom == "*" {
		switch lang {
		case LangChinese, LangTraditionalChinese:
			return "每" + strings.Join(parts, " ")
		case LangJapanese:
			return "毎" + strings.Join(parts, " ")
		default:
			return strings.Join(parts, " at ")
		}
	}
	if dom != "*" && month == "*" && dow == "*" {
		switch lang {
		case LangChinese, LangTraditionalChinese:
			return "每月" + strings.Join(parts, " ")
		case LangJapanese:
			return "毎月" + strings.Join(parts, " ")
		case LangSpanish:
			return "Mensual, " + strings.Join(parts, ", ")
		default:
			return "Monthly, " + strings.Join(parts, ", ")
		}
	}

	if cjk {
		return strings.Join(parts, " ")
	}
	return strings.Join(parts, ", ")
}

func padZero(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}
