# 转码服务

这个 Gin 应用部署在 SRS 所在服务器。它为每路直播启动一个 FFmpeg 进程，将推流者的源流直接封装为 `source` 档，并额外转码生成 720p、480p 两档 HLS；验证播放令牌后分发加密的自适应码率 HLS 文件。远端的 `backend/auth` 应用处理 SRS hooks，并在推流开始或结束时调用本服务。两个应用共用的令牌格式位于 `backend/shared`。

## 运行要求

- Go 1.22 或更高版本；在本目录执行 `go mod tidy` 和 `go build -o transcoder-service .`。
- `PATH` 中可以找到 FFmpeg，并能运行本目录下的 `start-abr-hls.sh`。
- 本机能访问 SRS 的 RTMP 地址，鉴权服务器能访问本服务。
- `ABR_ROOT` 必须是私有目录，不能位于 SRS 的 `http_server.dir` 或 `docker/data` 下。

## 配置

| 环境变量 | 用途 | 默认值 |
| --- | --- | --- |
| `TOKEN_SECRET` | 播放令牌和内部转码令牌的 HMAC 密钥，须与鉴权服务一致 | 必填 |
| `TRANSCODER_SECRET` | `/jobs/*` 使用的独立 Bearer 密钥，须与鉴权服务一致 | 必填 |
| `TRANSCODER_LISTEN` | HTTP 监听地址 | `:8090` |
| `SRS_RTMP_URL` | FFmpeg 拉取源流使用的 SRS RTMP 地址 | `rtmp://127.0.0.1` |
| `ABR_ROOT` | 播放列表、切片和密钥的私有输出目录 | `./abr-data` |
| `ABR_SCRIPT` | 转码脚本路径；通常无需设置，覆盖时请使用绝对路径 | 可执行文件同目录的 `start-abr-hls.sh` |
| `ABR_720P_KBPS` | 720p 视频目标码率，单位 kbps | `2800` |
| `ABR_480P_KBPS` | 480p 视频目标码率，单位 kbps | `1400` |

在 SRS 服务器上运行的示例：

```bash
cd backend/transcoder
go mod tidy
go build -o transcoder-service .
# 发布时将 transcoder-service 和 start-abr-hls.sh 放在同一个目录。
TOKEN_SECRET='replace-with-random-token-secret' \
TRANSCODER_SECRET='replace-with-different-random-service-secret' \
ABR_ROOT='/srv/srs/abr-private' \
SRS_RTMP_URL='rtmp://127.0.0.1' \
ABR_720P_KBPS=2800 ABR_480P_KBPS=1400 \
./transcoder-service
```

发布目录只需包含 `transcoder-service` 与 `start-abr-hls.sh`；服务通过 `bash` 执行脚本，因此脚本无需单独设置可执行权限。`docker/start-abr-hls.sh` 保留为兼容旧的手动命令的副本。

鉴权服务的 `TRANSCODER_URL` 应指向本服务的 HTTPS 地址或可信内网地址。将 `/jobs/*` 的网络访问范围限制为鉴权服务器。浏览器只访问 `/hls/*`；当前端与本服务不同源时，需要在反向代理上配置 HTTPS 和 CORS。

## 接口

鉴权服务调用两个任务接口时，都要携带 `Authorization: Bearer <TRANSCODER_SECRET>`：

| 请求 | JSON 请求体 | 返回结果 |
| --- | --- | --- |
| `POST /jobs/start` | `{"stream":"live/app/2026-10-07/user/live001","token":"<internal-transcode-token>"}` | `202` 已启动；`200` 已在运行；`503` 达到并发上限 |
| `POST /jobs/stop` | `{"stream":"live/app/2026-10-07/user/live001"}` | `204` 已请求停止 |

默认最多同时处理四路直播。每路直播写入 `ABR_ROOT/live/<app>/<开始日期>/<用户ID>/<直播ID>/`。FFmpeg 使用内部令牌拉取对应 RTMP 源流，SRS 通过 `on_play` 回调验证该令牌。

`source` 档采用 `-c copy`，保留推流者实际码率和分辨率；只有 720p、480p 需要重新编码。两档码率也可手动作为脚本参数传入：`bash backend/transcoder/start-abr-hls.sh '<RTMP_URL>' '<输出目录>' '<密钥信息文件>' 2800 1400`。PowerShell 对应使用 `-Bitrate720Kbps 2800 -Bitrate480Kbps 1400`。源流需为 HLS 可用的 H.264/AAC，且应在推流端使用约 2 秒的固定关键帧间隔；源流关键帧与转码档位不对齐时，自适应切换可能卡顿。源流分辨率应不低于 720p，否则 720p 档会放大画面。

浏览器应从业务后端取得**完整的**主播放列表地址，例如：

```text
https://srs.example.com:8090/hls/live/app/2026-10-07/user/live001/master.m3u8?token=<play-token>
```

本服务会对每个播放列表、切片和 AES 密钥请求验证播放令牌。返回播放列表时，它会给相对资源地址附加令牌，hls.js 随后会自动请求各档码率。SRS 虽能静态分发 FFmpeg 生成的文件，但 `on_play` 不会保护每个静态 HLS 请求，因此不要通过 SRS 的 `8080` 端口公开 `ABR_ROOT`。

## 当前限制

- 任务状态仅保存在内存中；重启本服务后不会恢复原有 FFmpeg 任务。
- 播放期间令牌过期后，需要获取新的播放地址并重新加载播放器。
- FFmpeg 进程失败后会记录日志并移除任务，目前没有自动重试。
- 每个任务只生成一个 AES 密钥，目前没有密钥轮换和旧输出文件清理机制。
