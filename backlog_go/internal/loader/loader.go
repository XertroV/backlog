package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/XertroV/tasks/backlog_go/internal/config"
	"github.com/XertroV/tasks/backlog_go/internal/models"
	"gopkg.in/yaml.v3"
)

const (
	loadModeFull     = "full"
	loadModeMetadata = "metadata"
	loadModeIndex    = "index"
)

var (
	rePhaseID     = regexp.MustCompile(`^P[0-9]+$`)
	reMilestoneID = regexp.MustCompile(`^M[0-9]+$`)
	reEpicID      = regexp.MustCompile(`^E[0-9]+$`)
)

type Loader struct {
	tasksDir string
}

type Benchmark struct {
	OverallMs            float64            `json:"overall_ms"`
	IndexParseMs         float64            `json:"index_parse_ms"`
	TaskFrontmatterParse float64            `json:"task_frontmatter_parse_ms"`
	TaskBodyParse        float64            `json:"task_body_parse_ms"`
	Files                map[string]int     `json:"files"`
	FilesByTypeMs        map[string]float64 `json:"files_by_type_ms"`
	Counts               map[string]int     `json:"counts"`
	MissingTaskFiles     int                `json:"missing_task_files"`
	PhaseTimings         []map[string]any   `json:"phase_timings"`
	MilestoneTimings     []map[string]any   `json:"milestone_timings"`
	EpicTimings          []map[string]any   `json:"epic_timings"`
	TaskTimings          []map[string]any   `json:"task_timings"`
}

func New(tasksDir ...string) *Loader {
	dir := ""
	if len(tasksDir) > 0 && tasksDir[0] != "" {
		dir = tasksDir[0]
	} else {
		dir = detectTasksDir()
	}
	return &Loader{tasksDir: dir}
}

func (l *Loader) LoadTree() (models.TaskTree, error) {
	return l.Load(loadModeFull, true, true)
}

func (l *Loader) Load(mode string, includeBugs bool, includeIdeas bool) (models.TaskTree, error) {
	return l.load(mode, true, includeBugs, includeIdeas)
}

func (l *Loader) LoadScope(scope string, mode string, parseTaskBody bool, includeBugs bool, includeIdeas bool) (models.TaskTree, error) {
	path, err := models.ParseTaskPath(scope)
	if err != nil {
		return models.TaskTree{}, fmt.Errorf("invalid scope: %w", err)
	}
	tree, err := l.Load(mode, includeBugs, includeIdeas)
	if err != nil {
		return models.TaskTree{}, err
	}
	return l.filterByPath(tree, path), nil
}

func (l *Loader) LoadWithBenchmark(mode string, parseTaskBody bool, includeBugs bool, includeIdeas bool) (models.TaskTree, Benchmark, error) {
	bench := newBenchmark()
	start := time.Now()
	if mode == "" {
		mode = loadModeFull
	}
	parseTaskBody = parseTaskBody && mode == loadModeFull
	tree, err := l.loadWithBenchmark(mode, parseTaskBody, includeBugs, includeIdeas, &bench)
	bench.OverallMs = time.Since(start).Seconds() * 1000
	if err != nil {
		return models.TaskTree{}, bench, err
	}
	return tree, bench, nil
}

func (l *Loader) LoadTreeWithBenchmark(parseTaskBody bool, includeBugs bool, includeIdeas bool) (models.TaskTree, Benchmark, error) {
	return l.LoadWithBenchmark(loadModeFull, parseTaskBody, includeBugs, includeIdeas)
}

func (l *Loader) load(mode string, parseTaskBody bool, includeBugs bool, includeIdeas bool) (models.TaskTree, error) {
	return l.loadWithBenchmark(mode, parseTaskBody, includeBugs, includeIdeas, nil)
}

