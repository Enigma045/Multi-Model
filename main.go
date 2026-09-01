package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const defaultPort = 5005

// ANSI color codes
const (
	colorReset      = "\033[0m"
	colorRed        = "\033[31m"
	colorGreen      = "\033[32m"
	colorYellow     = "\033[33m"
	colorCyan       = "\033[36m"
	colorClaude     = "\033[38;2;217;119;87m" // Terracotta
	colorGemini     = "\033[38;2;59;130;246m"  // Google Blue
	colorGrok       = "\033[38;2;29;155;240m"  // X / Grok Electric Cyan/Blue
	colorTerracotta = "\033[38;2;217;119;87m"
	colorGray       = "\033[90m"
	colorBold       = "\033[1m"
)

var (
	activeModel         = "claude" // "claude", "gemini", or "grok"
	activeModelMu       sync.RWMutex
	isTerminalWaiting   bool
	terminalWaitingMu   sync.Mutex
	spontaneousChan     = make(chan MessageEvent, 50)
	spontaneousUserChan = make(chan MessageEvent, 50)
)

type MessageEvent struct {
	Provider string
	Text     string
}

type FilePayload struct {
	Name     string `json:"name"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Type     string `json:"type,omitempty"`
}

type BridgeServer struct {
	mu             sync.Mutex
	pendingPrompt  string
	pendingFiles   []FilePayload
	pendingTarget  string
	latestResponse string
	responseChan   chan string
	wsClients      map[io.Writer]bool
}

var serverInstance = &BridgeServer{
	responseChan: make(chan string, 10),
	wsClients:    make(map[io.Writer]bool),
}

type ExtractedFile struct {
	Filename string `json:"filename"`
	Code     string `json:"code"`
	Action   string `json:"action,omitempty"`
	Run      any    `json:"run,omitempty"`
}

type ActionItem struct {
	Type       string `json:"type"`        // e.g. "run"
	Target     string `json:"target"`      // file or script path, e.g. "build.bat"
	Command    string `json:"command"`     // raw command line
	WorkingDir string `json:"working_dir"` // e.g. "Sandbox"
}

type ExtractedPayload struct {
	Action  any             `json:"action,omitempty"`
	Actions any             `json:"actions,omitempty"`
	Run     any             `json:"run,omitempty"`
	Target  string          `json:"target,omitempty"`
	Command string          `json:"command,omitempty"`
	Files   []ExtractedFile `json:"files,omitempty"`
}

type ExecutionResult struct {
	Target   string
	Success  bool
	ExitCode int
	Duration time.Duration
	Output   string
	Error    error
}

var markdownJsonRegex = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{[\\s\\S]*?\\})\\s*```")

func isExecutableScript(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".bat" || ext == ".cmd" || ext == ".ps1" || ext == ".sh" || ext == ".py" || ext == ".exe"
}

func extractActions(payload *ExtractedPayload, savedFiles []string) []ActionItem {
	var actions []ActionItem
	seen := make(map[string]bool)

	addAction := func(act ActionItem) {
		if act.Type == "" {
			act.Type = "run"
		}
		key := act.Type + ":" + act.Target + ":" + act.Command
		if !seen[key] && (act.Target != "" || act.Command != "") {
			seen[key] = true
			if act.WorkingDir == "" {
				act.WorkingDir = "Sandbox"
			}
			actions = append(actions, act)
		}
	}

	// 1. Check file-level actions
	for _, f := range payload.Files {
		actLower := strings.ToLower(strings.TrimSpace(f.Action))
		if actLower == "run" || actLower == "exec" || actLower == "execute" {
			addAction(ActionItem{Type: "run", Target: f.Filename})
		} else if f.Run == true || fmt.Sprintf("%v", f.Run) == "true" {
			addAction(ActionItem{Type: "run", Target: f.Filename})
		}
	}

	// Helper to resolve an action object/string
	parseSingleAction := func(val any) {
		if val == nil {
			return
		}
		switch v := val.(type) {
		case string:
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				return
			}
			lower := strings.ToLower(trimmed)
			if lower == "run" || lower == "exec" || lower == "execute" {
				if payload.Target != "" {
					addAction(ActionItem{Type: "run", Target: payload.Target})
				} else if payload.Command != "" {
					addAction(ActionItem{Type: "run", Command: payload.Command})
				} else {
					// Only auto-pick from saved files if it is an executable script or web document
					for _, sf := range savedFiles {
						if isExecutableScript(sf) {
							addAction(ActionItem{Type: "run", Target: sf})
							break
						}
					}
				}
			} else if strings.HasPrefix(lower, "run ") || strings.HasPrefix(lower, "exec ") || strings.HasPrefix(lower, "open ") {
				parts := strings.SplitN(trimmed, " ", 2)
				if len(parts) > 1 {
					sub := strings.TrimSpace(parts[1])
					if isExecutableScript(sub) || isWebDocument(sub) {
						addAction(ActionItem{Type: "run", Target: sub})
					} else {
						addAction(ActionItem{Type: "run", Command: sub})
					}
				}
			} else if isExecutableScript(trimmed) || isWebDocument(trimmed) {
				addAction(ActionItem{Type: "run", Target: trimmed})
			} else {
				addAction(ActionItem{Type: "run", Command: trimmed})
			}
		case map[string]any:
			actType := "run"
			if t, ok := v["type"].(string); ok && t != "" {
				actType = t
			} else if a, ok := v["action"].(string); ok && a != "" {
				actType = a
			}
			target := ""
			if tg, ok := v["target"].(string); ok {
				target = tg
			} else if f, ok := v["file"].(string); ok {
				target = f
			} else if fn, ok := v["filename"].(string); ok {
				target = fn
			}
			cmd := ""
			if c, ok := v["command"].(string); ok {
				cmd = c
			} else if c, ok := v["cmd"].(string); ok {
				cmd = c
			}
			wd := "Sandbox"
			if w, ok := v["working_dir"].(string); ok && w != "" {
				wd = w
			}
			if target != "" || cmd != "" {
				addAction(ActionItem{Type: actType, Target: target, Command: cmd, WorkingDir: wd})
			}
		}
	}

	// 2. Check root-level Action
	parseSingleAction(payload.Action)

	// 3. Check root-level Actions (array)
	if payload.Actions != nil {
		if list, ok := payload.Actions.([]any); ok {
			for _, item := range list {
				parseSingleAction(item)
			}
		}
	}

	// 4. Check root-level Run
	if payload.Run != nil {
		if rStr, ok := payload.Run.(string); ok {
			parseSingleAction(rStr)
		} else if rBool, ok := payload.Run.(bool); ok && rBool {
			for _, sf := range savedFiles {
				if isExecutableScript(sf) {
					addAction(ActionItem{Type: "run", Target: sf})
					break
				}
			}
		} else if rList, ok := payload.Run.([]any); ok {
			for _, item := range rList {
				parseSingleAction(item)
			}
		}
	}

	return actions
}

