param(
    [string]$DesignSystemRepo = "dzarlax/design-system",
    [string]$DesignSystemRef = "latest",
    [string]$VisNetworkVersion = "9.1.9",
    [string]$VisTimelineVersion = "7.7.3"
)

$ErrorActionPreference = "Stop"

node (Join-Path $PSScriptRoot "fetch-viz-assets.mjs") `
    "--design-system-repo=$DesignSystemRepo" `
    "--design-system-ref=$DesignSystemRef" `
    "--vis-network-version=$VisNetworkVersion" `
    "--vis-timeline-version=$VisTimelineVersion"

if ($LASTEXITCODE -ne 0) {
    throw "fetch-viz-assets.mjs failed"
}
