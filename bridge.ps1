# Claude Terminal Bridge Server
# Listens on http://127.0.0.1:5005 to bridge Terminal CLI with browser Claude session

$port = 5005
$listener = New-Object System.Net.HttpListener
$listener.Prefixes.Add("http://127.0.0.1:$port/")

try {
    $listener.Start()
} catch {
    Write-Host "⚠️ Port $port is already in use or requires admin. Bridge might already be running." -ForegroundColor Yellow
    exit 0
}

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "🚀 Claude Terminal Bridge is running on http://127.0.0.1:$port" -ForegroundColor Green
Write-Host "Keep this window open, or run in background." -ForegroundColor Gray
Write-Host "Make sure https://claude.ai is open in Chrome." -ForegroundColor Yellow
Write-Host "==========================================" -ForegroundColor Cyan

$global:pendingPrompt = $null
$global:pendingFiles = @()
$global:latestResponse = $null
$global:responseReceived = [System.Threading.ManualResetEventSlim]::new($false)

while ($listener.IsListening) {
    try {
        $context = $listener.GetContext()
        $request = $context.Request
        $response = $context.Response

        # Enable CORS
        $response.AddHeader("Access-Control-Allow-Origin", "*")
        $response.AddHeader("Access-Control-Allow-Headers", "Content-Type, Accept")
        $response.AddHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

        if ($request.HttpMethod -eq "OPTIONS") {
            $response.StatusCode = 200
            $response.Close()
            continue
        }

        $path = $request.Url.AbsolutePath
        $reader = New-Object System.IO.StreamReader($request.InputStream, $request.ContentEncoding)
        $body = $reader.ReadToEnd()
        $reader.Close()

        if ($path -eq "/poll" -and $request.HttpMethod -eq "GET") {
            $response.ContentType = "application/json"
            $data = @{ prompt = $global:pendingPrompt; files = $global:pendingFiles }
            if ($null -ne $global:pendingPrompt -or ($global:pendingFiles -and $global:pendingFiles.Count -gt 0)) {
                Write-Host "[Bridge] Extension picked up prompt/files" -ForegroundColor DarkCyan
                $global:pendingPrompt = $null
                $global:pendingFiles = @()
            }
            $json = $data | ConvertTo-Json -Depth 5 -Compress
            $buffer = [System.Text.Encoding]::UTF8.GetBytes($json)
            $response.ContentLength64 = $buffer.Length
            $response.OutputStream.Write($buffer, 0, $buffer.Length)
            $response.Close()
        }
        elseif ($path -eq "/response" -and $request.HttpMethod -eq "POST") {
            $jsonObj = $body | ConvertFrom-Json
            $global:latestResponse = $jsonObj.response
            Write-Host "[Bridge] Received response from Claude.ai ($($global:latestResponse.Length) chars)" -ForegroundColor Green
            $global:responseReceived.Set()

            $response.ContentType = "application/json"
            $respJson = @{ ok = $true } | ConvertTo-Json
            $buffer = [System.Text.Encoding]::UTF8.GetBytes($respJson)
            $response.ContentLength64 = $buffer.Length
            $response.OutputStream.Write($buffer, 0, $buffer.Length)
            $response.Close()
        }
        elseif ($path -eq "/send" -and $request.HttpMethod -eq "POST") {
            $jsonObj = $body | ConvertFrom-Json
            $promptText = $jsonObj.prompt
            $filesList = if ($jsonObj.files) { $jsonObj.files } else { @() }

            Write-Host "`n[Terminal] New prompt: '$promptText' (Files: $($filesList.Count))" -ForegroundColor Magenta
            $global:latestResponse = $null
            $global:responseReceived.Reset()
            $global:pendingPrompt = $promptText
            $global:pendingFiles = $filesList

            # Wait for browser extension to scrape and return response (timeout: 240s)
            $finished = $global:responseReceived.Wait(240000)

            $response.ContentType = "application/json"
            if ($finished -and $null -ne $global:latestResponse) {
                $outData = @{ success = $true; response = $global:latestResponse }
            } else {
                $outData = @{ success = $false; error = "Timeout waiting for Claude.ai response. Make sure claude.ai is open in Chrome." }
            }

            $json = $outData | ConvertTo-Json -Compress
            $buffer = [System.Text.Encoding]::UTF8.GetBytes($json)
            $response.ContentLength64 = $buffer.Length
            $response.OutputStream.Write($buffer, 0, $buffer.Length)
            $response.Close()
        }
        else {
            $response.StatusCode = 200
            $respJson = @{ status = "online" } | ConvertTo-Json
            $buffer = [System.Text.Encoding]::UTF8.GetBytes($respJson)
            $response.ContentLength64 = $buffer.Length
            $response.OutputStream.Write($buffer, 0, $buffer.Length)
            $response.Close()
        }
    }
    catch {
        Write-Host "Error in bridge loop: $_" -ForegroundColor Red
    }
}