func isWebDocument(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".html" || ext == ".htm"
}

func executeActionSilently(action ActionItem) ExecutionResult {
	startTime := time.Now()
	res := ExecutionResult{}

	cleanTarget := strings.TrimLeft(action.Target, "/\\")
	var targetPath string
	workingDir := action.WorkingDir
	if workingDir == "" {
		workingDir = "Sandbox"
	}

	if cleanTarget != "" {
		if strings.HasPrefix(cleanTarget, "Sandbox/") || strings.HasPrefix(cleanTarget, "Sandbox\\") {
			targetPath = cleanTarget
		} else {
			targetPath = filepath.Join("Sandbox", cleanTarget)
		}
	}

	var absTarget string
	var targetDir string
	var targetBase string
	if targetPath != "" {
		var err error
		absTarget, err = filepath.Abs(targetPath)
		if err != nil {
			absTarget = targetPath
		}
		targetDir = filepath.Dir(absTarget)
		targetBase = filepath.Base(absTarget)
	}

	var cmd *exec.Cmd
	displayLabel := action.Target
	if displayLabel == "" {
		displayLabel = action.Command
	}
	res.Target = displayLabel

	// Prepare command based on OS & file type
	if runtime.GOOS == "windows" {
		lowerTarget := strings.ToLower(absTarget)
		if strings.HasSuffix(lowerTarget, ".bat") || strings.HasSuffix(lowerTarget, ".cmd") {
			cmd = exec.Command("cmd.exe", "/c", targetBase)
			cmd.Dir = targetDir
		} else if strings.HasSuffix(lowerTarget, ".ps1") {
			cmd = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", targetBase)
			cmd.Dir = targetDir
		} else if strings.HasSuffix(lowerTarget, ".py") {
			cmd = exec.Command("python", targetBase)
			cmd.Dir = targetDir
		} else if strings.HasSuffix(lowerTarget, ".html") || strings.HasSuffix(lowerTarget, ".htm") {
			// Launch HTML in default web browser
			cmd = exec.Command("cmd.exe", "/c", "start", "", targetBase)
			cmd.Dir = targetDir
		} else if action.Command != "" {
			cmd = exec.Command("cmd.exe", "/c", action.Command)
			cmd.Dir = workingDir
		} else if absTarget != "" {
			cmd = exec.Command(absTarget)
			cmd.Dir = targetDir
		}
	} else {
		// Unix / Linux / macOS
		lowerTarget := strings.ToLower(absTarget)
		if strings.HasSuffix(lowerTarget, ".sh") {
			cmd = exec.Command("bash", targetBase)
			cmd.Dir = targetDir
		} else if strings.HasSuffix(lowerTarget, ".py") {
			cmd = exec.Command("python3", targetBase)
			cmd.Dir = targetDir
		} else if strings.HasSuffix(lowerTarget, ".html") || strings.HasSuffix(lowerTarget, ".htm") {
			if runtime.GOOS == "darwin" {
				cmd = exec.Command("open", absTarget)
			} else {
				cmd = exec.Command("xdg-open", absTarget)
			}
		} else if action.Command != "" {
			cmd = exec.Command("sh", "-c", action.Command)
			cmd.Dir = workingDir
		} else if absTarget != "" {
			cmd = exec.Command(absTarget)
			cmd.Dir = targetDir
		}
	}

	if cmd == nil {
		res.Error = fmt.Errorf("could not determine command for action target: %s", displayLabel)
		fmt.Printf("%s✖ Could not execute action: %v%s\n", colorRed, res.Error, colorReset)
		return res
	}

	// Apply silent execution (no console window popup on Windows)
	setHideWindow(cmd)

	// Ensure execution.log directory exists
	_ = os.MkdirAll("Sandbox", 0755)
	logPath := filepath.Join("Sandbox", "execution.log")
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logErr == nil {
		defer logFile.Close()
		timestamp := startTime.Format("2006-01-02 15:04:05")
		header := fmt.Sprintf("\n============================================================\n[%s] EXECUTION STARTED: %s\nWorking Directory: %s\n============================================================\n", timestamp, displayLabel, cmd.Dir)
		_, _ = logFile.WriteString(header)
	}

	// Print Action Execution Banner on Terminal
	fmt.Printf("\n%s⚡ Executing Action:%s %s%s%s (Silent Background Runner)\n", colorBold, colorReset, colorCyan, displayLabel, colorReset)
	if cmd.Dir != "" {
		fmt.Printf("%s📂 Directory:%s %s\n", colorGray, colorReset, cmd.Dir)
	}
	fmt.Printf("%s──────────────────────────────────────────────────────────%s\n", colorGray, colorReset)

	// Pipe combined stdout and stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		res.Error = err
		return res
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		res.Error = err
		return res
	}

	if err := cmd.Start(); err != nil {
		res.Error = err
		fmt.Printf("%s✖ Failed to start process: %v%s\n", colorRed, err, colorReset)
		if logFile != nil {
			_, _ = logFile.WriteString(fmt.Sprintf("[ERROR] Failed to start process: %v\n", err))
		}
		return res
	}

	var mu sync.Mutex
	var outputLines []string

	recordLine := func(streamName, line string, streamColor string) {
		elapsed := time.Since(startTime)
		mins := int(elapsed.Minutes())
		secs := int(elapsed.Seconds()) % 60

		mu.Lock()
		defer mu.Unlock()

		outputLines = append(outputLines, line)
		fmt.Printf("  %s[%02d:%02d]%s %s%s >%s %s\n", colorGray, mins, secs, colorReset, streamColor, streamName, colorReset, line)
		if logFile != nil {
			logEntry := fmt.Sprintf("[%02d:%02d] %s: %s\n", mins, secs, streamName, line)
			_, _ = logFile.WriteString(logEntry)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Stream stdout
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			recordLine("stdout", scanner.Text(), colorCyan)
		}
	}()

	// Stream stderr
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			recordLine("stderr", scanner.Text(), colorYellow)
		}
	}()

	wg.Wait()
	err = cmd.Wait()
	res.Duration = time.Since(startTime)
	res.Output = strings.Join(outputLines, "\n")

	fmt.Printf("%s──────────────────────────────────────────────────────────%s\n", colorGray, colorReset)

	if err == nil {
		res.Success = true
		res.ExitCode = 0
		fmt.Printf("%s✔ Action completed successfully (Exit Code 0, Elapsed: %.2fs)%s\n", colorGreen, res.Duration.Seconds(), colorReset)
		if logFile != nil {
			_, _ = logFile.WriteString(fmt.Sprintf("[%s] EXECUTION COMPLETED: Success (Exit Code 0, Elapsed: %.2fs)\n", time.Now().Format("2006-01-02 15:04:05"), res.Duration.Seconds()))
		}
	} else {
		res.Success = false
		res.Error = err
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
		fmt.Printf("%s✖ Action failed (Exit Code %d, Elapsed: %.2fs)%s\n", colorRed, res.ExitCode, res.Duration.Seconds(), colorReset)
		if logFile != nil {
			_, _ = logFile.WriteString(fmt.Sprintf("[%s] EXECUTION FAILED: (Exit Code %d, Error: %v, Elapsed: %.2fs)\n", time.Now().Format("2006-01-02 15:04:05"), res.ExitCode, err, res.Duration.Seconds()))
		}
	}
	fmt.Printf("%s📝 Progress and output recorded to Sandbox/execution.log%s\n\n", colorGray, colorReset)

	return res
}

