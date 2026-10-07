package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"srs-learn/shared/access"

	"github.com/gin-gonic/gin"
)

type request struct {
	Stream string `json:"stream"`
	Token  string `json:"token"`
}
type manager struct {
	// jobs 由 Gin 请求和等待 FFmpeg 退出的 goroutine 共同访问。
	mu                                    sync.Mutex
	jobs                                  map[string]*exec.Cmd
	limit                                 int
	root, script, srsURL, secret, signing string
	bitrate720, bitrate480                string
}

func main() {
	executable, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	// 默认从程序所在目录加载随服务发布的脚本。
	script := value("ABR_SCRIPT", filepath.Join(filepath.Dir(executable), "start-abr-hls.sh"))
	m := &manager{jobs: make(map[string]*exec.Cmd), limit: 4, root: value("ABR_ROOT", "./abr-data"), script: script, srsURL: value("SRS_RTMP_URL", "rtmp://127.0.0.1"), secret: required("TRANSCODER_SECRET"), signing: required("TOKEN_SECRET"), bitrate720: value("ABR_720P_KBPS", "2800"), bitrate480: value("ABR_480P_KBPS", "1400")}
	os.MkdirAll(m.root, 0750)
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	// 只有远端鉴权服务可以创建或停止转码任务。
	jobs := router.Group("/jobs", m.authorizeJob)
	jobs.POST("/start", m.start)
	jobs.POST("/stop", m.stop)
	router.GET("/hls/*path", m.hls)
	addr := value("TRANSCODER_LISTEN", ":8090")
	log.Printf("transcoder listening on %s", addr)
	log.Fatal(router.Run(addr))
}

func (m *manager) authorizeJob(c *gin.Context) {
	if c.GetHeader("Authorization") != "Bearer "+m.secret {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		c.Abort()
	}
}

func (m *manager) start(c *gin.Context) {
	var req request
	if c.ShouldBindJSON(&req) != nil || !access.ValidStream(req.Stream) || access.Verify([]byte(m.signing), req.Token, "transcode", req.Stream) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	m.mu.Lock()
	// 重复的推流回调复用现有任务；不同直播共享并发上限。
	if _, ok := m.jobs[req.Stream]; ok {
		m.mu.Unlock()
		c.Status(http.StatusOK)
		return
	}
	if len(m.jobs) >= m.limit {
		m.mu.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "transcoder capacity reached"})
		return
	}
	// 每路直播使用独立的输出目录和 AES 密钥。
	dir := filepath.Join(m.root, filepath.FromSlash(req.Stream))
	if err := os.MkdirAll(dir, 0750); err != nil {
		m.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage error"})
		return
	}
	keyInfo, err := writeKeyInfo(dir)
	if err != nil {
		m.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "key error"})
		return
	}
	input := strings.TrimRight(m.srsURL, "/") + "/" + req.Stream + "?token=" + url.QueryEscape(req.Token)
	cmd := exec.Command("bash", m.script, input, dir, keyInfo, m.bitrate720, m.bitrate480)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	m.jobs[req.Stream] = cmd
	m.mu.Unlock()
	go func() {
		// 即使 /jobs/stop 已终止进程，也要等待子进程退出并回收资源。
		err := cmd.Wait()
		if err != nil {
			log.Printf("transcode %s: %v", req.Stream, err)
		}
		m.mu.Lock()
		if m.jobs[req.Stream] == cmd {
			delete(m.jobs, req.Stream)
		}
		m.mu.Unlock()
	}()
	c.Status(http.StatusAccepted)
}

func (m *manager) stop(c *gin.Context) {
	var req request
	if c.ShouldBindJSON(&req) != nil || !access.ValidStream(req.Stream) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	m.mu.Lock()
	cmd := m.jobs[req.Stream]
	delete(m.jobs, req.Stream)
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	c.Status(http.StatusNoContent)
}

func (m *manager) hls(c *gin.Context) {
	relative := strings.TrimPrefix(c.Param("path"), "/")
	parts := strings.Split(relative, "/")
	if len(parts) < 6 || parts[0] != "live" || strings.Contains(relative, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	// 路径的前五段确定直播流；播放列表、切片和密钥请求都必须携带该直播的播放令牌。
	stream := strings.Join(parts[:5], "/")
	token := c.Query("token")
	if access.Verify([]byte(m.signing), token, "play", stream) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	// 限制文件访问范围在 ABR_ROOT 内，防止构造路径越界读取。
	clean := filepath.Clean(filepath.FromSlash(relative))
	root, _ := filepath.Abs(m.root)
	file, _ := filepath.Abs(filepath.Join(m.root, clean))
	if !strings.HasPrefix(file, root+string(os.PathSeparator)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}
	if strings.HasSuffix(file, ".m3u8") {
		servePlaylist(c, file, token)
		return
	}
	if strings.HasSuffix(file, ".key") || strings.HasSuffix(file, ".bin") || strings.HasSuffix(file, ".ts") || strings.HasSuffix(file, ".m4s") || strings.HasSuffix(file, ".mp4") {
		c.File(file)
		return
	}
	c.Status(http.StatusNotFound)
}

func servePlaylist(c *gin.Context, file, token string) {
	f, err := os.Open(file)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// 将 FFmpeg 写入的占位密钥地址替换为鉴权地址，并给相对资源地址附加播放令牌。
		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			line = strings.ReplaceAll(line, "https://invalid.local/key", "../stream.key?token="+url.QueryEscape(token))
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			sep := "?"
			if strings.Contains(line, "?") {
				sep = "&"
			}
			line += sep + "token=" + url.QueryEscape(token)
		}
		fmt.Fprintln(c.Writer, line)
	}
}

func writeKeyInfo(dir string) (string, error) {
	// FFmpeg 的密钥信息文件依次写入密钥 URI、本地密钥路径和 IV，每项占一行。
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	keyFile := filepath.Join(dir, "stream.key")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		return "", err
	}
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	info := filepath.Join(dir, "key.info")
	content := "https://invalid.local/key\n" + keyFile + "\n" + hex.EncodeToString(iv) + "\n"
	if err := os.WriteFile(info, []byte(content), 0600); err != nil {
		return "", err
	}
	return info, nil
}
func required(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatal(errors.New(k + " is required"))
	}
	return v
}
func value(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