func (l *Loader) loadWithBenchmark(mode string, parseTaskBody bool, includeBugs bool, includeIdeas bool, bench *Benchmark) (models.TaskTree, error) {
	if l.tasksDir == "" {
		return models.TaskTree{}, fmt.Errorf("no data directory found")
	}

	normalizedMode := normalizeMode(mode)
	if normalizedMode == "" {
		normalizedMode = loadModeFull
	}

	rootPath := filepath.Join(l.tasksDir, "index.yaml")
	root, err := l.readYaml(rootPath, "root_index", false, bench)
	if err != nil {
		return models.TaskTree{}, err
	}

	tree := models.TaskTree{
		Project:       asString(root["project"]),
		Description:   asString(root["description"]),
		TimelineWeeks: asInt(root["timeline_weeks"]),
		CriticalPath:  asStringSlice(root["critical_path"]),
		NextAvailable: asString(root["next_available"]),
		Phases:        []models.Phase{},
		Bugs:          []models.Task{},
		Ideas:         []models.Task{},
	}

	for _, phaseRaw := range asSlice(root["phases"]) {
		phaseData, ok := phaseRaw.(map[string]interface{})
		if !ok {
			return models.TaskTree{}, fmt.Errorf("invalid phase entry in index.yaml")
		}
		phase, err := l.loadPhase(phaseData, normalizedMode, parseTaskBody, bench)
		if err != nil {
			phaseID := asString(phaseData["id"])
			path := asString(phaseData["path"])
			return models.TaskTree{}, fmt.Errorf("error loading phase %s (path: %s): %w", phaseID, path, err)
		}
		tree.Phases = append(tree.Phases, phase)
	}

	if includeBugs {
		bugs, err := l.loadAux("bugs", "bug_index", normalizedMode, parseTaskBody, bench)
		if err != nil {
			return models.TaskTree{}, err
		}
		tree.Bugs = bugs
	}
	if includeIdeas {
		ideas, err := l.loadAux("ideas", "idea_index", normalizedMode, parseTaskBody, bench)
		if err != nil {
			return models.TaskTree{}, err
		}
		tree.Ideas = ideas
	}

	return tree, nil
}

func (l *Loader) loadPhase(data map[string]interface{}, mode string, parseTaskBody bool, bench *Benchmark) (models.Phase, error) {
	start := time.Now()
	phaseID := asString(data["id"])
	phasePath := asString(data["path"])
	if phaseID == "" {
		return models.Phase{}, fmt.Errorf("phase is missing id")
	}
	if !rePhaseID.MatchString(phaseID) {
		return models.Phase{}, fmt.Errorf("invalid phase id: %s", phaseID)
	}
	if phasePath == "" {
		return models.Phase{}, fmt.Errorf("phase %s is missing path", phaseID)
	}
	phase := models.Phase{
		ID:            phaseID,
		Name:          asString(data["name"]),
		Path:          phasePath,
		Status:        coerceStatus(data["status"]),
		Weeks:         asInt(data["weeks"]),
		EstimateHours: asFloat(data["estimate_hours"], data["estimated_hours"]),
		Priority:      coercePriority(data["priority"]),
		DependsOn:     asStringSlice(data["depends_on"]),
		Description:   asString(data["description"]),
		Milestones:    []models.Milestone{},
		Locked:        asBool(data["locked"]),
	}
	if bench != nil {
		bench.Counts["phases"]++
	}

	indexPath := filepath.Join(l.tasksDir, phase.Path, "index.yaml")
	index, err := l.readYaml(indexPath, "phase_index", mode == loadModeIndex, bench)
	if err != nil {
		if os.IsNotExist(err) {
			recordTiming(bench, "phase_timings", time.Since(start).Milliseconds(), phase.ID, phase.Path)
			return phase, nil
		}
		recordTiming(bench, "phase_timings", time.Since(start).Milliseconds(), phase.ID, phase.Path)
		return phase, nil
	}
	if index == nil {
		recordTiming(bench, "phase_timings", time.Since(start).Milliseconds(), phase.ID, phase.Path)
		return phase, nil
	}
	if asBool(index["locked"]) && data["locked"] == nil {
		phase.Locked = asBool(index["locked"])
	}

	for _, milestoneRaw := range asSlice(index["milestones"]) {
		milestoneData, ok := milestoneRaw.(map[string]interface{})
		if !ok {
			continue
		}
		milestone, err := l.loadMilestone(phase.ID, phase.Path, milestoneData, mode, parseTaskBody, bench)
		if err != nil {
			msID := asString(milestoneData["id"])
			msPath := asString(milestoneData["path"])
			return models.Phase{}, fmt.Errorf("error loading milestone %s.%s (path: %s): %w", phase.ID, msID, msPath, err)
		}
		phase.Milestones = append(phase.Milestones, milestone)
	}

	recordTiming(bench, "phase_timings", time.Since(start).Milliseconds(), phase.ID, phase.Path)
	return phase, nil
}

