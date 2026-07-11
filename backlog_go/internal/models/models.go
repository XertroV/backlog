package models

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Status string

type Priority string

type Complexity string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusDone       Status = "done"
	StatusRejected   Status = "rejected"
	StatusCancelled  Status = "cancelled"
)

const (
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

const (
	ComplexityLow      Complexity = "low"
	ComplexityMedium   Complexity = "medium"
	ComplexityHigh     Complexity = "high"
	ComplexityCritical Complexity = "critical"
)

// Validate enum strings.
var validStatuses = map[Status]struct{}{
	StatusPending:    {},
	StatusInProgress: {},
	StatusBlocked:    {},
	StatusDone:       {},
	StatusRejected:   {},
	StatusCancelled:  {},
}

var validPriorities = map[Priority]struct{}{
	PriorityLow:      {},
	PriorityMedium:   {},
	PriorityHigh:     {},
	PriorityCritical: {},
}

var validComplexities = map[Complexity]struct{}{
	ComplexityLow:      {},
	ComplexityMedium:   {},
	ComplexityHigh:     {},
	ComplexityCritical: {},
}

var statusTransitions = map[Status][]Status{
	StatusPending: {
		StatusInProgress,
		StatusBlocked,
		StatusCancelled,
	},
	StatusInProgress: {
		StatusDone,
		StatusBlocked,
		StatusRejected,
		StatusPending,
	},
	StatusDone: {
		StatusBlocked,
		StatusRejected,
	},
	StatusBlocked: {
		StatusPending,
		StatusCancelled,
	},
	StatusRejected: {
		StatusPending,
	},
	StatusCancelled: {},
}

func normalizeEnumValue(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	return normalized
}

func ParseStatus(raw string) (Status, error) {
	normalized := normalizeEnumValue(raw)
	if normalized == "" {
		return "", fmt.Errorf("status is required")
	}
	switch normalized {
	case "inprogress":
		return StatusInProgress, nil
	case "complete", "completed":
		return StatusDone, nil
	case string(StatusPending), string(StatusInProgress), string(StatusBlocked), string(StatusDone), string(StatusRejected), string(StatusCancelled):
		status := Status(normalized)
		return status, nil
	}
	return "", fmt.Errorf("invalid status: %s", raw)
}

func ParsePriority(raw string) (Priority, error) {
	normalized := normalizeEnumValue(raw)
	if normalized == "" {
		return "", fmt.Errorf("priority is required")
	}
	priority := Priority(normalized)
	if _, ok := validPriorities[priority]; ok {
		return priority, nil
	}
	return "", fmt.Errorf("invalid priority: %s", raw)
}

func ParseComplexity(raw string) (Complexity, error) {
	normalized := normalizeEnumValue(raw)
	if normalized == "" {
		return "", fmt.Errorf("complexity is required")
	}
	complexity := Complexity(normalized)
	if _, ok := validComplexities[complexity]; ok {
		return complexity, nil
	}
	return "", fmt.Errorf("invalid complexity: %s", raw)
}

func IsValidStatus(value string) bool {
	_, err := ParseStatus(value)
	return err == nil
}

func IsValidPriority(value string) bool {
	_, err := ParsePriority(value)
	return err == nil
}

func IsValidComplexity(value string) bool {
	_, err := ParseComplexity(value)
	return err == nil
}

// ValidateStatusTransition checks whether a workflow status change is allowed.
func ValidateStatusTransition(current Status, next Status) error {
	for _, allowed := range statusTransitions[current] {
		if allowed == next {
			return nil
		}
	}
	valid := statusTransitions[current]
	validNext := make([]string, 0, len(valid))
	for _, s := range valid {
		validNext = append(validNext, string(s))
	}
	return fmt.Errorf("cannot transition from '%s' to '%s'. valid transitions: %s", current, next, strings.Join(validNext, ", "))
}

// TaskPath models hierarchical IDs like P1, P1.M1, P1.M1.E1, P1.M1.E1.T001.
type TaskPath struct {
	Phase     string
	Milestone string
	Epic      string
	Task      string
}

func ParseTaskPath(pathStr string) (TaskPath, error) {
	parts := strings.Split(pathStr, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return TaskPath{}, fmt.Errorf("invalid path format: %s", pathStr)
	}
	for _, p := range parts {
		if p == "" {
			return TaskPath{}, fmt.Errorf("invalid path format: %s", pathStr)
		}
	}
	if !phaseIDRegexp.MatchString(parts[0]) {
		return TaskPath{}, fmt.Errorf("invalid phase ID format: %s", pathStr)
	}
	if len(parts) > 1 && !milestoneIDRegexp.MatchString(parts[1]) {
		return TaskPath{}, fmt.Errorf("invalid milestone ID format: %s", pathStr)
	}
	if len(parts) > 2 && !epicIDRegexp.MatchString(parts[2]) {
		return TaskPath{}, fmt.Errorf("invalid epic ID format: %s", pathStr)
	}
	if len(parts) > 3 && !taskIDRegexp.MatchString(parts[3]) {
		return TaskPath{}, fmt.Errorf("invalid task ID format: %s", pathStr)
	}

	path := TaskPath{Phase: parts[0]}
	if len(parts) > 1 {
		path.Milestone = parts[1]
	}
	if len(parts) > 2 {
		path.Epic = parts[2]
	}
	if len(parts) > 3 {
		path.Task = parts[3]
	}

	return path, nil
}

func ForPhase(phaseID string) TaskPath {
	return TaskPath{Phase: phaseID}
}

func ForMilestone(phaseID, milestoneID string) TaskPath {
	return TaskPath{Phase: phaseID, Milestone: milestoneID}
}

func ForEpic(phaseID, milestoneID, epicID string) TaskPath {
	return TaskPath{Phase: phaseID, Milestone: milestoneID, Epic: epicID}
}

func ForTask(phaseID, milestoneID, epicID, taskID string) TaskPath {
	return TaskPath{Phase: phaseID, Milestone: milestoneID, Epic: epicID, Task: taskID}
}

func (p TaskPath) FullID() string {
	parts := []string{p.Phase}
	if p.Milestone != "" {
		parts = append(parts, p.Milestone)
	}
	if p.Epic != "" {
		parts = append(parts, p.Epic)
	}
	if p.Task != "" {
		parts = append(parts, p.Task)
	}
	return strings.Join(parts, ".")
}

func (p TaskPath) PhaseID() string {
	return p.Phase
}

func (p TaskPath) MilestoneID() string {
	if p.Milestone == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s", p.Phase, p.Milestone)
}

func (p TaskPath) EpicID() string {
	if p.Milestone == "" || p.Epic == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s", p.Phase, p.Milestone, p.Epic)
}

func (p TaskPath) TaskID() string {
	if p.Milestone == "" || p.Epic == "" || p.Task == "" {
		return ""
	}
	return p.FullID()
}

func (p TaskPath) Depth() int {
	if p.Task != "" {
		return 4
	}
	if p.Epic != "" {
		return 3
	}
	if p.Milestone != "" {
		return 2
	}
	return 1
}

func (p TaskPath) IsPhase() bool {
	return p.Depth() == 1
}

func (p TaskPath) IsMilestone() bool {
	return p.Depth() == 2
}

func (p TaskPath) IsEpic() bool {
	return p.Depth() == 3
}

func (p TaskPath) IsTask() bool {
	return p.Depth() == 4
}

func (p TaskPath) WithMilestone(milestoneID string) TaskPath {
	return TaskPath{Phase: p.Phase, Milestone: milestoneID}
}

func (p TaskPath) WithEpic(epicID string) (TaskPath, error) {
	if p.Milestone == "" {
		return TaskPath{}, fmt.Errorf("cannot add epic without milestone")
	}
	return TaskPath{Phase: p.Phase, Milestone: p.Milestone, Epic: epicID}, nil
}

func (p TaskPath) WithTask(taskID string) (TaskPath, error) {
	if p.Milestone == "" || p.Epic == "" {
		return TaskPath{}, fmt.Errorf("cannot add task without milestone and epic")
	}
	return TaskPath{Phase: p.Phase, Milestone: p.Milestone, Epic: p.Epic, Task: taskID}, nil
}

func (p TaskPath) Parent() *TaskPath {
	if p.Task != "" {
		parent := TaskPath{Phase: p.Phase, Milestone: p.Milestone, Epic: p.Epic}
		return &parent
	}
	if p.Epic != "" {
		parent := TaskPath{Phase: p.Phase, Milestone: p.Milestone}
		return &parent
	}
	if p.Milestone != "" {
		parent := TaskPath{Phase: p.Phase}
		return &parent
	}
	return nil
}

func (p TaskPath) String() string {
	return p.FullID()
}

type PathQuery struct {
	Raw      string
	Segments []string
}

func ParsePathQuery(query string) (PathQuery, error) {
	if strings.TrimSpace(query) == "" {
		return PathQuery{}, fmt.Errorf("path query is required")
	}
	raw := strings.TrimSpace(query)
	segments := strings.Split(raw, ".")
	if len(segments) < 1 || len(segments) > 4 {
		return PathQuery{}, fmt.Errorf("invalid path query format: %s (must be 1-4 dot-separated segments)", query)
	}
	for i, segment := range segments {
		if segment == "" {
			return PathQuery{}, fmt.Errorf("invalid path query format: %s", query)
		}
		if strings.Contains(segment, "*") {
			if segment == "*" {
				continue
			}
			if segment == "**" {
				if i != len(segments)-1 {
					return PathQuery{}, fmt.Errorf("invalid wildcard in path query segment '%s': %s", segment, query)
				}
				continue
			}
			if strings.Count(segment, "*") != 1 || !strings.HasSuffix(segment, "*") {
				return PathQuery{}, fmt.Errorf("invalid wildcard in path query segment '%s': %s", segment, query)
			}
		}
	}
	return PathQuery{Raw: raw, Segments: segments}, nil
}

func (p PathQuery) matchesSegment(pattern, segment string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(segment, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == segment
}

func (p PathQuery) Matches(candidate string) bool {
	candidateParts := strings.Split(candidate, ".")
	if len(candidateParts) < len(p.Segments) {
		return false
	}
	for i, pattern := range p.Segments {
		if pattern == "**" {
			return true
		}
		if !p.matchesSegment(pattern, candidateParts[i]) {
			return false
		}
	}
	return true
}

type Task struct {
	ID              string
	Title           string
	File            string
	Status          Status
	EstimateHours   float64
	Complexity      Complexity
	Priority        Priority
	DependsOn       []string
	ClaimedBy       string
	ClaimedAt       *time.Time
	CreatedAt       *time.Time
	UpdatedAt       *time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	DurationMinutes *float64
	Tags            []string
	Reason          string

	EpicID      string
	MilestoneID string
	PhaseID     string
}

func (t Task) IsAvailable() bool {
	return t.Status == StatusPending && t.ClaimedBy == ""
}

func (t Task) TaskPath() (TaskPath, error) {
	if t.ID == "" {
		return TaskPath{}, fmt.Errorf("task has empty id")
	}
	return ParseTaskPath(t.ID)
}

type Epic struct {
	ID            string
	Name          string
	Path          string
	Status        Status
	EstimateHours float64
	Complexity    Complexity
	DependsOn     []string
	Tasks         []Task
	Description   string
	Locked        bool
	MilestoneID   string
	PhaseID       string
}

type Milestone struct {
	ID            string
	Name          string
	Path          string
	Status        Status
	EstimateHours float64
	Complexity    Complexity
	DependsOn     []string
	Epics         []Epic
	Description   string
	Locked        bool
	PhaseID       string
}

type Phase struct {
	ID            string
	Name          string
	Path          string
	Status        Status
	Weeks         int
	EstimateHours float64
	Priority      Priority
	DependsOn     []string
	Milestones    []Milestone
	Description   string
	Locked        bool
}

type TaskTree struct {
	Project       string
	Description   string
	TimelineWeeks int
	CriticalPath  []string
	NextAvailable string
	Phases        []Phase
	Bugs          []Task
	Ideas         []Task
}

func (t TaskTree) IDsMatch(candidate, target string) bool {
	if candidate == "" || target == "" {
		return false
	}
	if candidate == target {
		return true
	}
	return strings.HasSuffix(candidate, "."+target) || strings.HasSuffix(target, "."+candidate)
}

func (t TaskTree) FindTask(id string) *Task {
	for i, phase := range t.Phases {
		for j, milestone := range phase.Milestones {
			for k, epic := range milestone.Epics {
				for l := range epic.Tasks {
					task := &t.Phases[i].Milestones[j].Epics[k].Tasks[l]
					if task.ID == id {
						return task
					}
				}
			}
		}
	}
	for i := range t.Bugs {
		task := &t.Bugs[i]
		if task.ID == id {
			return task
		}
	}
	for i := range t.Ideas {
		task := &t.Ideas[i]
		if task.ID == id {
			return task
		}
	}
	return nil
}

func (t TaskTree) FindEpic(id string) *Epic {
	for i, phase := range t.Phases {
		for j, milestone := range phase.Milestones {
			for k := range milestone.Epics {
				epic := &t.Phases[i].Milestones[j].Epics[k]
				if t.IDsMatch(epic.ID, id) || epic.ID == id {
					return epic
				}
			}
		}
	}
	return nil
}

func (t TaskTree) FindMilestone(id string) *Milestone {
	for i, phase := range t.Phases {
		for j := range phase.Milestones {
			ms := &t.Phases[i].Milestones[j]
			if t.IDsMatch(ms.ID, id) || ms.ID == id {
				return ms
			}
		}
	}
	return nil
}

func (t TaskTree) FindPhase(id string) *Phase {
	for i := range t.Phases {
		phase := &t.Phases[i]
		if t.IDsMatch(phase.ID, id) || phase.ID == id {
			return phase
		}
	}
	return nil
}

func (t TaskTree) AllTasks() []Task {
	var tasks []Task
	for _, phase := range t.Phases {
		for _, milestone := range phase.Milestones {
			for _, epic := range milestone.Epics {
				tasks = append(tasks, epic.Tasks...)
			}
		}
	}
	tasks = append(tasks, t.Bugs...)
	tasks = append(tasks, t.Ideas...)
	return tasks
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(text string, maxLength int) string {
	slug := strings.ToLower(text)
	slug = nonAlphaNum.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > maxLength {
		slug = slug[:maxLength]
		slug = strings.TrimSuffix(slug, "-")
	}
	return slug
}

var (
	phaseIDRegexp     = regexp.MustCompile(`^P(\d+)$`)
	milestoneIDRegexp = regexp.MustCompile(`^M(\d+)$`)
	epicIDRegexp      = regexp.MustCompile(`^E(\d+)$`)
	taskIDRegexp      = regexp.MustCompile(`^T(\d+)$`)
)

const (
	TaskIDWidth        = 3
	DirectoryNameWidth = 2
)

// NextPhaseID returns the next phase identifier from existing phase IDs.
func NextPhaseID(existingIDs []string) string {
	return fmt.Sprintf("P%d", nextNumericID(existingIDs, phaseIDRegexp))
}

// NextMilestoneID returns the next milestone identifier from existing milestone IDs.
func NextMilestoneID(existingIDs []string) string {
	return fmt.Sprintf("M%d", nextNumericID(existingIDs, milestoneIDRegexp))
}

// NextEpicID returns the next epic identifier from existing epic IDs.
func NextEpicID(existingIDs []string) string {
	return fmt.Sprintf("E%d", nextNumericID(existingIDs, epicIDRegexp))
}

// NextTaskID returns the next task identifier from existing task IDs.
func NextTaskID(existingIDs []string) string {
	return fmt.Sprintf("T%0*d", TaskIDWidth, nextNumericID(existingIDs, taskIDRegexp))
}

// NumberedDirectoryName returns folder-style IDs for filesystem numbering, e.g. 01.
func NumberedDirectoryName(index int) string {
	if index < 1 {
		index = 1
	}
	return fmt.Sprintf("%0*d", DirectoryNameWidth, index)
}

// DirectoryName returns directory name with optional slug content.
func DirectoryName(index int, slug string) string {
	normalized := Slugify(slug, 30)
	if normalized == "" {
		return NumberedDirectoryName(index)
	}
	return NumberedDirectoryName(index) + "-" + normalized
}

// SafePathPrefix reports whether a candidate path segment is migration-safe.
func SafePathPrefix(value string) bool {
	return value == filepath.ToSlash(filepath.Clean(value)) && !strings.Contains(value, "..") && value != "" && value != "."
}

func nextNumericID(existingIDs []string, pattern *regexp.Regexp) int {
	max := 0
	for _, raw := range existingIDs {
		match := pattern.FindStringSubmatch(raw)
		if len(match) != 2 {
			continue
		}
		value, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if value > max {
			max = value
		}
	}
	return max + 1
}
