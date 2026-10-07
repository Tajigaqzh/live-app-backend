param(
    [Parameter(Mandatory = $true)]
    [string]$InputUrl,
    [string]$OutputDirectory,
    [string]$KeyInfoFile,
    [ValidateRange(100, 50000)]
    [int]$Bitrate720Kbps = 2800,
    [ValidateRange(100, 50000)]
    [int]$Bitrate480Kbps = 1400
)

$ErrorActionPreference = 'Stop'
if ($Bitrate720Kbps -le $Bitrate480Kbps) {
    throw '720p 码率必须高于 480p 码率'
}
$ffmpeg = Get-Command ffmpeg -ErrorAction Stop

$streamUri = [Uri]$InputUrl
if ($streamUri.Scheme -ne 'rtmp' -or $streamUri.AbsolutePath -cnotmatch '^/live/([A-Za-z0-9_-]+)/([0-9]{4}-[0-9]{2}-[0-9]{2})/([A-Za-z0-9_-]+)/([A-Za-z0-9_-]+)$') {
    throw 'RTMP 地址格式应为 /live/<appName>/<YYYY-MM-DD>/<userId>/<liveId>'
}
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $PSScriptRoot ("data/abr/{0}/{1}/{2}/{3}" -f $Matches[1], $Matches[2], $Matches[3], $Matches[4])
}

$outputRoot = [System.IO.Path]::GetFullPath($OutputDirectory)
foreach ($variant in @('source', '720p', '480p')) {
    New-Item -ItemType Directory -Path (Join-Path $outputRoot "variant_$variant") -Force | Out-Null
}

# FFmpeg accepts forward slashes in Windows paths; they also keep HLS URI handling consistent.
$outputRoot = $outputRoot.Replace('\', '/')
# source 直接复用推流者的默认码率；只为另外两档生成缩放视频。
$filter = '[0:v]split=2[v720in][v480in];' +
    '[v720in]fps=25,scale=w=-2:h=720:flags=lanczos,format=yuv420p[v720];' +
    '[v480in]fps=25,scale=w=-2:h=480:flags=lanczos,format=yuv420p[v480]'

$arguments = @(
    '-hide_banner', '-i', $InputUrl,
    '-filter_complex', $filter,
    '-map', '0:v:0', '-map', '0:a:0',
    '-map', '[v720]', '-map', '0:a:0',
    '-map', '[v480]', '-map', '0:a:0',
    '-c:v:0', 'copy', '-c:v:1', 'libx264', '-c:v:2', 'libx264', '-preset', 'veryfast',
    '-g:v:1', '50', '-keyint_min:v:1', '50', '-sc_threshold:v:1', '0',
    '-force_key_frames:v:1', 'expr:gte(t,n_forced*2)',
    '-g:v:2', '50', '-keyint_min:v:2', '50', '-sc_threshold:v:2', '0',
    '-force_key_frames:v:2', 'expr:gte(t,n_forced*2)',
    '-b:v:1', "${Bitrate720Kbps}k", '-maxrate:v:1', "$([int]($Bitrate720Kbps * 1.1))k", '-bufsize:v:1', "$(2 * $Bitrate720Kbps)k",
    '-b:v:2', "${Bitrate480Kbps}k", '-maxrate:v:2', "$([int]($Bitrate480Kbps * 1.1))k", '-bufsize:v:2', "$(2 * $Bitrate480Kbps)k",
    '-c:a:0', 'copy', '-c:a:1', 'aac', '-c:a:2', 'aac', '-ar:a:1', '48000', '-ar:a:2', '48000', '-ac:a:1', '2', '-ac:a:2', '2',
    '-b:a:1', '96k', '-b:a:2', '96k',
    '-f', 'hls', '-hls_time', '2', '-hls_list_size', '6',
    '-hls_delete_threshold', '3',
    '-hls_flags', 'independent_segments+delete_segments+temp_file',
    '-master_pl_name', 'master.m3u8', '-master_pl_publish_rate', '2',
    '-var_stream_map', 'v:0,a:0,name:source v:1,a:1,name:720p v:2,a:2,name:480p',
    '-hls_segment_filename', "$outputRoot/variant_%v/segment_%06d.ts",
    "$outputRoot/variant_%v/index.m3u8"
)
if ($KeyInfoFile) {
    $arguments = $arguments[0..($arguments.Count - 2)] + @('-hls_key_info_file', $KeyInfoFile, $arguments[-1])
}

Write-Host "Input: $InputUrl"
Write-Host "Master playlist: $outputRoot/master.m3u8"
Write-Host 'Press q in FFmpeg to stop transcoding.'
& $ffmpeg.Source @arguments
exit $LASTEXITCODE