func (l *Loader) loadMilestone(phaseID, phasePath string, data map[string]interface{}, mode string, parseTaskBody bool, bench *Benchmark) (models.Milestone, error) {
	start := time.Now()
	msShort := asString(data["id"])
	if msShort == "" {
		return models.Milestone{}, fmt.Errorf("milestone is missing id in phase %s", phaseID)
	}
	if !reMilestoneID.MatchString(msShort) {
		return models.Milestone{}, fmt.Errorf("invalid milestone id: %s", msShort)
	}
	msPath := models.ForMilestone(phaseID, msShort)
	msPathValue := asString(data["path"])
	if msPathValue == "" {
		return models.Milestone{}, fmt.Errorf("milestone %s is missing path", msPath.FullID())
	}
	milestone := models.Milestone{
		ID:            msPath.FullID(),
		Name:          asString(data["name"]),
		Path:          msPathValue,
		Status:        coerceStatus(data["status"]),
		EstimateHours: asFloat(data["estimate_hours"], data["estimated_hours"]),
		Complexity:    coerceComplexity(data["complexity"]),
		DependsOn:     asStringSlice(data["depends_on"]),
		Description:   asString(data["description"]),
		Epics:         []models.Epic{},
		Locked:        asBool(data["locked"]),
		PhaseID:       phaseID,
	}
	if bench != nil {
		bench.Counts["milestones"]++
	}
	if milestone.Path == "" {
		recordTiming(bench, "milestone_timings", time.Since(start).Milliseconds(), milestone.ID, milestone.Path)
		return milestone, nil
	}

	indexPath := filepath.Join(l.tasksDir, phasePath, milestone.Path, "index.yaml")
	index, err := l.readYaml(indexPath, "milestone_index", mode == loadModeIndex, bench)
	if err != nil {
		if os.IsNotExist(err) {
			recordTiming(bench, "milestone_timings", time.Since(start).Milliseconds(), milestone.ID, milestone.Path)
			return milestone, nil
		}
		recordTiming(bench, "milestone_timings", time.Since(start).Milliseconds(), milestone.ID, milestone.Path)
		return milestone, nil
	}
	if index == nil {
		recordTiming(bench, "milestone_timings", time.Since(start).Milliseconds(), milestone.ID, milestone.Path)
		return milestone, nil
	}

	epicRoot := filepath.Join(l.tasksDir, phasePath, milestone.Path)
	for _, epicRaw := range asSlice(index["epics"]) {
		epicData, ok := epicRaw.(map[string]interface{})
		if !ok {
			continue
		}
		epic, err := l.loadEpic(msPath, epicData, epicRoot, mode, parseTaskBody, bench)
		if err != nil {
			epicID := asString(epicData["id"])
			epicPath := asString(epicData["path"])
			return models.Milestone{}, fmt.Errorf("error loading epic %s.%s (path: %s): %w", msPath.FullID(), epicID, epicPath, err)
		}
		milestone.Epics = append(milestone.Epics, epic)
	}
	if locked, ok := index["locked"].(bool); ok {
		milestone.Locked = locked
	}

	recordTiming(bench, "milestone_timings", time.Since(start).Milliseconds(), milestone.ID, milestone.Path)
	return milestone, nil
}