func extractAndSaveFiles(text string) []ExecutionResult {
	// 1. Try markdown JSON code blocks
	for _, match := range markdownJsonRegex.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			if ok, results := parseAndSaveJson(match[1]); ok {
				return results
			}
		}
	}

	// 2. Try balanced raw JSON substring
	if idx := strings.Index(text, `"files"`); idx != -1 {
		start := strings.LastIndex(text[:idx], "{")
		if start != -1 {
			sub := text[start:]
			if end := findBalancedClosingBrace(sub); end != -1 {
				if ok, results := parseAndSaveJson(sub[:end+1]); ok {
					return results
				}
			}
		}
	} else if idx := strings.Index(text, `"action"`); idx != -1 {
		start := strings.LastIndex(text[:idx], "{")
		if start != -1 {
			sub := text[start:]
			if end := findBalancedClosingBrace(sub); end != -1 {
				if ok, results := parseAndSaveJson(sub[:end+1]); ok {
					return results
				}
			}
		}
	}

	// 3. Resilient fallback: regex search for ("filename", "code") pairs in the text
	_, results := extractFilesResilient(text)
	return results
}

func findBalancedClosingBrace(s string) int {
	depth := 0
	inString := false
	var strQuote byte = 0
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if inString {
			if c == strQuote {
				inString = false
			}
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			inString = true
			strQuote = c
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return strings.LastIndexByte(s, '}')
}

func parseAndSaveJson(jsonStr string) (bool, []ExecutionResult) {
	trimmed := strings.TrimSpace(jsonStr)
	var payload ExtractedPayload
	var execResults []ExecutionResult

	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil && (len(payload.Files) > 0 || payload.Action != nil || payload.Actions != nil || payload.Run != nil) {
		var savedFiles []string
		for _, f := range payload.Files {
			if f.Filename != "" {
				if saveSingleFile(f.Filename, f.Code) {
					savedFiles = append(savedFiles, f.Filename)
				}
			}
		}

		// Extract and run actions
		actions := extractActions(&payload, savedFiles)
		for _, act := range actions {
			res := executeActionSilently(act)
			execResults = append(execResults, res)
		}

		return len(savedFiles) > 0 || len(actions) > 0, execResults
	}

	// If standard unmarshal failed (e.g. unescaped newlines), run resilient field extractor
	return extractFilesResilient(trimmed)
}

func saveSingleFile(filename, code string) bool {
	cleanName := strings.TrimLeft(filename, "/\\")
	var targetPath string
	if strings.HasPrefix(cleanName, "Sandbox/") || strings.HasPrefix(cleanName, "Sandbox\\") {
		targetPath = cleanName
	} else {
		targetPath = filepath.Join("Sandbox", cleanName)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return false
	}
	if err := os.WriteFile(targetPath, []byte(code), 0644); err == nil {
		fmt.Printf("%s💾 Auto-saved: %s (%d bytes)%s\n", colorGreen, targetPath, len(code), colorReset)
		return true
	}
	return false
}

func extractFilesResilient(text string) (bool, []ExecutionResult) {
	fnRegex := regexp.MustCompile(`"filename"\s*:\s*["']([^"']+)["']`)
	matches := fnRegex.FindAllStringSubmatchIndex(text, -1)
	var execResults []ExecutionResult

	var savedFiles []string
	if len(matches) > 0 {
		for i, loc := range matches {
			filename := text[loc[2]:loc[3]]
			afterFn := text[loc[1]:]

			codeIdx := strings.Index(afterFn, `"code"`)
			if codeIdx == -1 {
				continue
			}
			afterCode := afterFn[codeIdx+6:]
			colonIdx := strings.Index(afterCode, ":")
			if colonIdx == -1 {
				continue
			}
			valPart := strings.TrimSpace(afterCode[colonIdx+1:])
			if len(valPart) == 0 {
				continue
			}

			var code string
			quoteChar := valPart[0]
			if quoteChar == '"' || quoteChar == '\'' || quoteChar == '`' {
				contentStart := valPart[1:]
				var segment string
				if i+1 < len(matches) {
					offsetToNext := matches[i+1][0] - (loc[1] + codeIdx + 6 + colonIdx + 1)
					if offsetToNext > 0 && offsetToNext < len(contentStart) {
						segment = contentStart[:offsetToNext]
					} else {
						segment = contentStart
					}
				} else {
					segment = contentStart
				}

				lastQ := strings.LastIndexByte(segment, quoteChar)
				if lastQ != -1 {
					code = segment[:lastQ]
				} else {
					if closeBrace := strings.LastIndexByte(segment, '}'); closeBrace != -1 {
						code = strings.TrimSpace(segment[:closeBrace])
						if len(code) > 0 && (code[len(code)-1] == '"' || code[len(code)-1] == '\'') {
							code = code[:len(code)-1]
						}
					} else {
						code = segment
					}
				}

				if strings.Contains(code, `\n`) || strings.Contains(code, `\"`) {
					code = strings.ReplaceAll(code, `\n`, "\n")
					code = strings.ReplaceAll(code, `\r`, "\r")
					code = strings.ReplaceAll(code, `\t`, "\t")
					code = strings.ReplaceAll(code, `\"`, "\"")
					code = strings.ReplaceAll(code, `\\`, "\\")
				}
			}

			if filename != "" && code != "" {
				if saveSingleFile(filename, code) {
					savedFiles = append(savedFiles, filename)
				}
			}
		}
	}

	// Resilient action matching
	actionRegex := regexp.MustCompile(`"action"\s*:\s*["']([^"']+)["']`)
	if actMatch := actionRegex.FindStringSubmatch(text); len(actMatch) > 1 {
		actVal := actMatch[1]
		dummyPayload := &ExtractedPayload{Action: actVal}
		actions := extractActions(dummyPayload, savedFiles)
		for _, act := range actions {
			res := executeActionSilently(act)
			execResults = append(execResults, res)
		}
	}

	return len(savedFiles) > 0 || len(execResults) > 0, execResults
}

func formatExecutionFeedback(results []ExecutionResult) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Here is the terminal execution output from the action(s) you requested:\n\n")
	for _, res := range results {
		statusText := "Success (Exit Code: 0)"
		if !res.Success {
			statusText = fmt.Sprintf("Failed (Exit Code: %d)", res.ExitCode)
		}
		b.WriteString(fmt.Sprintf("============================================================\n[Execution Result: %s]\nStatus: %s\nElapsed: %.2fs\n", res.Target, statusText, res.Duration.Seconds()))
		if res.Error != nil && !res.Success {
			b.WriteString(fmt.Sprintf("Error: %v\n", res.Error))
		}
		b.WriteString("Output:\n```\n")
		if strings.TrimSpace(res.Output) == "" {
			b.WriteString("(No output generated)\n")
		} else {
			b.WriteString(strings.TrimSpace(res.Output))
			b.WriteString("\n")
		}
		b.WriteString("```\n============================================================\n\n")
	}
	b.WriteString("Please review the terminal execution results and provide feedback, fixes, or proceed with the next steps.")
	return b.String()
}

var attachStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "this": true, "that": true, "these": true, "those": true,
	"it": true, "its": true, "file": true, "files": true, "directory": true, "directories": true,
	"folder": true, "folders": true, "path": true, "paths": true, "to": true, "for": true,
	"and": true, "or": true, "in": true, "on": true, "with": true, "from": true, "your": true,
	"my": true, "our": true, "all": true, "any": true, "some": true, "each": true, "every": true,
	"one": true, "two": true, "new": true, "old": true, "here": true, "there": true, "please": true,
	"me": true, "us": true, "them": true, "him": true, "her": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "do": true, "does": true, "did": true, "will": true, "would": true, "should": true,
	"can": true, "could": true, "may": true, "might": true, "must": true,
}

var attachCmdRegex = regexp.MustCompile(`(?i)(?:^|[\r\n\s"'` + "`" + `])attach\s+["'` + "`" + `]?([a-zA-Z0-9_\-./\\]+)["'` + "`" + `]?`)

func isLikelyFilePath(candidate string) bool {
	candidate = strings.Trim(candidate, " \t\r\n\"'`.,;:")
	if candidate == "" || attachStopwords[strings.ToLower(candidate)] {
		return false
	}
	cleanName := strings.TrimLeft(candidate, "/\\")
	target := filepath.Join("Sandbox", cleanName)
	if strings.HasPrefix(cleanName, "Sandbox/") || strings.HasPrefix(cleanName, "Sandbox\\") {
		target = cleanName
	}
	// 1. If it actually exists in Sandbox, it is a valid target
	if _, err := os.Stat(target); err == nil {
		return true
	}
	// 2. Contains path separators
	if strings.Contains(candidate, "/") || strings.Contains(candidate, "\\") {
		return true
	}
	// 3. Has a recognized file extension
	ext := filepath.Ext(candidate)
	if ext != "" && len(ext) >= 2 && len(ext) <= 6 && !strings.Contains(ext, " ") {
		return true
	}
	return false
}

func extractClaudeAttachCommands(text string) []string {
	matches := attachCmdRegex.FindAllStringSubmatch(text, -1)
	var requestedFiles []string
	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) > 1 {
			filename := strings.Trim(m[1], " \t\r\n\"'`.,;:")
			if filename != "" && !seen[filename] && isLikelyFilePath(filename) {
				seen[filename] = true
				requestedFiles = append(requestedFiles, filename)
			}
		}
	}
	return requestedFiles
}

func getMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".txt", ".log":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js", ".mjs":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".go", ".rs", ".py", ".c", ".cpp", ".h", ".hpp", ".java", ".ts", ".tsx", ".jsx", ".sh", ".bat", ".cmd", ".ps1", ".yaml", ".yml", ".toml", ".sql":
		return "text/plain"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	default:
		return "application/octet-stream"
	}
}

func createFilePayload(cleanName, targetPath string) (*FilePayload, error) {
	data, err := os.ReadFile(targetPath)
	if err != nil {
		return nil, err
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return &FilePayload{
		Name:     filepath.Base(cleanName),
		Content:  b64,
		Encoding: "base64",
		Type:     getMimeType(cleanName),
	}, nil
}

// Helper to recursively list all files relative to a base directory
func getSandboxRelativeFiles(baseDir string) []string {
	var files []string
	_ = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			rel, err := filepath.Rel(baseDir, path)
			if err == nil {
				cleanRel := filepath.ToSlash(rel)
				if cleanRel != "file_list.txt" && !strings.HasPrefix(cleanRel, ".git") {
					files = append(files, cleanRel)
				}
			}
		}
		return nil
	})
	return files
}

