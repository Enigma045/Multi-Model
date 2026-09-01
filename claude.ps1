param(
    [Parameter(Position=0, ValueFromRemainingArguments=$true)]
    [string]$Prompt
)

$bridgeUrl = "http://127.0.0.1:5005"
$sandboxDir = Join-Path $PSScriptRoot "Sandbox"
$nl = [Environment]::NewLine

# Helper to recursively get relative file paths
function Get-SandboxRelativeFiles($baseDir) {
    if (-not (Test-Path $baseDir)) { return @() }
    $items = @(Get-ChildItem -Path $baseDir -Recurse -File | Where-Object { $_.Name -ne "file_list.txt" -and $_.FullName -notmatch '[\\/]\.git[\\/]' })
    $res = @()
    foreach ($item in $items) {
        $rel = $item.FullName.Substring($baseDir.Length).TrimStart('/\') -replace '\\', '/'
        $res += $rel
    }
    return $res
}

function Get-MimeType($filename) {
    $ext = [System.IO.Path]::GetExtension($filename).ToLower()
    switch ($ext) {
        ".txt" { return "text/plain" }
        ".log" { return "text/plain" }
        ".md" { return "text/markdown" }
        ".html" { return "text/html" }
        ".htm" { return "text/html" }
        ".css" { return "text/css" }
        ".js" { return "application/javascript" }
        ".json" { return "application/json" }
        ".png" { return "image/png" }
        ".jpg" { return "image/jpeg" }
        ".jpeg" { return "image/jpeg" }
        ".gif" { return "image/gif" }
        ".svg" { return "image/svg+xml" }
        ".pdf" { return "application/pdf" }
        ".csv" { return "text/csv" }
        default { return "text/plain" }
    }
}

function Create-FilePayload($cleanName, $targetPath) {
    if (-not (Test-Path $targetPath)) { return $null }
    $bytes = [System.IO.File]::ReadAllBytes($targetPath)
    $b64 = [Convert]::ToBase64String($bytes)
    return @{
        name = [System.IO.Path]::GetFileName($cleanName)
        content = $b64
        encoding = "base64"
        type = Get-MimeType $cleanName
    }
}

# Resolve attach keyword for Sandbox directory
function Resolve-SandboxAttach($text) {
    if ($text -notmatch '^\s*attach(?:\s+(.*))?$') {
        return @{ prompt = $text; files = @() }
    }
    if (-not (Test-Path $sandboxDir)) { New-Item -ItemType Directory -Path $sandboxDir -Force | Out-Null }
    $arg = $Matches[1]

    if (-not $arg) {
        $files = Get-SandboxRelativeFiles $sandboxDir
        $list = if ($files.Count -gt 0) { $files -join $nl } else { "(No files in Sandbox)" }
        $listPath = Join-Path $sandboxDir "file_list.txt"
        $list | Set-Content $listPath -Encoding utf8
        Write-Host ("[Attached Sandbox file list (" + $files.Count + " files across all directories)]") -ForegroundColor DarkCyan
        $fp = Create-FilePayload "file_list.txt" $listPath
        return @{ prompt = "Here is the list of all files in my Sandbox directory:"; files = @($fp) }
    }

    $file = Join-Path $sandboxDir $arg
    if (Test-Path $file -PathType Container) {
        $dirFiles = Get-SandboxRelativeFiles $file
        if ($dirFiles.Count -gt 0 -and $dirFiles.Count -le 10) {
            $fps = @()
            foreach ($df in $dirFiles) {
                $dfTarget = Join-Path $sandboxDir $df
                $fp = Create-FilePayload $df $dfTarget
                if ($fp) { $fps += $fp }
            }
            Write-Host ("[Attaching " + $fps.Count + " files from Sandbox\" + $arg + "]") -ForegroundColor DarkCyan
            return @{ prompt = ""; files = $fps }
        }
        $list = if ($dirFiles.Count -gt 0) { $dirFiles -join $nl } else { "(No files in Sandbox/" + $arg + ")" }
        Write-Host ("[Attached directory listing for Sandbox\" + $arg + " (" + $dirFiles.Count + " files)]") -ForegroundColor DarkCyan
        return @{ prompt = "Here is the list of files in Sandbox/" + $arg + ":" + $nl + $nl + $list; files = @() }
    }

    if (Test-Path $file -PathType Leaf) {
        $fp = Create-FilePayload $arg $file
        Write-Host ("[Attaching Sandbox\" + $arg + "]") -ForegroundColor DarkCyan
        return @{ prompt = ""; files = @($fp) }
    }

    $parts = $arg -split '\s+', 2
    $fileFirst = Join-Path $sandboxDir $parts[0]
    if (Test-Path $fileFirst -PathType Leaf) {
        $fp = Create-FilePayload $parts[0] $fileFirst
        Write-Host ("[Attaching Sandbox\" + $parts[0] + "]") -ForegroundColor DarkCyan
        $extra = if ($parts.Length -gt 1) { $parts[1] } else { "" }
        return @{ prompt = $extra; files = @($fp) }
    }

    Write-Host ("Error: File or directory '" + $arg + "' not found in Sandbox") -ForegroundColor Red
    return $null
}

# Save a single extracted file to disk
function Save-SingleFile($filename, $code) {
    $cleanName = $filename.TrimStart('/\')
    $targetPath = if ($cleanName -like "Sandbox*") { Join-Path $PSScriptRoot $cleanName } else { Join-Path $sandboxDir $cleanName }
    $dir = Split-Path $targetPath -Parent
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
    [System.IO.File]::WriteAllText($targetPath, $code, [System.Text.Encoding]::UTF8)
    Write-Host ("[Auto-saved: " + $targetPath + " (" + $code.Length + " bytes)]") -ForegroundColor Green
}

# Execute an action silently while streaming progress
function Execute-ActionSilently($actionTarget, $actionCommand = $null) {
    $displayLabel = if ($actionTarget) { $actionTarget } else { $actionCommand }
    if (-not $displayLabel) { return }

    $startTime = Get-Date
    $targetPath = if ($actionTarget) {
        $clean = $actionTarget.TrimStart('/\')
        if ($clean -like "Sandbox*") { Join-Path $PSScriptRoot $clean } else { Join-Path $sandboxDir $clean }
    } else { $null }

    if (-not (Test-Path $sandboxDir)) { New-Item -ItemType Directory -Path $sandboxDir -Force | Out-Null }
    $logPath = Join-Path $sandboxDir "execution.log"

    $logHeader = "`n============================================================`n[" + ($startTime.ToString("yyyy-MM-dd HH:mm:ss")) + "] EXECUTION STARTED: " + $displayLabel + "`n============================================================`n"
    Add-Content -Path $logPath -Value $logHeader -Encoding utf8

    Write-Host ("`n⚡ Executing Action: " + $displayLabel + " (Silent Background Runner)") -ForegroundColor Cyan
    Write-Host ("📂 Directory: Sandbox") -ForegroundColor DarkGray
    Write-Host ("──────────────────────────────────────────────────────────") -ForegroundColor DarkGray

    $pinfo = New-Object System.Diagnostics.ProcessStartInfo
    $pinfo.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden
    $pinfo.CreateNoWindow = $true
    $pinfo.UseShellExecute = $false
    $pinfo.RedirectStandardOutput = $true
    $pinfo.RedirectStandardError = $true
    $targetDir = $sandboxDir
    if ($targetPath -and (Test-Path $targetPath)) {
        $absTarget = (Resolve-Path $targetPath).Path
        $targetDir = [System.IO.Path]::GetDirectoryName($absTarget)
        $targetBase = [System.IO.Path]::GetFileName($absTarget)
        $pinfo.WorkingDirectory = $targetDir

        $ext = [System.IO.Path]::GetExtension($absTarget).ToLower()
        if ($ext -eq ".bat" -or $ext -eq ".cmd") {
            $pinfo.FileName = "cmd.exe"
            $pinfo.Arguments = "/c `"" + $targetBase + "`""
        } elseif ($ext -eq ".ps1") {
            $pinfo.FileName = "powershell.exe"
            $pinfo.Arguments = "-NoProfile -ExecutionPolicy Bypass -File `"" + $targetBase + "`""
        } elseif ($ext -eq ".py") {
            $pinfo.FileName = "python"
            $pinfo.Arguments = "`"" + $targetBase + "`""
        } elseif ($ext -eq ".html" -or $ext -eq ".htm") {
            $pinfo.FileName = "cmd.exe"
            $pinfo.Arguments = "/c start `"`" `"" + $targetBase + "`""
        } else {
            $pinfo.FileName = $absTarget
        }
    } elseif ($actionCommand) {
        $pinfo.FileName = "cmd.exe"
        $pinfo.Arguments = "/c " + $actionCommand
        $pinfo.WorkingDirectory = $sandboxDir
    } else {
        Write-Host ("✖ Cannot execute action: target not found (" + $displayLabel + ")") -ForegroundColor Red
        return
    }

    try {
        $process = New-Object System.Diagnostics.Process
        $process.StartInfo = $pinfo
        $process.Start() | Out-Null

        while (-not $process.HasExited) {
            while (-not $process.StandardOutput.EndOfStream) {
                $line = $process.StandardOutput.ReadLine()
                $elapsed = (Get-Date) - $startTime
                $ts = "[" + ("{0:d2}:{1:d2}" -f [int]$elapsed.TotalMinutes, $elapsed.Seconds) + "]"
                Write-Host ("  " + $ts + " ") -NoNewline -ForegroundColor DarkGray
                Write-Host "stdout > " -NoNewline -ForegroundColor DarkCyan
                Write-Host $line
                Add-Content -Path $logPath -Value ($ts + " stdout: " + $line) -Encoding utf8
            }
            while (-not $process.StandardError.EndOfStream) {
                $line = $process.StandardError.ReadLine()
                $elapsed = (Get-Date) - $startTime
                $ts = "[" + ("{0:d2}:{1:d2}" -f [int]$elapsed.TotalMinutes, $elapsed.Seconds) + "]"
                Write-Host ("  " + $ts + " ") -NoNewline -ForegroundColor DarkGray
                Write-Host "stderr > " -NoNewline -ForegroundColor Yellow
                Write-Host $line
                Add-Content -Path $logPath -Value ($ts + " stderr: " + $line) -Encoding utf8
            }
            Start-Sleep -Milliseconds 40
        }

        # Flush remaining lines
        while (-not $process.StandardOutput.EndOfStream) {
            $line = $process.StandardOutput.ReadLine()
            $elapsed = (Get-Date) - $startTime
            $ts = "[" + ("{0:d2}:{1:d2}" -f [int]$elapsed.TotalMinutes, $elapsed.Seconds) + "]"
            Write-Host ("  " + $ts + " ") -NoNewline -ForegroundColor DarkGray
            Write-Host "stdout > " -NoNewline -ForegroundColor DarkCyan
            Write-Host $line
            Add-Content -Path $logPath -Value ($ts + " stdout: " + $line) -Encoding utf8
        }
        while (-not $process.StandardError.EndOfStream) {
            $line = $process.StandardError.ReadLine()
            $elapsed = (Get-Date) - $startTime
            $ts = "[" + ("{0:d2}:{1:d2}" -f [int]$elapsed.TotalMinutes, $elapsed.Seconds) + "]"
            Write-Host ("  " + $ts + " ") -NoNewline -ForegroundColor DarkGray
            Write-Host "stderr > " -NoNewline -ForegroundColor Yellow
            Write-Host $line
            Add-Content -Path $logPath -Value ($ts + " stderr: " + $line) -Encoding utf8
        }

        $exitCode = $process.ExitCode
        $duration = ((Get-Date) - $startTime).TotalSeconds

        Write-Host ("──────────────────────────────────────────────────────────") -ForegroundColor DarkGray
        if ($exitCode -eq 0) {
            Write-Host ("✔ Action completed successfully (Exit Code 0, Elapsed: " + ("{0:N2}" -f $duration) + "s)") -ForegroundColor Green
            Add-Content -Path $logPath -Value ("[" + ((Get-Date).ToString("yyyy-MM-dd HH:mm:ss")) + "] EXECUTION COMPLETED: Success (Exit Code 0)") -Encoding utf8
        } else {
            Write-Host ("✖ Action failed (Exit Code " + $exitCode + ", Elapsed: " + ("{0:N2}" -f $duration) + "s)") -ForegroundColor Red
            Add-Content -Path $logPath -Value ("[" + ((Get-Date).ToString("yyyy-MM-dd HH:mm:ss")) + "] EXECUTION FAILED: (Exit Code " + $exitCode + ")") -Encoding utf8
        }
        Write-Host ("📝 Progress and output recorded to Sandbox/execution.log`n") -ForegroundColor DarkGray
    } catch {
        Write-Host ("✖ Failed to start action: " + $_.Exception.Message) -ForegroundColor Red
    }
}

# Extract and save JSON files pattern from response
function Extract-AndSaveFiles($text) {
    if (-not $text) { return }

    $savedFiles = @()
    $actionsToRun = @()

    # 1. Try standard JSON parse
    $pattern = '(?s)\{\s*"(?:files|action|actions)"\s*:[\s\S]*'
    if ($text -match $pattern) {
        $sub = $Matches[0]
        $lastBrace = $sub.LastIndexOf('}')
        if ($lastBrace -gt 0) {
            $jsonCandidate = $sub.Substring(0, $lastBrace + 1)
            try {
                $json = $jsonCandidate | ConvertFrom-Json
                if ($json.files) {
                    foreach ($f in $json.files) {
                        if ($f.filename -and ($null -ne $f.code)) {
                            Save-SingleFile $f.filename $f.code
                            $savedFiles += $f.filename
                            if ($f.action -eq "run" -or $f.run -eq $true) {
                                $actionsToRun += @{ target = $f.filename }
                            }
                        }
                    }
                }
                if ($json.action) {
                    if ($json.action -is [string]) {
                        if ($json.action -eq "run") {
                            $target = if ($json.target) { $json.target } elseif ($savedFiles.Count -gt 0) { $savedFiles[0] } else { $null }
                            $actionsToRun += @{ target = $target; command = $json.command }
                        } elseif ($json.action -like "run *") {
                            $actionsToRun += @{ target = $json.action.Substring(4).Trim() }
                        } else {
                            $actionsToRun += @{ target = $json.action }
                        }
                    }
                }
                foreach ($act in $actionsToRun) {
                    Execute-ActionSilently $act.target $act.command
                }
                if ($savedFiles.Count -gt 0 -or $actionsToRun.Count -gt 0) {
                    return
                }
            } catch {}
        }
    }

    # 2. Resilient fallback: regex search for filename and code fields
    $fnMatches = [regex]::Matches($text, '"filename"\s*:\s*["'']([^"'']+)["'']')
    for ($i = 0; $i -lt $fnMatches.Count; $i++) {
        $m = $fnMatches[$i]
        $filename = $m.Groups[1].Value
        $subText = $text.Substring($m.Index + $m.Length)
        if ($subText -match '"code"\s*:\s*["'']') {
            $codeStart = $Matches[0].Length + $subText.IndexOf($Matches[0])
            $remaining = $subText.Substring($codeStart)
            $limit = $remaining.Length
            if ($i + 1 -lt $fnMatches.Count) {
                $nextIdx = $fnMatches[$i + 1].Index - ($m.Index + $m.Length + $codeStart)
                if ($nextIdx -gt 0 -and $nextIdx -lt $limit) {
                    $limit = $nextIdx
                }
            }
            $seg = $remaining.Substring(0, $limit)
            $lastQuote = $seg.LastIndexOfAny(@('"', "'"))
            $code = if ($lastQuote -gt 0) { $seg.Substring(0, $lastQuote) } else { $seg }
            if ($code.Contains('\n') -or $code.Contains('\"')) {
                $code = $code.Replace('\n', "`n").Replace('\r', "`r").Replace('\t', "`t").Replace('\"', '"').Replace('\\', '\')
            }
            Save-SingleFile $filename $code
            $savedFiles += $filename
        }
    }

    if ($text -match '"action"\s*:\s*["'']([^"'']+)["'']') {
        $actVal = $Matches[1]
        if ($actVal -eq "run" -and $savedFiles.Count -gt 0) {
            Execute-ActionSilently $savedFiles[0] $null
        } elseif ($actVal -like "run *") {
            Execute-ActionSilently ($actVal.Substring(4).Trim()) $null
        } else {
            Execute-ActionSilently $actVal $null
        }
    }
}

$attachStopwords = @{
    "a" = $true; "an" = $true; "the" = $true; "this" = $true; "that" = $true; "these" = $true; "those" = $true;
    "it" = $true; "its" = $true; "file" = $true; "files" = $true; "directory" = $true; "directories" = $true;
    "folder" = $true; "folders" = $true; "path" = $true; "paths" = $true; "to" = $true; "for" = $true;
    "and" = $true; "or" = $true; "in" = $true; "on" = $true; "with" = $true; "from" = $true; "your" = $true;
    "my" = $true; "our" = $true; "all" = $true; "any" = $true; "some" = $true; "each" = $true; "every" = $true;
    "one" = $true; "two" = $true; "new" = $true; "old" = $true; "here" = $true; "there" = $true; "please" = $true;
    "me" = $true; "us" = $true; "them" = $true; "him" = $true; "her" = $true; "is" = $true; "are" = $true;
    "was" = $true; "were" = $true; "be" = $true; "been" = $true; "being" = $true; "have" = $true; "has" = $true;
    "had" = $true; "do" = $true; "does" = $true; "did" = $true; "will" = $true; "would" = $true; "should" = $true;
    "can" = $true; "could" = $true; "may" = $true; "might" = $true; "must" = $true;
}

# Handle attach commands requested by Claude
function Handle-ClaudeAttachRequests($text, $depth = 0) {
    if ($depth -ge 5 -or -not $text) { return }
    $pattern = '(?i)(?:^|[\r\n\s"`''])attach\s+["'`]?([a-zA-Z0-9_\-./\\]+)["'`]?'
    $matches = [regex]::Matches($text, $pattern)
    $filesToAttach = @()
    $missingFiles = @()
    $seen = @{}

    foreach ($m in $matches) {
        $relPath = $m.Groups[1].Value.Trim(" `t`r`n`"'.,;:")
        $lower = $relPath.ToLower()
        if ($relPath -and -not $seen[$relPath] -and -not $global:attachStopwords.ContainsKey($lower)) {
            $seen[$relPath] = $true
            $cleanName = $relPath.TrimStart('/\')
            $target = if ($cleanName -like "Sandbox*") { Join-Path $PSScriptRoot $cleanName } else { Join-Path $sandboxDir $cleanName }

            if (Test-Path $target -PathType Container) {
                $dirFiles = Get-SandboxRelativeFiles $target
                if ($dirFiles.Count -gt 0 -and $dirFiles.Count -le 10) {
                    foreach ($df in $dirFiles) {
                        $dfTarget = Join-Path $sandboxDir $df
                        $fp = Create-FilePayload $df $dfTarget
                        if ($fp) { $filesToAttach += $fp }
                    }
                    Write-Host ("[Claude requested directory: " + $relPath + " -> Auto-attaching " + $filesToAttach.Count + " files directly...]") -ForegroundColor DarkCyan
                } else {
                    $list = if ($dirFiles.Count -gt 0) { $dirFiles -join $nl } else { "(No files in Sandbox/" + $cleanName + ")" }
                    Write-Host ("[Claude requested directory: " + $relPath + " -> Sending file list (" + $dirFiles.Count + " files)...]") -ForegroundColor DarkCyan
                    $missingFiles += "List of files in Sandbox/" + $cleanName + ":" + $nl + $nl + $list
                }
            }
            elseif (Test-Path $target -PathType Leaf) {
                $fp = Create-FilePayload $cleanName $target
                if ($fp) {
                    Write-Host ("[Claude requested: " + $relPath + " -> Attaching exact file " + $cleanName + "...]") -ForegroundColor DarkCyan
                    $filesToAttach += $fp
                }
            } elseif ($relPath.Contains('.') -or $relPath.Contains('/') -or $relPath.Contains('\')) {
                Write-Host ("Warning: Claude requested '" + $relPath + "' but it was not found in Sandbox.") -ForegroundColor Yellow
                $missingFiles += $relPath
            }
        }
    }

    if ($filesToAttach.Count -gt 0) {
        Send-ClaudePrompt "" $filesToAttach ($depth + 1)
        return
    }

    if ($missingFiles.Count -gt 0) {
        $notice = "Notice: Requested file(s) [" + ($missingFiles -join ", ") + "] not found in Sandbox directory."
        Send-ClaudePrompt $notice @() ($depth + 1)
    }
}

# Function to check if bridge is running
function Test-Bridge {
    try {
        $resp = Invoke-RestMethod -Uri ($bridgeUrl + "/status") -Method GET -TimeoutSec 2 -ErrorAction Stop
        return ($resp.status -eq "online")
    } catch {
        return $false
    }
}

# Function to send prompt and receive response
function Send-ClaudePrompt($text, $files = @(), $depth = 0) {
    if (-not (Test-Bridge)) {
        Write-Host "Warning: Claude Bridge is not running!" -ForegroundColor Red
        Write-Host "Please start the bridge or claude.exe in another terminal." -ForegroundColor Yellow
        Write-Host "And make sure https://claude.ai is open in Chrome." -ForegroundColor Yellow
        return
    }

    if ($files -and $files.Count -gt 0) {
        Write-Host ("Sending " + $files.Count + " attached file(s) to Claude AI...") -ForegroundColor DarkGray
    } else {
        Write-Host "Sending to Claude AI..." -ForegroundColor DarkGray
    }

    $bodyMap = @{ prompt = $text }
    if ($files -and $files.Count -gt 0) {
        $bodyMap["files"] = $files
    }
    $body = $bodyMap | ConvertTo-Json -Depth 5

    try {
        $resp = Invoke-RestMethod -Uri ($bridgeUrl + "/send") -Method POST -Body $body -ContentType "application/json; charset=utf-8" -TimeoutSec 245
        if ($resp.success) {
            Write-Host ($nl + "Claude:") -ForegroundColor Cyan -NoNewline
            Write-Host (" " + $resp.response + $nl)
            Extract-AndSaveFiles $resp.response
            Handle-ClaudeAttachRequests $resp.response $depth
        } else {
            Write-Host ("Error: " + $resp.error) -ForegroundColor Red
        }
    } catch {
        Write-Host ("Failed to receive response: " + $_) -ForegroundColor Red
    }
}

# Run CLI if executed directly
if ($MyInvocation.InvocationName -ne '.') {
    if ($Prompt -and $Prompt.Trim().Length -gt 0) {
        $resolved = Resolve-SandboxAttach $Prompt.Trim()
        if ($resolved) { Send-ClaudePrompt $resolved.prompt $resolved.files }
        exit 0
    }

    # Interactive CLI Chat Mode
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "Claude AI Terminal Chat (Zero API Key)" -ForegroundColor Green
    Write-Host "Type your message and press Enter." -ForegroundColor Gray
    Write-Host "Type 'attach' or 'attach filename' for Sandbox files." -ForegroundColor DarkCyan
    Write-Host "Type 'exit' or 'quit' to end chat." -ForegroundColor Gray
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""

    while ($true) {
        Write-Host "You > " -ForegroundColor Yellow -NoNewline
        $inputLine = [Console]::ReadLine()

        if ($null -eq $inputLine) { break }
        $trimmed = $inputLine.Trim()

        if ($trimmed -in @("exit", "quit", "q", ":q")) {
            Write-Host "Goodbye!" -ForegroundColor Gray
            break
        }

        if ($trimmed.Length -gt 0) {
            $resolved = Resolve-SandboxAttach $trimmed
            if ($resolved) { Send-ClaudePrompt $resolved.prompt $resolved.files }
        }
    }
}