func (l *Loader) loadEpic(milestoneID models.TaskPath, data map[string]interface{}, epicRoot string, mode string, parseTaskBody bool, bench *Benchmark) (models.Epic, error) {
	start := time.Now()
	epicShort := asString(data["id"])
	if epicShort == "" {
		return models.Epic{}, fmt.Errorf("epic is missing id in milestone %s", milestoneID.FullID())
	}
	if !reEpicID.MatchString(epicShort) {
		return models.Epic{}, fmt.Errorf("invalid epic id: %s", epicShort)
	}
	epPath, err := milestoneID.WithEpic(epicShort)
	if err != nil {
		return models.Epic{}, err
	}
	epicPathValue := asString(data["path"])
	if epicPathValue == "" {
		return models.Epic{}, fmt.Errorf("epic %s is missing path", epPath.FullID())
	}
	epic := models.Epic{
		ID:            epPath.FullID(),
		Name:          asString(data["name"]),
		Path:          epicPathValue,
		Status:        coerceStatus(data["status"]),
		EstimateHours: asFloat(data["estimate_hours"], data["estimated_hours"]),
		Complexity:    coerceComplexity(data["complexity"]),
		DependsOn:     asStringSlice(data["depends_on"]),
		Description:   asString(data["description"]),
		Tasks:         []models.Task{},
		MilestoneID:   milestoneID.FullID(),
		PhaseID:       milestoneID.PhaseID(),
		Locked:        asBool(data["locked"]),
	}
	if bench != nil {
		bench.Counts["epics"]++
	}

	if epic.Path == "" {
		recordTiming(bench, "epic_timings", time.Since(start).Milliseconds(), epic.ID, epic.Path)
		return epic, nil
	}

	indexPath := filepath.Join(epicRoot, epic.Path, "index.yaml")
	index, err := l.readYaml(indexPath, "epic_index", mode == loadModeIndex, bench)
	if err != nil {
		if os.IsNotExist(err) {
			recordTiming(bench, "epic_timings", time.Since(start).Milliseconds(), epic.ID, epic.Path)
			return epic, nil
		}
		recordTiming(bench, "epic_timings", time.Since(start).Milliseconds(), epic.ID, epic.Path)
		return epic, nil
	}
	if index == nil {
		recordTiming(bench, "epic_timings", time.Since(start).Milliseconds(), epic.ID, epic.Path)
		return epic, nil
	}
	if locked, ok := index["locked"].(bool); ok {
		epic.Locked = locked
	}

	taskRoot := filepath.Join(epicRoot, epic.Path)
	for _, taskRaw := range asSlice(index["tasks"]) {
		task, err := l.loadTask(taskRaw, taskRoot, epPath, mode, parseTaskBody, bench)
		if err != nil {
			return models.Epic{}, err
		}
		epic.Tasks = append(epic.Tasks, task)
	}

	recordTiming(bench, "epic_timings", time.Since(start).Milliseconds(), epic.ID, epic.Path)
	return epic, nil
}