func handleClaudeAttachRequests(answer string, depth int) bool {
	if depth >= 5 {
		return false
	}
	files := extractClaudeAttachCommands(answer)
	if len(files) == 0 {
		return false
	}

	var filePayloads []FilePayload
	var missingFiles []string

	for _, relPath := range files {
		cleanName := strings.TrimLeft(relPath, "/\\")
		target := filepath.Join("Sandbox", cleanName)
		if strings.HasPrefix(cleanName, "Sandbox/") || strings.HasPrefix(cleanName, "Sandbox\\") {
			target = cleanName
		}

		if fi, err := os.Stat(target); err == nil {
			if fi.IsDir() {
				dirFiles := getSandboxRelativeFiles(target)
				if len(dirFiles) > 0 && len(dirFiles) <= 10 {
					for _, df := range dirFiles {
						dfTarget := filepath.Join("Sandbox", df)
						if fp, err := createFilePayload(df, dfTarget); err == nil {
							filePayloads = append(filePayloads, *fp)
						}
					}
					fmt.Printf("%s📎 Claude requested directory '%s' -> Attaching %d files directly...%s\n", colorCyan, relPath, len(filePayloads), colorReset)
				} else {
					list := fmt.Sprintf("(No files in Sandbox/%s)", cleanName)
					if len(dirFiles) > 0 {
						list = strings.Join(dirFiles, "\n")
					}
					fmt.Printf("%s📎 Claude requested directory '%s' -> Sending file list (%d files)...%s\n", colorCyan, relPath, len(dirFiles), colorReset)
					missingFiles = append(missingFiles, fmt.Sprintf("List of files in Sandbox/%s:\n\n%s", cleanName, list))
				}
			} else {
				fp, err := createFilePayload(cleanName, target)
				if err == nil {
					fmt.Printf("%s📎 Claude requested '%s' -> Attaching exact file %s (%d bytes)...%s\n", colorCyan, relPath, target, fi.Size(), colorReset)
					filePayloads = append(filePayloads, *fp)
				} else {
					fmt.Printf("%s⚠️ Error reading '%s': %v%s\n", colorRed, target, err, colorReset)
				}
			}
		} else {
			fmt.Printf("%s⚠️ Claude requested '%s' but it was not found in Sandbox.%s\n", colorYellow, relPath, colorReset)
			missingFiles = append(missingFiles, relPath)
		}
	}

	// If we successfully attached files, send them directly without sending false error notices
	if len(filePayloads) > 0 {
		sendPromptInternalWithFiles("", filePayloads, depth+1)
		return true
	}

	// Only if NO files could be attached, report missing file notice
	if len(missingFiles) > 0 {
		notice := fmt.Sprintf("Notice: Requested file(s) [%s] not found in Sandbox directory.", strings.Join(missingFiles, ", "))
		sendPromptInternalWithFiles(notice, nil, depth+1)
		return true
	}

	return false
}

type AttachResult struct {
	Prompt string
	Files  []FilePayload
}

// Resolve attach keyword for Sandbox directory
func resolveSandboxAttach(input string) *AttachResult {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(strings.ToLower(trimmed), "attach") {
		return &AttachResult{Prompt: input}
	}
	arg := strings.TrimSpace(trimmed[6:])
	sandboxDir := "./Sandbox"
	_ = os.MkdirAll(sandboxDir, 0755)

	if arg == "" {
    files := getSandboxRelativeFiles(sandboxDir)
    list := "(No files in Sandbox)"
    if len(files) > 0 {
        list = strings.Join(files, "\n")
    }
    
    aiName := getProviderDisplayName(getActiveModel())
    instructions := fmt.Sprintf(`When responding, %s may include multiple files, do not use absolute paths, and do not use ../ paths. For normal conversation that does not require writing files, %s should respond normally. If %s requires a file, say "attach relative/path/to/file" to speed up the workflow.
%s can instruct the user's terminal to automatically execute batch scripts (.bat, .cmd), PowerShell scripts (.ps1), Python scripts (.py), or shell commands. When providing JSON formatted examples for running actions, it can be a root-level action, an explicit target or command, or a file-level action.`, aiName, aiName, aiName, aiName)

    combinedContent := list + "\n\n---\n\n" + instructions
    listPath := filepath.Join(sandboxDir, "file_list.txt")
    _ = os.WriteFile(listPath, []byte(combinedContent), 0644)
    
    fmt.Printf("%s📎 Attached Sandbox file list (%d files across all directories, saved to Sandbox/file_list.txt)%s\n", colorCyan, len(files), colorReset)

		if fp, err := createFilePayload("file_list.txt", listPath); err == nil {
			return &AttachResult{
				Prompt: "Here is the list of all files in my Sandbox directory:",
				Files:  []FilePayload{*fp},
			}
		}
		return &AttachResult{
			Prompt: fmt.Sprintf("Here is the list of all files in my Sandbox directory (including subdirectories):\n\n%s", list),
		}
	}

	parts := strings.SplitN(arg, " ", 2)
	target := filepath.Join(sandboxDir, parts[0])
	extraPrompt := ""
	if len(parts) > 1 {
		extraPrompt = strings.TrimSpace(parts[1])
	}

	// If target is a directory, list its files recursively
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		dirFiles := getSandboxRelativeFiles(target)
		var payloads []FilePayload
		if len(dirFiles) > 0 && len(dirFiles) <= 10 {
			for _, df := range dirFiles {
				dfTarget := filepath.Join("Sandbox", df)
				if fp, err := createFilePayload(df, dfTarget); err == nil {
					payloads = append(payloads, *fp)
				}
			}
			fmt.Printf("%s📎 Attaching %d files from Sandbox/%s%s\n", colorCyan, len(payloads), parts[0], colorReset)
			return &AttachResult{
				Prompt: extraPrompt,
				Files:  payloads,
			}
		}
		list := fmt.Sprintf("(No files in Sandbox/%s)", parts[0])
		if len(dirFiles) > 0 {
			list = strings.Join(dirFiles, "\n")
		}
		fmt.Printf("%s📎 Attached directory listing for Sandbox/%s (%d files)%s\n", colorCyan, parts[0], len(dirFiles), colorReset)
		return &AttachResult{
			Prompt: fmt.Sprintf("Here is the list of files in Sandbox/%s:\n\n%s", parts[0], list),
		}
	}

	if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
		if fp, err := createFilePayload(parts[0], target); err == nil {
			fmt.Printf("%s📎 Attaching Sandbox/%s (%d bytes)%s\n", colorCyan, parts[0], fi.Size(), colorReset)
			return &AttachResult{
				Prompt: extraPrompt,
				Files:  []FilePayload{*fp},
			}
		}
	}

	// Try whole arg in case path has spaces
	fullTarget := filepath.Join(sandboxDir, arg)
	if fi, err := os.Stat(fullTarget); err == nil && !fi.IsDir() {
		if fp, err := createFilePayload(arg, fullTarget); err == nil {
			fmt.Printf("%s📎 Attaching Sandbox/%s (%d bytes)%s\n", colorCyan, arg, fi.Size(), colorReset)
			return &AttachResult{
				Prompt: "",
				Files:  []FilePayload{*fp},
			}
		}
	}

	fmt.Printf("%s⚠️ File or directory '%s' not found in Sandbox%s\n", colorRed, arg, colorReset)
	return nil
}

