package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// --- Configuration & Globals ---
var (
	WorkspaceName = getEnv("WORKSPACE_NAME", "Workspace")
	WebhookSecret = getEnv("WEBHOOK_SECRET", "")
	DiscordURL    = getEnv("DISCORD_WEBHOOK_URL", "")
	AppURL        = getEnv("APP_URL", "https://plane.so")
	WebPort       = getEnv("WEB_PORT", "8080")
)

var priorities = map[string]string{
	"urgent": "🔴 Urgent!",
	"high":   "🟠 High",
	"medium": "🟡 Medium",
	"low":    "🔵 Low",
	"none":   "⚫ None",
}

// Thread-safe map for spam protection
var (
	lastUpdated = make(map[string]int64)
	mu          sync.Mutex
)

// --- Discord Payload Structures ---
type DiscordEmbed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color"`
	Author      *EmbedAuthor `json:"author,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Fields      []EmbedField `json:"fields,omitempty"`
}

type EmbedAuthor struct {
	Name    string `json:"name"`
	IconURL string `json:"icon_url"`
}

type EmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url"`
}

type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// --- Logic ---

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func verifySignature(body []byte, signature string) bool {
	if WebhookSecret == "" {
		return true
	} // Warning: Security disabled
	h := hmac.New(sha256.New, []byte(WebhookSecret))
	h.Write(body)
	expected := hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func sendToDiscord(embed DiscordEmbed) {
	payload := map[string]interface{}{
		"username":   "Plane",
		"avatar_url": fmt.Sprintf("%s/plane-icon.png", AppURL),
		"embeds":     []DiscordEmbed{embed},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(DiscordURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Error sending to Discord: %v", err)
		return
	}
	defer resp.Body.Close()
}

func webhookHandler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	if !verifySignature(body, r.Header.Get("x-plane-signature")) {
		log.Println("[WARN] Invalid signature")
		http.Error(w, "Invalid signature", http.StatusForbidden)
		return
	}

	var p map[string]interface{}
	json.Unmarshal(body, &p)

	event, _ := p["event"].(string)
	action, _ := p["action"].(string)
	data, _ := p["data"].(map[string]interface{})
	activity, _ := p["activity"].(map[string]interface{})

	embed := DiscordEmbed{
		Author: &EmbedAuthor{
			Name:    fmt.Sprintf("Update in %s", WorkspaceName),
			IconURL: fmt.Sprintf("%s/plane-icon.png", AppURL),
		},
	}

	if event == "issue" {
		issueID := fmt.Sprintf("%v", data["id"])
		name, _ := data["name"].(string)

		switch action {
		case "created":
			log.Printf("[INFO] Issue Created: %s", name)
			embed.Color = 8184715
			embed.Title = name
			embed.Description = fmt.Sprintf("%v", data["description_stripped"])
			prio, _ := data["priority"].(string)
			embed.Fields = append(embed.Fields, EmbedField{Name: "Priority", Value: priorities[prio], Inline: true})

		case "deleted":
			embed.Color = 16415088
			embed.Author.Name = "Work item deleted"
			embed.Description = fmt.Sprintf("ID: `%s`", issueID)

		case "updated":
			// Anti-spam: check if this ID was updated in the last 2 seconds
			mu.Lock()
			now := time.Now().Unix()
			if now < lastUpdated[issueID]+2 {
				mu.Unlock()
				return
			}
			lastUpdated[issueID] = now
			mu.Unlock()

			embed.Color = 4093438
			embed.Title = name
			field := fmt.Sprintf("%v", activity["field"])
			oldV := fmt.Sprintf("%v", activity["old_value"])
			newV := fmt.Sprintf("%v", activity["new_value"])

			// Ignore high-noise fields
			if field == "state_id" || field == "sort_order" {
				return
			}

			embed.Description = fmt.Sprintf("Field **%s** changed.", field)
			embed.Fields = append(embed.Fields, EmbedField{
				Name:  "Change",
				Value: fmt.Sprintf("`%s` → `%s`", oldV, newV),
			})
		}
	}

	if event == "issue_comment" {
		embed.Color = 8184715
		embed.Author.Name = "New Comment"
		comment, _ := data["comment_stripped"].(string)
		embed.Description = comment
		embed.Fields = append(embed.Fields, EmbedField{Name: "Issue ID", Value: fmt.Sprintf("%v", data["issue"]), Inline: true})
	}

	sendToDiscord(embed)
	w.WriteHeader(http.StatusOK)
}

func main() {
	// Health check endpoint for Dokploy/Traefik
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	http.HandleFunc("/", webhookHandler)

	log.Printf("[INFO] Server listening on port %s", WebPort)
	log.Fatal(http.ListenAndServe(":"+WebPort, nil))
}
