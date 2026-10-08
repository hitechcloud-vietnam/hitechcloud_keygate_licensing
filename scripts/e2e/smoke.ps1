# E2E smoke test against a running HiTechCloud instance (plan §99/§109).
#
# Usage (PowerShell):
#   .\scripts\e2e\smoke.ps1                          # defaults to http://localhost:9000
#   .\scripts\e2e\smoke.ps1 -BaseUrl https://lic.example.com
#
# Checks: /health, /api/v1/config, /api/v1/site-config, setup status,
# marketplace list, and the 404 envelope shape. Exits non-zero when any
# check fails. No third-party modules required.

param(
    [string]$BaseUrl = "http://localhost:9000"
)

$ErrorActionPreference = "Continue"
$script:failed = 0
$script:passed = 0

function Assert-True {
    param([string]$Name, [bool]$Condition, [string]$Detail = "")
    if ($Condition) {
        $script:passed++
        Write-Host "PASS  $Name" -ForegroundColor Green
    } else {
        $script:failed++
        Write-Host "FAIL  $Name  $Detail" -ForegroundColor Red
    }
}

function Get-Json {
    param([string]$Path)
    try {
        $resp = Invoke-RestMethod -Uri "$BaseUrl$Path" -Method Get -TimeoutSec 15
        return @{ Ok = $true; Status = 200; Body = $resp }
    } catch {
        $status = 0
        if ($_.Exception.Response) {
            $status = [int]$_.Exception.Response.StatusCode
        }
        return @{ Ok = $false; Status = $status; Body = $null; Error = $_.Exception.Message }
    }
}

Write-Host "E2E smoke against $BaseUrl"
Write-Host ("-" * 60)

# 1. /health — liveness + DB check.
$h = Get-Json -Path "/health"
Assert-True "GET /health returns 200" ($h.Ok -and $h.Status -eq 200) $h.Error
if ($h.Ok) {
    Assert-True "GET /health status is ok" ($h.Body.status -eq "ok") "got $($h.Body.status)"
    Assert-True "GET /health reports database check" ($null -ne $h.Body.checks) ""
}

# 2. /api/v1/config — public config + attribution (AGPL §7(b) fields).
$c = Get-Json -Path "/api/v1/config"
Assert-True "GET /api/v1/config returns 200" ($c.Ok -and $c.Status -eq 200) $c.Error
if ($c.Ok) {
    Assert-True "config envelope success=true" ($c.Body.success -eq $true) ""
    Assert-True "config carries attribution_text" (-not [string]::IsNullOrEmpty($c.Body.data.attribution_text)) ""
    Assert-True "config carries attribution_url" (-not [string]::IsNullOrEmpty($c.Body.data.attribution_url)) ""
}

# 3. /api/v1/site-config — surface/branding config.
$s = Get-Json -Path "/api/v1/site-config"
Assert-True "GET /api/v1/site-config returns 200" ($s.Ok -and $s.Status -eq 200) $s.Error
if ($s.Ok) {
    Assert-True "site-config envelope success=true" ($s.Body.success -eq $true) ""
}

# 4. Setup status — always answers with the wizard state.
$setup = Get-Json -Path "/api/v1/setup/status"
Assert-True "GET /api/v1/setup/status returns 200" ($setup.Ok -and $setup.Status -eq 200) $setup.Error
if ($setup.Ok) {
    Assert-True "setup status has needed flag" ($null -ne $setup.Body.data.needed) ""
}

# 5. Marketplace list — public catalog answers with the envelope.
$m = Get-Json -Path "/api/v1/marketplace/products?limit=5"
Assert-True "GET /api/v1/marketplace/products returns 200" ($m.Ok -and $m.Status -eq 200) $m.Error
if ($m.Ok) {
    Assert-True "marketplace envelope success=true" ($m.Body.success -eq $true) ""
}

# 6. 404 shape — unknown API path must answer the JSON error envelope,
#    not gin's plain-text 404 (contract: {"success":false,"error":{...}}).
try {
    Invoke-RestMethod -Uri "$BaseUrl/api/v1/definitely-not-a-route-$([guid]::NewGuid())" -Method Get -TimeoutSec 15 | Out-Null
    Assert-True "unknown API path returns 404" $false "request unexpectedly succeeded"
} catch {
    $status = 0
    $body = $null
    if ($_.Exception.Response) {
        $status = [int]$_.Exception.Response.StatusCode
        try {
            $stream = $_.Exception.Response.GetResponseStream()
            if ($stream) {
                $stream.Position = 0
                $reader = New-Object System.IO.StreamReader($stream)
                $raw = $reader.ReadToEnd()
                $reader.Close()
                $body = $raw | ConvertFrom-Json
            }
        } catch { }
    }
    Assert-True "unknown API path returns 404" ($status -eq 404) "got $status"
    Assert-True "404 uses error envelope" ($null -ne $body -and $body.success -eq $false -and $null -ne $body.error) ""
    Assert-True "404 error code is NOT_FOUND" ($null -ne $body -and $body.error.code -eq "NOT_FOUND") ""
}

Write-Host ("-" * 60)
Write-Host "passed: $script:passed  failed: $script:failed"
if ($script:failed -gt 0) {
    exit 1
}
exit 0