func (l *Loader) loadTask(raw interface{}, taskRoot string, epPath models.TaskPath, mode string, parseTaskBody bool, bench *Benchmark) (models.Task, error) {
	start := time.Now()

	entry := map[string]interface{}{}
	filename := ""
	switch value := raw.(type) {
	case string:
		filename = value
		entry["file"] = value
	case map[string]interface{}:
		entry = value
		if f, ok := value["file"].(string); ok {
			filename = f
		} else if f, ok := value["path"].(string); ok {
			filename = f
		}
	default:
		return models.Task{}, fmt.Errorf("invalid task entry")
	}
	if filename == "" {
		return models.Task{}, fmt.Errorf("task entry missing file")
	}

	taskFile := filepath.Join(taskRoot, filename)
	shortID := asString(entry["id"])
	if shortID == "" {
		shortID = strings.TrimSuffix(filename, filepath.Ext(filename))
		if idx := strings.Index(shortID, "-"); idx > -1 {
			shortID = shortID[:idx]
		}
	}
	if shortID != "" {
		normalizedID, err := normalizeTaskID(shortID, epPath)
		if err != nil {
			return models.Task{}, err
		}
		shortID = normalizedID
	}

	task := models.Task{
		ID:            shortID,
		Title:         asString(entry["title"]),
		File:          filepath.ToSlash(strings.TrimPrefix(taskFile, l.tasksDir+string(filepath.Separator))),
		Status:        coerceStatus(entry["status"]),
		EstimateHours: asFloat(entry["estimate_hours"], entry["estimated_hours"]),
		Complexity:    coerceComplexity(entry["complexity"]),
		Priority:      coercePriority(entry["priority"]),
		DependsOn:     asStringSlice(entry["depends_on"]),
		Tags:          asStringSlice(entry["tags"]),
		Reason:        asString(entry["reason"]),
		CreatedAt:     parseRFC3339(entry["created_at"]),
		UpdatedAt:     parseRFC3339(entry["updated_at"]),
		EpicID:        epPath.FullID(),
		MilestoneID:   epPath.MilestoneID(),
		PhaseID:       epPath.PhaseID(),
	}
	if task.Status == "" {
		task.Status = models.StatusPending
	}
	if task.Priority == "" {
		task.Priority = models.PriorityMedium
	}
	if task.Complexity == "" {
		task.Complexity = models.ComplexityMedium
	}
	if task.DependsOn == nil {
		task.DependsOn = []string{}
	}

	front := map[string]interface{}{}
	if mode != loadModeIndex {
		fm, _, parseErr := l.parseTodoFile(taskFile, parseTaskBody, mode == loadModeFull, bench)
		if parseErr != nil {
			if !os.IsNotExist(parseErr) {
				return models.Task{}, parseErr
			}
			if bench != nil {
				bench.MissingTaskFiles++
			}
		} else {
			front = fm
		}
	}

	// Index-derived ID is the addressable identity. Prefer it over frontmatter
	// `id` so show/claim can still address the task when frontmatter disagrees
	// (id_path_mismatch warning is emitted when validating the file).
	if task.ID == "" {
		if fmID := asString(front["id"]); fmID != "" {
			normalizedID, err := normalizeTaskID(fmID, epPath)
			if err != nil {
				return models.Task{}, err
			}
			task.ID = normalizedID
		}
	}
	if title, has := front["title"]; has {
		if parsedTitle := asString(title); parsedTitle != "" {
			task.Title = parsedTitle
		}
	}
	if rawStatus, has := front["status"]; has {
		if status := coerceStatus(rawStatus); status != "" {
			task.Status = status
		}
	}
	if est, ok := asFloatFromMap(front, "estimate_hours", "estimated_hours"); ok {
		task.EstimateHours = est
	}
	if rawComplexity, has := front["complexity"]; has {
		if comp := coerceComplexity(rawComplexity); comp != "" {
			task.Complexity = comp
		}
	}
	if rawPriority, has := front["priority"]; has {
		if prio := coercePriority(rawPriority); prio != "" {
			task.Priority = prio
		}
	}
	if deps, has := front["depends_on"]; has {
		task.DependsOn = asStringSlice(deps)
	}
	task.DependsOn = expandDependsOn(task.DependsOn, epPath)
	if task.DependsOn == nil {
		task.DependsOn = []string{}
	}
	if claimedBy, ok := front["claimed_by"].(string); ok {
		task.ClaimedBy = claimedBy
	}
	if claimedAt, ok := front["claimed_at"]; ok {
		task.ClaimedAt = parseRFC3339(claimedAt)
	}
	if startedAt, ok := front["started_at"]; ok {
		task.StartedAt = parseRFC3339(startedAt)
	}
	if completedAt, ok := front["completed_at"]; ok {
		task.CompletedAt = parseRFC3339(completedAt)
	}
	if createdAt, ok := front["created_at"]; ok {
		task.CreatedAt = parseRFC3339(createdAt)
	}
	if updatedAt, ok := front["updated_at"]; ok {
		task.UpdatedAt = parseRFC3339(updatedAt)
	}
	if duration, ok := front["duration_minutes"].(float64); ok {
		task.DurationMinutes = &duration
	}
	if mode == loadModeIndex {
		if tags := asStringSlice(front["tags"]); len(tags) > 0 {
			task.Tags = tags
		}
	}

	if task.Title == "" {
		task.Title = asString(front["title"])
	}
	if task.Priority == "" {
		task.Priority = models.PriorityMedium
	}
	if task.Complexity == "" {
		task.Complexity = models.ComplexityMedium
	}

	if bench != nil {
		bench.Counts["tasks"]++
		recordTiming(bench, "task_timings", time.Since(start).Milliseconds(), task.ID, task.File)
	}
	return task, nil
}

func (l *Loader) loadAux(section string, fileType string, mode string, parseTaskBody bool, bench *Benchmark) ([]models.Task, error) {
	idxPath := filepath.Join(l.tasksDir, section, "index.yaml")
	index, err := l.readYaml(idxPath, fileType, mode == loadModeIndex, bench)
	if err != nil {
		if os.IsNotExist(err) {
			return []models.Task{}, nil
		}
		return nil, err
	}
	if index == nil {
		return []models.Task{}, nil
	}
	entries := asSlice(index[section])
	out := make([]models.Task, 0, len(entries))
	for _, raw := range entries {
		entry := map[string]interface{}{}
		filename := ""
		switch item := raw.(type) {
		case string:
			filename = item
			entry["file"] = item
			entry["id"] = strings.TrimSuffix(item, filepath.Ext(item))
		case map[string]interface{}:
			entry = item
			if f, ok := item["file"].(string); ok {
				filename = f
			} else if f, ok := item["path"].(string); ok {
				filename = f
			}
		default:
			continue
		}
		if filename == "" {
			continue
		}
		task, err := l.loadTask(entry, filepath.Join(l.tasksDir, section), models.TaskPath{}, mode, parseTaskBody, bench)
		if err != nil {
			return nil, err
		}
		task.File = filepath.ToSlash(filepath.Join(section, filename))
		task.Title = fallbackAuxTitle(task.Title, asString(entry["title"]), filename, task.ID)
		out = append(out, task)
	}
	return out, nil
}

