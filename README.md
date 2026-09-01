# 🤖 Claude, Gemini & Grok AI Terminal & Web Assistant (Zero API Key)

[![Go Version](https://img.shields.io/badge/Go-1.20+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Chrome Extension](https://img.shields.io/badge/Chrome%20Extension-Manifest%20V3-4285F4?style=flat&logo=googlechrome)](https://developer.chrome.com/docs/extensions/mv3/)
[![Supported Models](https://img.shields.io/badge/Models-Claude%20%7C%20Gemini%20%7C%20Grok-8A2BE2?style=flat)](https://github.com)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Send and receive messages with **Anthropic Claude**, **Google Gemini**, and **xAI Grok** directly from your **Terminal (Go App)** and your **Browser** without requiring any paid API keys or subscriptions. Switch dynamically between models on the fly with a single command or click!

---

## 🌟 Key Features

- 🔑 **Zero API Keys Required**: Uses your active browser session directly via a local WebSocket bridge.
- 🔀 **Multi-Model Support**: Seamlessly switch between **Claude** (`claude.ai`), **Gemini** (`gemini.google.com`), and **Grok** (`grok.com` / `x.com/i/grok`).
- ⚡ **Real-Time Full-Duplex WebSockets**: Instant bidirectional prompt delivery and response streaming directly to your console.
- 💾 **Automated Sandbox & Silent Code Runner**:
  - Automatically parses JSON file payloads and saves them to `Sandbox/`.
  - Silently executes batch scripts (`.bat`, `.cmd`), PowerShell (`.ps1`), Python (`.py`), Shell scripts (`.sh`), and opens web apps (`.html`).
  - Automatically pipes stdout/stderr back into the AI context for automated debugging and iterative refinement.
- 📎 **Smart File & Directory Attachments**:
  - Attach individual files (`attach script.py`) or whole folder listings (`attach`).
  - Supports binary & text file encoding with accurate MIME type resolution.
- 🖥️ **Dual Interface**:
  - **Terminal CLI**: Interactive colored prompts with dynamic model colors and single-shot execution mode.
  - **Chrome Extension**: Clean Popup & Chrome Side Panel with tab health monitoring and one-click model tabs.

---

## 🏗️ Architecture

```
┌────────────────────────────────────────────────────────┐
│                   Terminal (Go CLI)                    │
│  - Interactive Shell / Single-Shot Execution           │
│  - Silent Script Runner (.bat, .ps1, .py, .sh, .html)  │
│  - Embedded HTTP & WebSocket Bridge (127.0.0.1:5005)   │
└──────────────────────────┬─────────────────────────────┘
                           │
                 WebSocket (ws://)
                           │
┌──────────────────────────▼─────────────────────────────┐
│                 Chrome Browser Engine                  │
│  ┌──────────────────────────────────────────────────┐  │
│  │              Content Script (content.js)         │  │
│  │  - DOM Injection (ProseMirror, Rich-Textarea)    │  │
│  │  - Real-time MutationObserver stream scraper     │  │
│  └──────────────────────────────────────────────────┘  │
│       ▲                     ▲                    ▲     │
│       │                     │                    │     │
│  ┌────┴───────┐     ┌───────┴──────┐     ┌───────┴───┐ │
│  │  Claude    │     │    Gemini    │     │   Grok    │ │
│  │ (claude.ai)│     │(gemini.google│     │ (grok.com │ │
│  │            │     │    .com)     │     │ /x.com)   │ │
│  └────────────┘     └──────────────┘     └───────────┘ │
└────────────────────────────────────────────────────────┘
```

---

## 🚀 Quick Start

### 1. Load the Chrome Extension
1. Open Google Chrome and navigate to `chrome://extensions`.
2. Enable **Developer mode** (toggle in the top-right corner).
3. Click **Load unpacked** and select this directory: `d:\Github\Claude_Extension`.
4. Open the web interface for the AI model you wish to use:
   - **Claude**: [https://claude.ai](https://claude.ai)
   - **Gemini**: [https://gemini.google.com](https://gemini.google.com)
   - **Grok**: [https://grok.com](https://grok.com) or [https://x.com/i/grok](https://x.com/i/grok)

---

### 2. Run the Terminal App

#### Option A: Run directly with Go
```bash
# Interactive Chat Mode (Default: Claude):
go run .

# Start directly in Grok mode:
go run . -model=grok

# Start directly in Gemini mode:
go run . -model=gemini

# Single-shot prompt with Grok:
go run . -model=grok "Explain quantum computing in simple terms"

# Single-shot prompt with Claude:
go run . "Explain how goroutines and channels work in Go"

# Single-shot prompt with Gemini:
go run . -model=gemini "Compare Rust ownership with Go garbage collection"
```

#### Option B: Build & Run Native Executable
```bash
go build -o claude.exe .
.\claude.exe
```

---

## 🔀 Switching Between AI Models

You can switch between models anytime both in the Terminal CLI and the Extension UI:

### 1. In the Terminal CLI
- `switch` or `toggle` — Cycles through `Claude` ➔ `Gemini` ➔ `Grok` ➔ `Claude`
- `grok` or `switch grok` — Switch active model directly to **xAI Grok**
- `gemini` or `switch gemini` — Switch active model directly to **Google Gemini**
- `claude` or `switch claude` — Switch active model directly to **Claude**
- `model` — Display current active AI model and details

The prompt dynamic styling updates immediately:
- `You [Claude] > ` (Terracotta)
- `You [Gemini] > ` (Google Blue)
- `You [Grok] > ` (Electric Cyan/Blue)

### 2. In the Chrome Extension Popup / Side Panel
- Click the **Claude**, **Gemini**, or **Grok** tab in the header.
- Status badges dynamically monitor tab availability for each model.

---

## 💻 Interactive Terminal Commands

Inside interactive chat mode:

| Command | Description |
| :--- | :--- |
| `<prompt>` | Send prompt to the active AI model |
| `switch` / `toggle` | Cycle between Claude, Gemini, and Grok |
| `claude` / `gemini` / `grok` | Switch directly to the specified AI model |
| `model` / `status` | Show currently active model and details |
| `attach` | List all files in `Sandbox/` and send to the AI |
| `attach <file>` | Attach and send content of a specific file from `Sandbox/` |
| `attach <file> <prompt>` | Attach file with custom instruction (e.g. `attach main.go review this`) |
| `ls` / `dir` / `tree` | View directory tree and sizes of files in `Sandbox/` |
| `clear` / `cls` | Clear the terminal screen |
| `help` | Show interactive commands cheatsheet |
| `exit` / `quit` | Exit the CLI |

---

## 💾 Automatic File Generation & Silent Runner

When Claude, Gemini, or Grok outputs JSON formatted with file operations:

```json
{
  "action": "run",
  "files": [
    {
      "filename": "build.bat",
      "code": "@echo off\necho Compiling project assets...\ntimeout /t 1 >nul\necho Build completed successfully!"
    }
  ]
}
```

The CLI automatically:
1. **Auto-saves** files to `Sandbox/<filename>`.
2. **Silently executes** the action in the background (no intrusive console popups on Windows).
3. **Streams stdout/stderr** directly in the console with formatted timestamps.
4. **Logs** progress to `Sandbox/execution.log`.
5. **Feeds execution results** back to the AI for continuous refinement and automated bug fixing.

---

## 🧪 Running Unit & Integration Tests

Run the full test suite with Go:

```bash
$env:GOCACHE="d:\Github\Claude_Extension\.gocache"; go test -v ./...
```

The test suite covers:
- File extraction and recursive payload generation (`TestExtractAndSaveFiles`)
- Action parsing and command resolution (`TestExtractActions`)
- Silent background runner execution (`TestExecuteActionSilently_BatchScript`)
- End-to-End action feedback loop (`TestExtractAndSaveFiles_WithActionRun`)
- Nested directory execution (`TestNestedDirectoryActionExecution`)
- MIME type mapping and Base64 packaging (`TestGetMimeType`, `TestCreateFilePayload`)
- Sandbox attach keyword resolver (`TestResolveSandboxAttach`)
- Multi-model management for Claude, Gemini, and Grok (`TestModelManagement_GrokClaudeGemini`)

---

## 📁 Repository Structure

```
Claude_Extension/
├── background.js       # Manifest V3 service worker for tab routing & state
├── content.js          # Injected content script for Claude, Gemini & Grok DOM automation
├── popup.html          # Extension popup & Chrome Side Panel markup
├── popup.js            # Popup/Side Panel logic & tab synchronization
├── popup.css           # Modern theme styling with model-specific palettes
├── manifest.json       # Chrome extension Manifest V3 definition
├── main.go             # Go CLI application & embedded WebSocket/HTTP bridge
├── main_test.go        # Unit & integration test suites
├── exec_windows.go     # Windows silent background process execution
├── exec_other.go       # POSIX process execution fallback
├── claude.ps1          # PowerShell CLI interface script
├── bridge.ps1          # Standalone PowerShell bridge runner
├── Sandbox/            # Local directory for auto-saved and executed files
└── .gitignore          # Git exclusion rules for builds, logs & cache
```

---

## 📄 License

This project is licensed under the [MIT License](LICENSE).
