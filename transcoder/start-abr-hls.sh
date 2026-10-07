#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [[ $# -lt 1 || -z ${1:-} ]]; then
    printf 'Usage: %s <rtmp-url-with-token> [output-directory] [key-info-file] [720p-kbps] [480p-kbps]\n' "$0" >&2
    printf 'Path format: rtmp://host/live/<appName>/<YYYY-MM-DD>/<userId>/<liveId>\n' >&2
    exit 64
fi
input_url=$1
stream_path=${input_url#*://}
stream_path=/${stream_path#*/}
stream_path=${stream_path%%\?*}
if [[ $input_url != rtmp://* || ! $stream_path =~ ^/live/([A-Za-z0-9_-]+)/([0-9]{4}-[0-9]{2}-[0-9]{2})/([A-Za-z0-9_-]+)/([A-Za-z0-9_-]+)$ ]]; then
    printf 'Expected RTMP path: /live/<appName>/<YYYY-MM-DD>/<userId>/<liveId>\n' >&2
    exit 1
fi
output_dir=${2:-"$script_dir/abr-data/live/${BASH_REMATCH[1]}/${BASH_REMATCH[2]}/${BASH_REMATCH[3]}/${BASH_REMATCH[4]}"}
key_info_file=${3:-}
bitrate_720=${4:-2800}
bitrate_480=${5:-1400}
if [[ ! $bitrate_720 =~ ^[0-9]+$ || ! $bitrate_480 =~ ^[0-9]+$ ]] || (( bitrate_720 < 100 || bitrate_720 > 50000 || bitrate_480 < 100 || bitrate_480 > 50000 || bitrate_720 <= bitrate_480 )); then
    printf 'Expected 720p and 480p bitrates in kbps: 100..50000, with 720p > 480p.\n' >&2
    exit 64
fi
key_args=()
if [[ -n $key_info_file ]]; then
    key_args=(-hls_key_info_file "$key_info_file")
fi

if ! command -v ffmpeg >/dev/null 2>&1; then
    printf 'ffmpeg is required and must be available in PATH.\n' >&2
    exit 1
fi

mkdir -p -- "$output_dir"/variant_{source,720p,480p}
output_root=$(cd -- "$output_dir" && pwd -P)

# source 直接复用推流者的默认码率；只为另外两档生成缩放视频。
filter='[0:v]split=2[v720in][v480in];'
filter+='[v720in]fps=25,scale=w=-2:h=720:flags=lanczos,format=yuv420p[v720];'
filter+='[v480in]fps=25,scale=w=-2:h=480:flags=lanczos,format=yuv420p[v480]'

printf 'Input: %s\nMaster playlist: %s/master.m3u8\n' "$input_url" "$output_root"
printf 'Press q in FFmpeg to stop transcoding.\n'

# on_publish 返回时，SRS 可能尚未完成源流注册。
sleep 1

exec ffmpeg \
    -hide_banner -i "$input_url" \
    -filter_complex "$filter" \
    -map '0:v:0' -map '0:a:0' \
    -map '[v720]' -map '0:a:0' \
    -map '[v480]' -map '0:a:0' \
    -c:v:0 copy -c:v:1 libx264 -c:v:2 libx264 -preset veryfast \
    -g:v:1 50 -keyint_min:v:1 50 -sc_threshold:v:1 0 \
    -force_key_frames:v:1 'expr:gte(t,n_forced*2)' \
    -g:v:2 50 -keyint_min:v:2 50 -sc_threshold:v:2 0 \
    -force_key_frames:v:2 'expr:gte(t,n_forced*2)' \
    -b:v:1 "${bitrate_720}k" -maxrate:v:1 "$((bitrate_720 * 11 / 10))k" -bufsize:v:1 "$((bitrate_720 * 2))k" \
    -b:v:2 "${bitrate_480}k" -maxrate:v:2 "$((bitrate_480 * 11 / 10))k" -bufsize:v:2 "$((bitrate_480 * 2))k" \
    -c:a:0 copy -c:a:1 aac -c:a:2 aac -ar:a:1 48000 -ar:a:2 48000 -ac:a:1 2 -ac:a:2 2 \
    -b:a:1 96k -b:a:2 96k \
    -f hls -hls_time 2 -hls_list_size 6 \
    -hls_delete_threshold 3 \
    -hls_flags independent_segments+delete_segments+temp_file \
    -master_pl_name master.m3u8 -master_pl_publish_rate 2 \
    -var_stream_map 'v:0,a:0,name:source v:1,a:1,name:720p v:2,a:2,name:480p' \
    -hls_segment_filename "$output_root/variant_%v/segment_%06d.ts" \
    "${key_args[@]}" \
    "$output_root/variant_%v/index.m3u8"