func getActiveModel() string {
	activeModelMu.RLock()
	defer activeModelMu.RUnlock()
	return activeModel
}

func setActiveModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "grok" || m == "x" || m == "grk" {
		m = "grok"
	} else if m == "gemini" || m == "gem" || m == "g" {
		m = "gemini"
	} else {
		m = "claude"
	}
	activeModelMu.Lock()
	activeModel = m
	activeModelMu.Unlock()
	return m
}

func toggleActiveModel() string {
	activeModelMu.Lock()
	defer activeModelMu.Unlock()
	switch activeModel {
	case "claude":
		activeModel = "gemini"
	case "gemini":
		activeModel = "grok"
	case "grok":
		activeModel = "claude"
	default:
		activeModel = "claude"
	}
	return activeModel
}

func getProviderDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "grok", "x":
		return "Grok"
	case "gemini":
		return "Gemini"
	default:
		return "Claude"
	}
}

func getModelColor(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "grok", "x":
		return colorGrok
	case "gemini":
		return colorGemini
	default:
		return colorClaude
	}
}

func printPromptPrompt() {
	m := getActiveModel()
	name := getProviderDisplayName(m)
	col := getModelColor(m)
	fmt.Printf("%s%sYou [%s]%s > ", colorBold, col, name, colorReset)
}

func main() {
	serverOnly := flag.Bool("server", false, "Run in background bridge server mode only")
	modelFlag := flag.String("model", "", "Active AI model: 'claude', 'gemini', or 'grok'")
	flag.Parse()

	if *modelFlag != "" {
		setActiveModel(*modelFlag)
	}

	// Check if port 5005 is available; if so, start embedded bridge server
	startEmbeddedServerIfAvailable(defaultPort)

	if *serverOnly {
		fmt.Printf("%s[Bridge Server] Running on http://127.0.0.1:%d. Press Ctrl+C to stop.%s\n", colorGreen, defaultPort, colorReset)
		for {
			select {
			case userEvt := <-spontaneousUserChan:
				provName := getProviderDisplayName(userEvt.Provider)
				fmt.Printf("\n%s🌐 [%s Web Prompt]:%s %s\n", colorBold, provName, colorReset, strings.TrimSpace(userEvt.Text))
			case evt := <-spontaneousChan:
				provName := getProviderDisplayName(evt.Provider)
				col := getModelColor(evt.Provider)
				fmt.Printf("\n%s%s%s (Web):%s %s\n\n", colorBold, col, provName, colorReset, strings.TrimSpace(evt.Text))
				execResults := extractAndSaveFiles(evt.Text)
				if len(execResults) > 0 {
					feedbackMsg := formatExecutionFeedback(execResults)
					fmt.Printf("%s🔄 Sending terminal execution feedback to %s...%s\n\n", colorCyan, provName, colorReset)
					sendPromptInternalWithFiles(feedbackMsg, nil, 1)
				} else {
					handleClaudeAttachRequests(evt.Text, 0)
				}
			}
		}
	}

	args := flag.Args()
	if len(args) > 0 {
		prompt := strings.Join(args, " ")
		if resolved := resolveSandboxAttach(prompt); resolved != nil {
			sendPromptWithFiles(resolved.Prompt, resolved.Files)
		}
		return
	}

	// Interactive Mode
	runInteractiveChat()
}

// Start embedded HTTP bridge server if port is not in use
func startEmbeddedServerIfAvailable(port int) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		// Port already occupied (e.g. by another bridge instance or running bridge), which is fine
		return
	}

	mux := http.NewServeMux()

	// WebSocket endpoint for persistent, full-duplex communication
	mux.HandleFunc("/ws", handleWebSocket)

	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		enableCORS(w)
		serverInstance.mu.Lock()
		prompt := serverInstance.pendingPrompt
		files := serverInstance.pendingFiles
		target := serverInstance.pendingTarget
		serverInstance.pendingPrompt = ""
		serverInstance.pendingFiles = nil
		serverInstance.pendingTarget = ""
		serverInstance.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"prompt": prompt,
			"files":  files,
			"target": target,
		})
	})

	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		enableCORS(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		var payload struct {
			Prompt string        `json:"prompt"`
			Model  string        `json:"model,omitempty"`
			Files  []FilePayload `json:"files,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			if payload.Model != "" {
				setActiveModel(payload.Model)
			}
			sendPromptWithFiles(payload.Prompt, payload.Files)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	mux.HandleFunc("/response", func(w http.ResponseWriter, r *http.Request) {
		enableCORS(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		var payload struct {
			Response string `json:"response"`
			Provider string `json:"provider,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil && payload.Response != "" {
			prov := strings.ToLower(payload.Provider)
			if prov == "" {
				prov = "claude"
			}

			terminalWaitingMu.Lock()
			waiting := isTerminalWaiting
			terminalWaitingMu.Unlock()

			if waiting {
				select {
				case serverInstance.responseChan <- payload.Response:
				default:
				}
			} else {
				select {
				case spontaneousChan <- MessageEvent{Provider: prov, Text: payload.Response}:
				default:
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		enableCORS(w)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":       "online",
			"active_model": getActiveModel(),
		})
	})

	go http.Serve(listener, mux)
}

type WsMessage struct {
	Type     string        `json:"type"`
	Target   string        `json:"target,omitempty"`
	Provider string        `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"`
	Prompt   string        `json:"prompt,omitempty"`
	Text     string        `json:"text,omitempty"`
	Files    []FilePayload `json:"files,omitempty"`
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "Not a websocket handshake", http.StatusBadRequest)
		return
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "Missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}

	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	acceptKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey + "\r\n\r\n"

	bufrw.WriteString(response)
	bufrw.Flush()

	serverInstance.mu.Lock()
	serverInstance.wsClients[bufrw] = true
	serverInstance.mu.Unlock()

	defer func() {
		serverInstance.mu.Lock()
		delete(serverInstance.wsClients, bufrw)
		serverInstance.mu.Unlock()
	}()

	for {
		opcode, payload, err := readWsMessage(bufrw)
		if err != nil {
			break
		}
		if opcode == 0x8 { // Close
			break
		}
		if opcode == 0x9 { // Ping -> Pong
			pongHeader := []byte{0x8A, 0x00}
			bufrw.Write(pongHeader)
			bufrw.Flush()
			continue
		}
		if opcode == 0x1 { // Text frame
			var msg WsMessage
			if err := json.Unmarshal(payload, &msg); err == nil {
				prov := strings.ToLower(msg.Provider)
				if prov == "" {
					prov = "claude"
				}

				if msg.Type == "REGISTER" {
					provName := getProviderDisplayName(prov)
					col := getModelColor(prov)
					fmt.Printf("\r\033[K%s⚡ Browser Connected:%s %s%s%s\n", colorGreen, colorReset, col, provName, colorReset)
					printPromptPrompt()
					continue
				}

				if msg.Type == "USER_MESSAGE" && msg.Text != "" {
					select {
					case spontaneousUserChan <- MessageEvent{Provider: prov, Text: msg.Text}:
					default:
					}
				} else if msg.Type == "DONE" && msg.Text != "" {
					terminalWaitingMu.Lock()
					waiting := isTerminalWaiting
					terminalWaitingMu.Unlock()

					if waiting {
						select {
						case serverInstance.responseChan <- msg.Text:
						default:
						}
					} else {
						select {
						case spontaneousChan <- MessageEvent{Provider: prov, Text: msg.Text}:
						default:
						}
					}
				}
			}
		}
	}
}

