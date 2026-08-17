// Package journey manages the lifecycle of the synthetic monitoring journey
// process (node journey.js) that continuously exercises the trace test bed.
//
// The journey runs on the same host as this service, so it is launched and
// terminated directly with os/exec rather than over SSH. Start detaches the
// child into its own session (Setsid) so it survives API-server restarts,
// mirroring the `nohup ... & disown` used by the original shell commands.
package journey

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultTargetURL     = "http://10.1.92.192:8081/"
	defaultClickInterval = 2000
	defaultIterations    = 0
	stopGrace            = 5 * time.Second
	logTailBytes         = 16 * 1024
)

// Params are the tunable knobs accepted by the start/trigger endpoints.
// Empty fields fall back to the journey defaults.
type Params struct {
	ClickIntervalMS int    `json:"clickIntervalMs"`
	Iterations      int    `json:"iterations"`
	TargetURL       string `json:"targetUrl"`
	Headless        *bool  `json:"headless"`
}

// Status is the current lifecycle snapshot returned by the status endpoint.
type Status struct {
	Status    string `json:"status"` // "running" | "stopped"
	PID       int    `json:"pid"`
	Iteration int    `json:"iteration,omitempty"`
	LatencyMS int64  `json:"latencyMs,omitempty"`
	LastLog   string `json:"lastLog,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Manager controls the synthetic journey subprocess.
type Manager struct {
	dir     string
	logFile string
	outFile string
	pidFile string

	mu sync.Mutex
}

// New builds a Manager from environment variables, falling back to the
// synthetic-journey deployment paths on this host.
func New() *Manager {
	dir := os.Getenv("JOURNEY_DIR")
	if dir == "" {
		dir = "/home/vunet/synthetic-journey"
	}
	logFile := os.Getenv("JOURNEY_LOG_FILE")
	if logFile == "" {
		logFile = dir + "/journey.log"
	}
	pidFile := os.Getenv("JOURNEY_PID_FILE")
	if pidFile == "" {
		pidFile = dir + "/journey.pid"
	}
	return &Manager{
		dir:     dir,
		logFile: logFile,
		outFile: dir + "/journey.out",
		pidFile: pidFile,
	}
}

// Start launches the journey detached from this process and records its PID in
// the pidfile. If a journey is already running it is left untouched.
func (m *Manager) Start(p Params) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing := m.runningPID(); existing != 0 {
		return Status{Status: "running", PID: existing, Message: "journey already running"}, nil
	}

	p = withDefaults(p)
	headless := true
	if p.Headless != nil {
		headless = *p.Headless
	}

	env := append(os.Environ(),
		"CLICK_INTERVAL_MS="+strconv.Itoa(p.ClickIntervalMS),
		"ITERATIONS="+strconv.Itoa(p.Iterations),
		"TARGET_URL="+p.TargetURL,
		"HEADLESS="+strconv.FormatBool(headless),
		"LOG_FILE="+m.logFile,
	)

	out, err := os.OpenFile(m.outFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Status{}, fmt.Errorf("open journey.out: %w", err)
	}

	cmd := exec.Command("node", "journey.js")
	cmd.Dir = m.dir
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		out.Close()
		return Status{}, fmt.Errorf("start journey: %w", err)
	}
	out.Close()   // parent copy; the child keeps its own
	go cmd.Wait() // reap the child when the journey exits

	_ = os.WriteFile(m.pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)

	return Status{Status: "running", PID: cmd.Process.Pid, Message: "journey started"}, nil
}

// Stop terminates the running journey gracefully (SIGTERM, escalated to
// SIGKILL if it does not exit within the grace period) and clears the pidfile.
func (m *Manager) Stop() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pids := m.runningPIDs()
	if len(pids) == 0 {
		_ = os.Remove(m.pidFile)
		return Status{Status: "stopped", Message: "journey already stopped"}, nil
	}

	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}

	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		allGone := true
		for _, pid := range pids {
			if alive(pid) {
				allGone = false
				break
			}
		}
		if allGone {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, pid := range pids {
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}

	_ = os.Remove(m.pidFile)
	return Status{Status: "stopped", PID: pids[0], Message: "journey stopped"}, nil
}

// Status reports whether the journey is running and, when it is, the latest
// entry tailed from the journey log.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := Status{Status: "stopped"}
	if pids := m.runningPIDs(); len(pids) > 0 {
		s.Status = "running"
		s.PID = pids[0]
	}
	if e := m.lastLogEntry(); e != nil {
		s.Iteration = e.Iteration
		s.LatencyMS = e.LatencyMS
		s.LastLog = e.Raw
		s.Message = e.Msg
	}
	return s
}

// runningPID returns the PID of the currently running journey, or 0.
func (m *Manager) runningPID() int {
	pids := m.runningPIDs()
	if len(pids) == 0 {
		return 0
	}
	return pids[0]
}

// runningPIDs returns every live journey PID, preferring the tracked pidfile
// PID before falling back to a pgrep scan (to also catch manually-started
// journeys).
func (m *Manager) runningPIDs() []int {
	var pids []int
	seen := make(map[int]bool)

	if pid := m.pidFromFile(); pid > 0 && alive(pid) {
		pids = append(pids, pid)
		seen[pid] = true
	}
	for _, pid := range pgrepJourney() {
		if !seen[pid] && alive(pid) {
			pids = append(pids, pid)
			seen[pid] = true
		}
	}
	return pids
}

func (m *Manager) pidFromFile() int {
	data, err := os.ReadFile(m.pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

type logEntry struct {
	Raw       string
	Iteration int
	LatencyMS int64
	Msg       string
}

func (m *Manager) lastLogEntry() *logEntry {
	data, err := readTail(m.logFile, logTailBytes)
	if err != nil {
		return nil
	}
	line := lastJSONLine(data)
	if line == "" {
		return nil
	}
	var parsed struct {
		Iteration int    `json:"iteration"`
		LatencyMS int64  `json:"latencyMs"`
		Msg       string `json:"msg"`
	}
	_ = json.Unmarshal([]byte(line), &parsed)
	return &logEntry{Raw: line, Iteration: parsed.Iteration, LatencyMS: parsed.LatencyMS, Msg: parsed.Msg}
}

func withDefaults(p Params) Params {
	if p.TargetURL == "" {
		p.TargetURL = defaultTargetURL
	}
	if p.ClickIntervalMS <= 0 {
		p.ClickIntervalMS = defaultClickInterval
	}
	if p.Iterations <= 0 {
		p.Iterations = defaultIterations
	}
	return p
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// pgrepJourney finds live journey processes. The pattern is anchored to the
// exact command line of the journey (`node journey.js`) so that unrelated
// processes merely mentioning the pattern (e.g. a shell running `curl ... stop`
// or `pgrep -f "node journey.js"`) are not matched.
func pgrepJourney() []int {
	out, err := exec.Command("pgrep", "-f", "^node journey.js$").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, field := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(field); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// readTail returns the last n bytes of path.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - n
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return nil, err
	}
	return buf, nil
}

// lastJSONLine returns the final non-empty line of data that looks like a JSON
// object, or an empty string if there is none.
func lastJSONLine(data []byte) string {
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") {
			return line
		}
	}
	return ""
}
