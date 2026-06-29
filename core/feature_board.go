package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const featureBoardRelPath = "board/features.json"

type FeatureTask struct {
	TaskID        string    `json:"task_id"`
	Title         string    `json:"title"`
	Owner         string    `json:"owner"`
	Status        string    `json:"status"`
	RepoWorktree  string    `json:"repo_worktree"`
	Blocker       string    `json:"blocker"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Evidence      []string  `json:"evidence"`
	HandbackState string    `json:"handback_state"`
	NextAction    string    `json:"next_action"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type FeatureBoard struct {
	ActiveFeature *FeatureActiveContext `json:"active_feature,omitempty"`
	Tasks         []*FeatureTask        `json:"tasks"`
}

type FeatureActiveContext struct {
	TaskID     string                       `json:"task_id"`
	Title      string                       `json:"title"`
	SessionKey string                       `json:"session_key,omitempty"`
	StartedAt  time.Time                    `json:"started_at"`
	Seats      map[string]*FeatureSeatState `json:"seats"`
}

type FeatureSeatState struct {
	Status      string     `json:"status"`
	SessionKey  string     `json:"session_key,omitempty"`
	RefreshedAt *time.Time `json:"refreshed_at,omitempty"`
}

type FeatureBoardStore struct {
	path string
	now  func() time.Time
}

var featureBoardFileMu sync.Mutex

func NewFeatureBoardStore(dataDir string) *FeatureBoardStore {
	return &FeatureBoardStore{
		path: filepath.Join(dataDir, featureBoardRelPath),
		now:  time.Now,
	}
}

func (s *FeatureBoardStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *FeatureBoardStore) Create(title, owner, repoWorktree, nextAction, sessionKey string, seatNames []string) (*FeatureTask, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, fmt.Errorf("feature board store is not configured")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("feature title is required")
	}
	featureBoardFileMu.Lock()
	defer featureBoardFileMu.Unlock()

	board, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	task := &FeatureTask{
		TaskID:        uniqueFeatureTaskID(board, now, title),
		Title:         title,
		Owner:         strings.TrimSpace(owner),
		Status:        "planning",
		RepoWorktree:  strings.TrimSpace(repoWorktree),
		Blocker:       "",
		LastHeartbeat: now,
		Evidence:      []string{},
		HandbackState: "not_started",
		NextAction:    strings.TrimSpace(nextAction),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	board.Tasks = append(board.Tasks, task)
	board.ActiveFeature = &FeatureActiveContext{
		TaskID:     task.TaskID,
		Title:      task.Title,
		SessionKey: strings.TrimSpace(sessionKey),
		StartedAt:  now,
		Seats:      featureSeatStateMap(seatNames),
	}
	if err := s.saveLocked(board); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *FeatureBoardStore) ActiveTaskForSeat(seatName string) (*FeatureTask, bool, error) {
	if s == nil || strings.TrimSpace(s.path) == "" || strings.TrimSpace(seatName) == "" {
		return nil, false, nil
	}
	featureBoardFileMu.Lock()
	defer featureBoardFileMu.Unlock()

	board, err := s.loadLocked()
	if err != nil {
		return nil, false, err
	}
	active := board.ActiveFeature
	if active == nil || strings.TrimSpace(active.TaskID) == "" {
		return nil, false, nil
	}
	if active.Seats == nil {
		active.Seats = map[string]*FeatureSeatState{}
	}
	state := active.Seats[seatName]
	if state == nil {
		state = &FeatureSeatState{Status: "pending"}
		active.Seats[seatName] = state
	}
	if state.Status == "refreshed" {
		return nil, false, nil
	}
	state.Status = "refreshing"
	if err := s.saveLocked(board); err != nil {
		return nil, false, err
	}
	task := board.findTask(active.TaskID)
	if task == nil {
		task = &FeatureTask{
			TaskID:        active.TaskID,
			Title:         active.Title,
			Owner:         featureChefSeat,
			Status:        "planning",
			LastHeartbeat: active.StartedAt,
			Evidence:      []string{},
			HandbackState: "not_started",
			NextAction:    "Continue active feature workflow.",
			CreatedAt:     active.StartedAt,
			UpdatedAt:     active.StartedAt,
		}
	}
	return task, true, nil
}

func (s *FeatureBoardStore) MarkSeatRefreshed(taskID, seatName, sessionKey string) error {
	if s == nil || strings.TrimSpace(s.path) == "" || strings.TrimSpace(taskID) == "" || strings.TrimSpace(seatName) == "" {
		return nil
	}
	featureBoardFileMu.Lock()
	defer featureBoardFileMu.Unlock()

	board, err := s.loadLocked()
	if err != nil {
		return err
	}
	active := board.ActiveFeature
	if active == nil || active.TaskID != taskID {
		return nil
	}
	if active.Seats == nil {
		active.Seats = map[string]*FeatureSeatState{}
	}
	now := s.now().UTC()
	active.Seats[seatName] = &FeatureSeatState{
		Status:      "refreshed",
		SessionKey:  strings.TrimSpace(sessionKey),
		RefreshedAt: &now,
	}
	if task := board.findTask(taskID); task != nil {
		task.UpdatedAt = now
	}
	return s.saveLocked(board)
}

func (s *FeatureBoardStore) loadLocked() (*FeatureBoard, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &FeatureBoard{Tasks: []*FeatureTask{}}, nil
		}
		return nil, err
	}
	var board FeatureBoard
	if len(strings.TrimSpace(string(data))) == 0 {
		return &FeatureBoard{Tasks: []*FeatureTask{}}, nil
	}
	if err := json.Unmarshal(data, &board); err != nil {
		return nil, err
	}
	if board.Tasks == nil {
		board.Tasks = []*FeatureTask{}
	}
	return &board, nil
}

func (s *FeatureBoardStore) saveLocked(board *FeatureBoard) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(board, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(s.path, data, 0o644)
}

func (b *FeatureBoard) findTask(taskID string) *FeatureTask {
	if b == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	for _, task := range b.Tasks {
		if task != nil && task.TaskID == taskID {
			return task
		}
	}
	return nil
}

func featureSeatStateMap(seatNames []string) map[string]*FeatureSeatState {
	seats := make(map[string]*FeatureSeatState)
	for _, name := range seatNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		seats[name] = &FeatureSeatState{Status: "pending"}
	}
	return seats
}

func uniqueFeatureTaskID(board *FeatureBoard, now time.Time, title string) string {
	base := "feat-" + now.Format("20060102-150405")
	if slug := featureTitleSlug(title); slug != "" {
		base += "-" + slug
	}
	used := make(map[string]bool)
	if board != nil {
		for _, task := range board.Tasks {
			if task != nil {
				used[task.TaskID] = true
			}
		}
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !used[candidate] {
			return candidate
		}
	}
}

var featureSlugCleanup = regexp.MustCompile(`[^a-z0-9]+`)

func featureTitleSlug(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = featureSlugCleanup.ReplaceAllString(title, "-")
	title = strings.Trim(title, "-")
	if title == "" {
		return ""
	}
	parts := strings.Split(title, "-")
	if len(parts) > 4 {
		parts = parts[:4]
	}
	return strings.Join(parts, "-")
}