func broadcastWsPromptWithFiles(prompt string, files []FilePayload) {
	target := getActiveModel()
	msg, _ := json.Marshal(WsMessage{
		Type:     "PROMPT",
		Target:   target,
		Provider: target,
		Model:    target,
		Prompt:   prompt,
		Files:    files,
	})
	serverInstance.mu.Lock()
	defer serverInstance.mu.Unlock()
	for client := range serverInstance.wsClients {
		_ = writeWsText(client, string(msg))
	}
}

func readWsMessage(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}

	opcode := header[0] & 0x0F
	masked := (header[1] & 0x80) != 0
	length := uint64(header[1] & 0x7F)

	if length == 126 {
		var l uint16
		if err := binary.Read(r, binary.BigEndian, &l); err != nil {
			return 0, nil, err
		}
		length = uint64(l)
	} else if length == 127 {
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return 0, nil, err
		}
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}

	if masked {
		for i := uint64(0); i < length; i++ {
			payload[i] ^= mask[i%4]
		}
	}

	return opcode, payload, nil
}

func writeWsText(w io.Writer, msg string) error {
	data := []byte(msg)
	length := len(data)

	var header []byte
	header = append(header, 0x81) // FIN + text opcode

	if length <= 125 {
		header = append(header, byte(length))
	} else if length <= 65535 {
		header = append(header, 126)
		h := make([]byte, 2)
		binary.BigEndian.PutUint16(h, uint16(length))
		header = append(header, h...)
	} else {
		header = append(header, 127)
		h := make([]byte, 8)
		binary.BigEndian.PutUint64(h, uint64(length))
		header = append(header, h...)
	}

	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if flusher, ok := w.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func sendPrompt(prompt string) {
	sendPromptInternalWithFiles(prompt, nil, 0)
}

func sendPromptWithFiles(prompt string, files []FilePayload) {
	sendPromptInternalWithFiles(prompt, files, 0)
}

func sendPromptInternalWithFiles(prompt string, files []FilePayload, depth int) {
	terminalWaitingMu.Lock()
	isTerminalWaiting = true
	terminalWaitingMu.Unlock()

	defer func() {
		terminalWaitingMu.Lock()
		isTerminalWaiting = false
		terminalWaitingMu.Unlock()
	}()

	currentModel := getActiveModel()
	modelName := getProviderDisplayName(currentModel)
	modelCol := getModelColor(currentModel)

	// First check if an external bridge or local server is responsive
	url := fmt.Sprintf("http://127.0.0.1:%d", defaultPort)
	resp, err := http.Get(url + "/status")
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Printf("%s⚠️ Cannot reach bridge at %s.%s\n", colorRed, url, colorReset)
		fmt.Printf("%sMake sure Chrome extension is loaded and %s is open in Chrome.%s\n", colorYellow, modelName, colorReset)
		return
	}
	resp.Body.Close()

	// Push prompt and files to WebSocket clients immediately
	broadcastWsPromptWithFiles(prompt, files)

	// Also set pendingPrompt and pendingFiles for HTTP poll fallback
	serverInstance.mu.Lock()
	serverInstance.pendingPrompt = prompt
	serverInstance.pendingFiles = files
	serverInstance.pendingTarget = currentModel
	// Drain any old response
	select {
	case <-serverInstance.responseChan:
	default:
	}
	serverInstance.mu.Unlock()

	// Show spinner
	stopSpinner := make(chan bool)
	spinnerText := fmt.Sprintf("Sending to %s AI...", modelName)
	if len(files) > 0 {
		spinnerText = fmt.Sprintf("Sending %d attached file(s) to %s...", len(files), modelName)
	}
	go showSpinnerWithColor(spinnerText, modelCol, stopSpinner)

	// Wait for response (up to 240 seconds)
	select {
	case answer := <-serverInstance.responseChan:
		stopSpinner <- true
		fmt.Printf("\n%s%s%s:%s %s\n\n", colorBold, modelCol, modelName, colorReset, strings.TrimSpace(answer))
		execResults := extractAndSaveFiles(answer)

		// Feedback loop: if actions were executed, automatically send terminal output back to Claude for review
		if len(execResults) > 0 && depth < 5 {
			feedbackMsg := formatExecutionFeedback(execResults)
			fmt.Printf("%s🔄 Sending terminal execution feedback to %s for review...%s\n\n", colorCyan, modelName, colorReset)
			sendPromptInternalWithFiles(feedbackMsg, nil, depth+1)
			return
		}

		handleClaudeAttachRequests(answer, depth)
	case <-time.After(240 * time.Second):
		stopSpinner <- true
		urlCheck := "https://claude.ai"
		if currentModel == "gemini" {
			urlCheck = "https://gemini.google.com"
		} else if currentModel == "grok" {
			urlCheck = "https://grok.com or https://x.com/i/grok"
		}
		fmt.Printf("\n%s⚠️ Timeout waiting for response. Please check that %s is open in Chrome.%s\n\n", colorRed, urlCheck, colorReset)
	}
}