func fallbackAuxTitle(taskTitle string, entryTitle string, filename string, taskID string) string {
	if title := strings.TrimSpace(taskTitle); title != "" {
		return title
	}
	if title := strings.TrimSpace(entryTitle); title != "" {
		return title
	}

	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	prefixes := []string{taskID, strings.ToUpper(taskID), strings.ToLower(taskID)}
	for _, prefix := range prefixes {
		if strings.TrimSpace(prefix) == "" {
			continue
		}
		base = strings.TrimPrefix(base, prefix)
		base = strings.TrimPrefix(base, "-")
		base = strings.TrimPrefix(base, "_")
	}
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.ReplaceAll(base, "_", " ")
	base = strings.Join(strings.Fields(base), " ")
	if strings.TrimSpace(base) != "" {
		return base
	}
	if strings.TrimSpace(taskID) != "" {
		return taskID
	}
	return "untitled"
}

func (l *Loader) parseTodoFile(path string, includeBody bool, parseFrontmatter bool, bench *Benchmark) (map[string]interface{}, string, error) {
	start := time.Now()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	frontmatter := map[string]interface{}{}
	body := ""

	if strings.HasPrefix(content, "---") {
		parts := strings.SplitN(content, "---\n", 3)
		if len(parts) >= 3 {
			frontmatterRaw := strings.TrimSpace(parts[1])
			if frontmatterRaw != "" {
				if parseErr := yaml.Unmarshal([]byte(frontmatterRaw), &frontmatter); parseErr != nil {
					elapsed := time.Since(start).Seconds() * 1000
					if parseFrontmatter {
						if bench != nil {
							bench.TaskFrontmatterParse += elapsed
						}
					}
					if bench != nil {
						recordFile(bench, fileTypeFromPath(path), elapsed)
					}
					return frontmatter, "", fmt.Errorf("invalid yaml in %s: %w", path, parseErr)
				}
			}
			if includeBody {
				body = parts[2]
			}
			if parseFrontmatter {
				if bench != nil {
					bench.TaskFrontmatterParse += time.Since(start).Seconds() * 1000
				}
			}
		}
	}

	if bench != nil {
		recordFile(bench, fileTypeFromPath(path), time.Since(start).Seconds()*1000)
		if includeBody {
			bench.TaskBodyParse += time.Since(start).Seconds() * 1000
		}
	}
	return frontmatter, body, nil
}

func (l *Loader) parseTodoFrontmatter(path string, parseTaskBody bool, bench *Benchmark) (map[string]interface{}, error) {
	front, _, err := l.parseTodoFile(path, false, true, bench)
	return front, err
}

func (l *Loader) readYaml(path string, fileType string, allowMissing bool, bench *Benchmark) (map[string]interface{}, error) {
	start := time.Now()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && allowMissing {
			return nil, err
		}
		if bench != nil {
			recordFile(bench, fileType, time.Since(start).Seconds()*1000)
		}
		return nil, err
	}

	value := map[string]interface{}{}
	err = yaml.Unmarshal(raw, &value)
	if err != nil {
		if bench != nil {
			recordFile(bench, fileType, time.Since(start).Seconds()*1000)
		}
		return nil, fmt.Errorf("invalid yaml in %s: %w", path, err)
	}
	if bench != nil {
		recordFile(bench, fileType, time.Since(start).Seconds()*1000)
	}
	return value, nil
}

