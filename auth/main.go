package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"srs-learn/shared/access"

	"github.com/gin-gonic/gin"
)

type hook struct {
	App    string `json:"app"`
	Stream string `json:"stream"`
	Param  string `json:"param"`
}
type job struct {
	Stream string `json:"stream"`
	Token  string `json:"token,omitempty"`
}
type tokenRequest struct {
	Stream     string `json:"stream"`
	Role       string `json:"role"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

func main() {
	signing := required("TOKEN_SECRET")
	admin := required("ADMIN_SECRET")
	service := required("TRANSCODER_SECRET")
	transcoder := strings.TrimRight(required("TRANSCODER_URL"), "/")
	client := &http.Client{Timeout: 10 * time.Second}
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	router.POST("/api/tokens", func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+admin {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		var request tokenRequest
		if c.ShouldBindJSON(&request) != nil || !access.ValidStream(request.Stream) || (request.Role != "publish" && request.Role != "play") || request.TTLSeconds < 1 || request.TTLSeconds > 86400 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		token, err := access.Sign([]byte(signing), access.Claims{Stream: request.Stream, Role: request.Role, Expires: time.Now().Add(time.Duration(request.TTLSeconds) * time.Second).Unix()})
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token})
	})

	router.POST("/api/srs/hooks/:action", func(c *gin.Context) {
		var event hook
		if c.ShouldBindJSON(&event) != nil {
			reject(c)
			return
		}
		stream := event.App + "/" + event.Stream
		if !access.ValidStream(stream) {
			reject(c)
			return
		}
		switch c.Param("action") {
		case "on_publish", "on_play":
			params, err := url.ParseQuery(strings.TrimPrefix(event.Param, "?"))
			if err != nil {
				reject(c)
				return
			}
			role := "play"
			if c.Param("action") == "on_publish" {
				role = "publish"
			}
			token := params.Get("token")
			if role == "play" && access.Verify([]byte(signing), token, "transcode", stream) == nil {
				break
			}
			if access.Verify([]byte(signing), token, role, stream) != nil {
				reject(c)
				return
			}
			if role == "publish" {
				internal, err := access.Sign([]byte(signing), access.Claims{Stream: stream, Role: "transcode", Expires: time.Now().Add(12 * time.Hour).Unix()})
				if err != nil || callJob(client, transcoder, service, "start", job{Stream: stream, Token: internal}) != nil {
					reject(c)
					return
				}
			}
		case "on_unpublish":
			params, err := url.ParseQuery(strings.TrimPrefix(event.Param, "?"))
			if err != nil || access.Verify([]byte(signing), params.Get("token"), "publish", stream) != nil {
				reject(c)
				return
			}
			if err := callJob(client, transcoder, service, "stop", job{Stream: stream}); err != nil {
				log.Printf("stop job %s: %v", stream, err)
			}
		case "on_stop", "on_hls":
		default:
			reject(c)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0})
	})
	addr := value("AUTH_LISTEN", ":8081")
	log.Printf("auth listening on %s", addr)
	log.Fatal(router.Run(addr))
}

func callJob(client *http.Client, base, secret, action string, payload job) error {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, base+"/jobs/"+action, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("transcoder returned %d: %s", response.StatusCode, body)
	}
	return nil
}
func reject(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"code": 1}) }
func required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