// Interactive chat loop
func runInteractiveChat() {
	printBanner()

	inputChan := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			inputChan <- scanner.Text()
		}
	}()

	printPromptPrompt()

	for {
		select {
		case line, ok := <-inputChan:
			if !ok {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				printPromptPrompt()
				continue
			}

			lowerLine := strings.ToLower(line)
			switch lowerLine {
			case "exit", "quit", "q", ":q":
				fmt.Printf("%sGoodbye!%s\n", colorGray, colorReset)
				return
			case "clear", "cls":
				clearScreen()
				printBanner()
				printPromptPrompt()
				continue
			case "ls", "dir", "tree":
				printSandboxTree()
				printPromptPrompt()
				continue
			case "switch", "toggle", "switch model":
				newM := toggleActiveModel()
				fmt.Printf("\n%s🔀 Switched active AI model to: %s%s%s%s\n\n", colorGreen, colorBold, getModelColor(newM), getProviderDisplayName(newM), colorReset)
				printPromptPrompt()
				continue
			case "grok", "switch grok", "use grok", "/grok", "x", "switch x", "/x":
				setActiveModel("grok")
				fmt.Printf("\n%s🔀 Switched active AI model to: %s%sGrok%s\n\n", colorGreen, colorBold, colorGrok, colorReset)
				printPromptPrompt()
				continue
			case "gemini", "switch gemini", "use gemini", "/gemini":
				setActiveModel("gemini")
				fmt.Printf("\n%s🔀 Switched active AI model to: %s%sGemini%s\n\n", colorGreen, colorBold, colorGemini, colorReset)
				printPromptPrompt()
				continue
			case "claude", "switch claude", "use claude", "/claude":
				setActiveModel("claude")
				fmt.Printf("\n%s🔀 Switched active AI model to: %s%sClaude%s\n\n", colorGreen, colorBold, colorClaude, colorReset)
				printPromptPrompt()
				continue
			case "model", "models", "status":
				cur := getActiveModel()
				fmt.Printf("\n%s🤖 Active Model:%s %s%s%s\n", colorBold, colorReset, getModelColor(cur), getProviderDisplayName(cur), colorReset)
				fmt.Printf("%s  • Type 'switch' or 'claude' / 'gemini' / 'grok' to switch models anytime%s\n\n", colorGray, colorReset)
				printPromptPrompt()
				continue
			case "help":
				fmt.Printf("\n%sCommands:%s\n", colorBold, colorReset)
				fmt.Printf("  switch / toggle        - Cycle between Claude, Gemini, and Grok\n")
				fmt.Printf("  claude / gemini / grok - Switch to specific AI model\n")
				fmt.Printf("  model                  - Show current active AI model\n")
				fmt.Printf("  attach                 - Attach Sandbox file list\n")
				fmt.Printf("  attach <file>          - Attach specific file from Sandbox\n")
				fmt.Printf("  ls / tree              - Display files and folders in Sandbox\n")
				fmt.Printf("  clear                  - Clear terminal screen\n")
				fmt.Printf("  exit                   - Exit chat\n\n")
				printPromptPrompt()
				continue
			}

			if resolved := resolveSandboxAttach(line); resolved != nil {
				sendPromptWithFiles(resolved.Prompt, resolved.Files)
			}
			printPromptPrompt()

		case userEvt := <-spontaneousUserChan:
			provName := getProviderDisplayName(userEvt.Provider)
			fmt.Printf("\r\033[K%s🌐 [%s Web Prompt]:%s %s\n\n", colorBold, provName, colorReset, strings.TrimSpace(userEvt.Text))
			printPromptPrompt()

		case answerEvt := <-spontaneousChan:
			provName := getProviderDisplayName(answerEvt.Provider)
			col := getModelColor(answerEvt.Provider)
			fmt.Printf("\r\033[K\n%s%s%s (Web):%s %s\n\n", colorBold, col, provName, colorReset, strings.TrimSpace(answerEvt.Text))
			execResults := extractAndSaveFiles(answerEvt.Text)

			if len(execResults) > 0 {
				feedbackMsg := formatExecutionFeedback(execResults)
				fmt.Printf("%s🔄 Sending terminal execution feedback to %s for review...%s\n\n", colorCyan, provName, colorReset)
				sendPromptInternalWithFiles(feedbackMsg, nil, 1)
			} else {
				handleClaudeAttachRequests(answerEvt.Text, 0)
			}
			printPromptPrompt()
		}
	}
}

func printSandboxTree() {
	sandboxDir := "./Sandbox"
	_ = os.MkdirAll(sandboxDir, 0755)
	files := getSandboxRelativeFiles(sandboxDir)
	fmt.Printf("\n%s📂 Sandbox Files (%d total):%s\n", colorBold, len(files), colorReset)
	if len(files) == 0 {
		fmt.Printf("  %s(No files in Sandbox)%s\n\n", colorGray, colorReset)
		return
	}
	for _, f := range files {
		fi, err := os.Stat(filepath.Join(sandboxDir, f))
		if err == nil {
			fmt.Printf("  %s•%s %-35s %s(%d bytes)%s\n", colorCyan, colorReset, f, colorGray, fi.Size(), colorReset)
		} else {
			fmt.Printf("  %s•%s %s\n", colorCyan, colorReset, f)
		}
	}
	fmt.Println()
}

func printBanner() {
	cur := getActiveModel()
	name := getProviderDisplayName(cur)
	col := getModelColor(cur)

	fmt.Printf("%s==================================================%s\n", col, colorReset)
	fmt.Printf("%s%s  Claude, Gemini & Grok AI Terminal (Zero API Key)%s\n", colorBold, col, colorReset)
	fmt.Printf("%s  • Active Model: %s%s%s%s\n", colorGray, colorBold, col, name, colorGray)
	fmt.Printf("  • Open https://claude.ai, https://gemini.google.com, or https://grok.com in Chrome\n")
	fmt.Printf("  • Type 'switch' or 'claude' / 'gemini' / 'grok' to change AI model\n")
	fmt.Printf("  • Type 'attach' or 'attach <file>' for Sandbox files\n")
	fmt.Printf("  • Type 'exit' to quit, 'help' for commands%s\n", colorGray)
	fmt.Printf("%s==================================================%s\n\n", col, colorReset)
}

func clearScreen() {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		cmd.Run()
	} else {
		fmt.Print("\033[H\033[2J")
	}
}

func showSpinner(msg string, done chan bool) {
	showSpinnerWithColor(msg, colorClaude, done)
}

func showSpinnerWithColor(msg string, col string, done chan bool) {
	spinnerChars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	for {
		select {
		case <-done:
			fmt.Print("\r\033[K") // Clear line
			return
		default:
			fmt.Printf("\r%s%s %s%s", col, spinnerChars[i%len(spinnerChars)], msg, colorReset)
			time.Sleep(90 * time.Millisecond)
			i++
		}
	}
}