func (l *Loader) filterByPath(tree models.TaskTree, target models.TaskPath) models.TaskTree {
	filtered := models.TaskTree{
		Project:       tree.Project,
		Description:   tree.Description,
		TimelineWeeks: tree.TimelineWeeks,
		CriticalPath:  append([]string{}, tree.CriticalPath...),
		NextAvailable: tree.NextAvailable,
		Bugs:          append([]models.Task{}, tree.Bugs...),
		Ideas:         append([]models.Task{}, tree.Ideas...),
	}

	switch target.Depth() {
	case 1:
		for _, phase := range tree.Phases {
			if phase.ID == target.PhaseID() {
				filtered.Phases = append(filtered.Phases, phase)
			}
		}
	case 2:
		for _, phase := range tree.Phases {
			if phase.ID != target.PhaseID() {
				continue
			}
			copyPhase := phase
			copyPhase.Milestones = []models.Milestone{}
			for _, milestone := range phase.Milestones {
				if milestone.ID == target.MilestoneID() {
					copyPhase.Milestones = append(copyPhase.Milestones, milestone)
				}
			}
			filtered.Phases = append(filtered.Phases, copyPhase)
		}
	case 3:
		for _, phase := range tree.Phases {
			if phase.ID != target.PhaseID() {
				continue
			}
			copyPhase := phase
			copyPhase.Milestones = []models.Milestone{}
			for _, milestone := range phase.Milestones {
				copyMilestone := milestone
				copyMilestone.Epics = []models.Epic{}
				for _, epic := range milestone.Epics {
					if epic.ID == target.EpicID() {
						copyMilestone.Epics = append(copyMilestone.Epics, epic)
					}
				}
				if len(copyMilestone.Epics) > 0 {
					copyPhase.Milestones = append(copyPhase.Milestones, copyMilestone)
				}
			}
			filtered.Phases = append(filtered.Phases, copyPhase)
		}
	case 4:
		for _, phase := range tree.Phases {
			if phase.ID != target.PhaseID() {
				continue
			}
			copyPhase := phase
			copyPhase.Milestones = []models.Milestone{}
			for _, milestone := range phase.Milestones {
				copyMilestone := milestone
				copyMilestone.Epics = []models.Epic{}
				for _, epic := range milestone.Epics {
					if !strings.HasPrefix(epic.ID, target.EpicID()) {
						continue
					}
					copyEpic := epic
					copyEpic.Tasks = []models.Task{}
					for _, task := range epic.Tasks {
						if task.ID == target.TaskID() {
							copyEpic.Tasks = append(copyEpic.Tasks, task)
							break
						}
					}
					copyMilestone.Epics = append(copyMilestone.Epics, copyEpic)
				}
				if len(copyMilestone.Epics) > 0 {
					copyPhase.Milestones = append(copyPhase.Milestones, copyMilestone)
				}
			}
			filtered.Phases = append(filtered.Phases, copyPhase)
		}
	}

	return filtered
}

func normalizeMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "", loadModeFull, loadModeMetadata, loadModeIndex:
		return m
	default:
		return loadModeFull
	}
}

func newBenchmark() Benchmark {
	return Benchmark{
		Files: map[string]int{
			"root_index":      0,
			"phase_index":     0,
			"milestone_index": 0,
			"epic_index":      0,
			"todo_file":       0,
			"bug_index":       0,
			"idea_index":      0,
			"bug_file":        0,
			"idea_file":       0,
		},
		FilesByTypeMs: map[string]float64{
			"root_index":      0,
			"phase_index":     0,
			"milestone_index": 0,
			"epic_index":      0,
			"todo_file":       0,
			"bug_index":       0,
			"idea_index":      0,
			"bug_file":        0,
			"idea_file":       0,
		},
		Counts: map[string]int{
			"phases":     0,
			"milestones": 0,
			"epics":      0,
			"tasks":      0,
		},
		PhaseTimings:     []map[string]any{},
		MilestoneTimings: []map[string]any{},
		EpicTimings:      []map[string]any{},
		TaskTimings:      []map[string]any{},
	}
}

func recordTiming(bench *Benchmark, bucket string, ms int64, id, path string) {
	if bench == nil {
		return
	}
	rec := map[string]any{"id": id, "path": path, "ms": float64(ms)}
	switch bucket {
	case "phase_timings":
		bench.PhaseTimings = append(bench.PhaseTimings, rec)
	case "milestone_timings":
		bench.MilestoneTimings = append(bench.MilestoneTimings, rec)
	case "epic_timings":
		bench.EpicTimings = append(bench.EpicTimings, rec)
	case "task_timings":
		bench.TaskTimings = append(bench.TaskTimings, rec)
	}
}

func recordFile(bench *Benchmark, fileType string, elapsedMs float64) {
	if bench == nil {
		return
	}
	bench.Files[fileType]++
	bench.FilesByTypeMs[fileType] += elapsedMs
	if strings.HasSuffix(fileType, "_index") || strings.HasSuffix(fileType, "_file") {
		bench.IndexParseMs += elapsedMs
	}
}

