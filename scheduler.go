package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScheduledTask represents a task to be executed at a specific time
type ScheduledTask struct {
	ID        string
	ChannelID string
	ThreadTS  string
	WorkDir   string
	Command   string
	RunAt     time.Time
	CreatedAt time.Time
}

// Scheduler manages scheduled tasks
type Scheduler struct {
	mu     sync.Mutex
	tasks  map[string]*ScheduledTask // ID -> task
	nextID int
	stopCh chan struct{}
	config *Config
}

// Global scheduler instance
var scheduler *Scheduler

// NewScheduler creates a new scheduler
func NewScheduler(config *Config) *Scheduler {
	s := &Scheduler{
		tasks:  make(map[string]*ScheduledTask),
		stopCh: make(chan struct{}),
		config: config,
	}
	go s.run()
	return s
}

// run is the main scheduler loop
func (s *Scheduler) run() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkAndRunTasks()
		}
	}
}

// checkAndRunTasks checks for due tasks and runs them
func (s *Scheduler) checkAndRunTasks() {
	s.mu.Lock()
	now := time.Now()
	var toRun []*ScheduledTask

	for id, task := range s.tasks {
		if now.After(task.RunAt) || now.Equal(task.RunAt) {
			toRun = append(toRun, task)
			delete(s.tasks, id)
		}
	}
	s.mu.Unlock()

	// Run tasks outside lock
	for _, task := range toRun {
		s.executeTask(task)
	}
}

// executeTask runs a scheduled task
func (s *Scheduler) executeTask(task *ScheduledTask) {
	logf("Running scheduled task %s: %s", task.ID, task.Command)

	// Notify that task is starting
	sendMessageToThread(s.config, task.ChannelID, task.ThreadTS,
		fmt.Sprintf(":alarm_clock: *Scheduled task running:* `%s`", task.Command))

	// Build prompt with slack prefix
	prompt := slackUserPrefix + task.Command

	// Run Claude
	resp, err := callClaudeStreaming(prompt, task.ChannelID, task.ThreadTS, task.WorkDir, s.config)
	if err != nil {
		sendMessageToThread(s.config, task.ChannelID, task.ThreadTS,
			fmt.Sprintf(":x: Scheduled task failed: %v", err))
		return
	}

	logf("Scheduled task %s completed (tokens: %d in / %d out)",
		task.ID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

// Schedule adds a new scheduled task
// Returns task ID and formatted run time
func (s *Scheduler) Schedule(channelID, threadTS, workDir, timeSpec, command string) (string, time.Time, error) {
	runAt, err := parseTimeSpec(timeSpec)
	if err != nil {
		return "", time.Time{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := fmt.Sprintf("task-%d", s.nextID)

	task := &ScheduledTask{
		ID:        id,
		ChannelID: channelID,
		ThreadTS:  threadTS,
		WorkDir:   workDir,
		Command:   command,
		RunAt:     runAt,
		CreatedAt: time.Now(),
	}
	s.tasks[id] = task

	return id, runAt, nil
}

// Cancel removes a scheduled task
func (s *Scheduler) Cancel(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.tasks[taskID]; ok {
		delete(s.tasks, taskID)
		return true
	}
	return false
}

// List returns all scheduled tasks for a channel
func (s *Scheduler) List(channelID string) []*ScheduledTask {
	s.mu.Lock()
	defer s.mu.Unlock()

	var tasks []*ScheduledTask
	for _, task := range s.tasks {
		if task.ChannelID == channelID {
			tasks = append(tasks, task)
		}
	}

	// Sort by run time
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].RunAt.Before(tasks[j].RunAt)
	})

	return tasks
}

// parseTimeSpec parses time specifications like:
// - "5m", "10m", "1h", "2h30m" (relative)
// - "9am", "14:30", "9:00am" (today or tomorrow if past)
// - "tomorrow 9am"
func parseTimeSpec(spec string) (time.Time, error) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	now := time.Now()

	// Relative time: 5m, 1h, 2h30m
	if matched, _ := regexp.MatchString(`^\d+[mh]`, spec); matched {
		return parseRelativeTime(spec, now)
	}

	// Handle "tomorrow" prefix
	isTomorrow := false
	if strings.HasPrefix(spec, "tomorrow ") {
		isTomorrow = true
		spec = strings.TrimPrefix(spec, "tomorrow ")
	}

	// Parse time of day
	t, err := parseTimeOfDay(spec, now)
	if err != nil {
		return time.Time{}, err
	}

	if isTomorrow {
		t = t.AddDate(0, 0, 1)
	} else if t.Before(now) {
		// If time is in the past, schedule for tomorrow
		t = t.AddDate(0, 0, 1)
	}

	return t, nil
}

func parseRelativeTime(spec string, now time.Time) (time.Time, error) {
	// Parse patterns like "5m", "1h", "2h30m"
	re := regexp.MustCompile(`(?:(\d+)h)?(?:(\d+)m)?`)
	matches := re.FindStringSubmatch(spec)

	if matches == nil {
		return time.Time{}, fmt.Errorf("invalid relative time: %s", spec)
	}

	var hours, minutes int
	if matches[1] != "" {
		hours, _ = strconv.Atoi(matches[1])
	}
	if matches[2] != "" {
		minutes, _ = strconv.Atoi(matches[2])
	}

	if hours == 0 && minutes == 0 {
		return time.Time{}, fmt.Errorf("invalid relative time: %s", spec)
	}

	return now.Add(time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute), nil
}

func parseTimeOfDay(spec string, now time.Time) (time.Time, error) {
	// Try various formats
	formats := []string{
		"3:04pm",
		"3:04 pm",
		"3pm",
		"3 pm",
		"15:04",
		"15h04",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, spec); err == nil {
			// Combine with today's date
			return time.Date(now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), 0, 0, now.Location()), nil
		}
	}

	return time.Time{}, fmt.Errorf("invalid time format: %s (use e.g., 9am, 14:30, 5m, 1h)")
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	close(s.stopCh)
}