func detectTasksDir() string {
	dataDir, err := config.DetectDataDir()
	if err != nil {
		return ""
	}
	return dataDir
}

func asString(v interface{}) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func asBool(v interface{}) bool {
	switch value := v.(type) {
	case bool:
		return value
	case string:
		return strings.ToLower(strings.TrimSpace(value)) == "true"
	default:
		return false
	}
}

func asInt(v interface{}) int {
	switch value := v.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

func asFloat(values ...interface{}) float64 {
	for _, raw := range values {
		switch value := raw.(type) {
		case float64:
			return value
		case float32:
			return float64(value)
		case int:
			return float64(value)
		case int64:
			return float64(value)
		case string:
			var parsed float64
			if _, err := fmt.Sscanf(value, "%f", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func asFloatFromMap(input map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		value, ok := input[key]
		if !ok {
			continue
		}
		return asFloat(value), true
	}
	return 0, false
}

func asStringSlice(v interface{}) []string {
	switch value := v.(type) {
	case nil:
		return []string{}
	case []string:
		return value
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		if value != nil {
			return []string{fmt.Sprintf("%v", value)}
		}
		return []string{}
	}
}

func normalizeTaskID(raw string, epPath models.TaskPath) (string, error) {
	if raw == "" || !epPath.IsEpic() {
		return raw, nil
	}
	if strings.Contains(raw, ".") {
		return raw, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(raw)), "T") {
		return "", fmt.Errorf("invalid task id %q for scope %s", raw, epPath.FullID())
	}
	fullPath, err := epPath.WithTask(raw)
	if err != nil {
		return "", err
	}
	return fullPath.FullID(), nil
}

func asSlice(v interface{}) []interface{} {
	slice, ok := v.([]interface{})
	if !ok {
		return []interface{}{}
	}
	return slice
}

func coerceStatus(raw interface{}) models.Status {
	value := strings.ToLower(strings.TrimSpace(asString(raw)))
	if status, err := models.ParseStatus(value); err == nil {
		return status
	}
	if value == "" {
		return models.StatusPending
	}
	return models.Status(value)
}

func coercePriority(raw interface{}) models.Priority {
	value := strings.TrimSpace(strings.ToLower(asString(raw)))
	if priority, err := models.ParsePriority(value); err == nil {
		return priority
	}
	if value == "" {
		return models.PriorityMedium
	}
	return models.PriorityMedium
}

func coerceComplexity(raw interface{}) models.Complexity {
	value := strings.TrimSpace(strings.ToLower(asString(raw)))
	if complexity, err := models.ParseComplexity(value); err == nil {
		return complexity
	}
	if value == "" {
		return models.ComplexityMedium
	}
	return models.ComplexityMedium
}

func expandDependsOn(dependsOn []string, epic models.TaskPath) []string {
	if len(dependsOn) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(dependsOn))
	for _, dep := range dependsOn {
		if dep == "" {
			continue
		}
		if strings.HasPrefix(dep, "P") && strings.Count(dep, ".") >= 3 {
			out = append(out, dep)
			continue
		}
		if strings.HasPrefix(dep, "T") {
			if epic.IsTask() {
				path, err := epic.WithTask(dep)
				if err != nil {
					out = append(out, dep)
					continue
				}
				out = append(out, path.FullID())
				continue
			}
			if epic.IsEpic() {
				path, err := epic.WithTask(dep)
				if err != nil {
					out = append(out, dep)
					continue
				}
				out = append(out, path.FullID())
				continue
			}
			out = append(out, dep)
			continue
		}
		out = append(out, dep)
	}
	return out
}

func parseRFC3339(v interface{}) *time.Time {
	text := asString(v)
	if text == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.ReplaceAll(text, "Z", "+00:00"))
	if err != nil {
		return nil
	}
	return &parsed
}

func fileTypeFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".todo":
		return "todo_file"
	default:
		return "yaml"
	}
}

func fileTypeIndex(fileType string) string {
	switch fileType {
	case "todo_file", "bug_file", "idea_file":
		return fileType
	default:
		return "root_index"
	}
}

func taskPathFromTaskID(taskID string) string {
	parts := strings.SplitN(taskID, ".", 2)
	if len(parts) == 2 {
		return parts[0]
	}
	return ""
}
