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
	WorkspaceSlug = getEnv("WORKSPACE_SLUG", "workspace")
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
	Thumbnail   *EmbedImage  `json:"thumbnail,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Fields      []EmbedField `json:"fields,omitempty"`
}

type EmbedAuthor struct {
	Name    string `json:"name"`
	IconURL string `json:"icon_url"`
}

type EmbedImage struct {
	URL string `json:"url"`
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
		"avatar_url": fmt.Sprintf("%s/img/plane-icon.png", AppURL),
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

	log.Printf("[DEBUG] Event: %s | Action: %s | Payload: %s", event, action, string(body))

	data, _ := p["data"].(map[string]interface{})
	activity, _ := p["activity"].(map[string]interface{})

	// Extract Actor info
	actor, _ := activity["actor"].(map[string]interface{})
	actorName, _ := actor["display_name"].(string)
	actorIcon, _ := actor["avatar"].(string)
	if actorIcon == "" {
		actorIcon, _ = actor["avatar_url"].(string)
	}
	if actorIcon != "" && len(actorIcon) > 0 && actorIcon[0] == '/' {
		actorIcon = fmt.Sprintf("%s%s", AppURL, actorIcon)
	}

	embed := DiscordEmbed{
		Author: &EmbedAuthor{
			Name:    WorkspaceName,
			IconURL: fmt.Sprintf("%s/img/plane-icon.png", AppURL),
		},
		Footer: &EmbedFooter{
			Text: "Plane Bridge",
		},
	}

	if actorName != "" {
		embed.Author.Name = actorName
		if actorIcon != "" {
			embed.Author.IconURL = actorIcon
		}
	}

	handled := false

	if event == "issue" {
		handled = true
		issueID := fmt.Sprintf("%v", data["id"])
		name, _ := data["name"].(string)

		switch action {
		case "created":
			log.Printf("[INFO] Issue Created: %s", name)
			if actorName != "" {
				embed.Author.Name = fmt.Sprintf("%s created an issue", actorName)
			}
			embed.Color = 8184715
			embed.Title = name
			embed.Description = fmt.Sprintf("%v", data["description_stripped"])
			prio, _ := data["priority"].(string)
			embed.Fields = append(embed.Fields, EmbedField{Name: "Priority", Value: priorities[prio], Inline: true})

		case "deleted":
			if actorName != "" {
				embed.Author.Name = fmt.Sprintf("%s deleted an issue", actorName)
			} else {
				embed.Author.Name = "Work item deleted"
			}
			embed.Color = 16415088
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

			field := fmt.Sprintf("%v", activity["field"])

			// Whitelist
			allowed := map[string]bool{
				"name":           true,
				"priority":       true,
				"state":          true,
				"state_id":       true,
				"assignee_ids":   true,
				"target_date":    true,
				"parent":         true,
				"estimate_point": true,
			}
			if !allowed[field] {
				return
			}

			embed.Color = 4093438
			embed.Title = name

			oldV := fmt.Sprintf("%v", activity["old_value"])
			newV := fmt.Sprintf("%v", activity["new_value"])

			if field == "priority" {
				oldV = priorities[oldV]
				newV = priorities[newV]
			}

			if field == "state_id" || field == "state" {
				field = "State"
				if st, ok := data["state"].(map[string]interface{}); ok {
					newV, _ = st["name"].(string)
				}
				if oldV == "null" || oldV == "<nil>" {
					oldV = "None"
				} else {
					oldV = "Changed"
				}
			}

			if field == "assignee_ids" {
				field = "Assignees"
				assignees, _ := data["assignees"].([]interface{})
				var names []string
				for _, a := range assignees {
					if amap, ok := a.(map[string]interface{}); ok {
						if dname, ok := amap["display_name"].(string); ok {
							names = append(names, dname)
						}
					}
				}
				newV = "None"
				if len(names) > 0 {
					newV = ""
					for i, n := range names {
						if i > 0 {
							newV += ", "
						}
						newV += n
					}
				}
				if oldV == "[]" || oldV == "null" || oldV == "<nil>" {
					oldV = "None"
				} else {
					oldV = "Previously set"
				}

				// If we have assignees, set the first one's avatar as thumbnail
				if len(assignees) > 0 {
					if first, ok := assignees[0].(map[string]interface{}); ok {
						favatar, _ := first["avatar"].(string)
						if favatar == "" {
							favatar, _ = first["avatar_url"].(string)
						}
						if favatar != "" {
							if favatar[0] == '/' {
								favatar = fmt.Sprintf("%s%s", AppURL, favatar)
							}
							embed.Thumbnail = &EmbedImage{URL: favatar}
						}
					}
				}
			}

			embed.Description = fmt.Sprintf("Field **%s** changed.", field)
			embed.Fields = append(embed.Fields, EmbedField{
				Name:  "Change",
				Value: fmt.Sprintf("`%s` → `%s`", oldV, newV),
			})
		default:
			return // Ignore other actions for issues
		}
	} else if event == "issue_comment" {
		handled = true
		embed.Color = 8184715
		if actorName != "" {
			embed.Author.Name = fmt.Sprintf("%s commented", actorName)
		} else {
			embed.Author.Name = "New Comment"
		}
		comment, _ := data["comment_stripped"].(string)
		embed.Description = comment

		issueID := fmt.Sprintf("%v", data["issue"])
		issueName := "Issue Update"
		if issue, ok := data["issue_detail"].(map[string]interface{}); ok {
			if n, ok := issue["name"].(string); ok {
				issueName = n
			}
		}
		embed.Title = issueName
		embed.Fields = append(embed.Fields, EmbedField{Name: "Issue ID", Value: issueID, Inline: true})
	}

	if !handled {
		log.Printf("[INFO] Skipping unhandled event: %s action: %s", event, action)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Double check for "empty" content
	if embed.Title == "" && embed.Description == "" {
		log.Printf("[WARN] Skipping empty embed for event: %s", event)
		w.WriteHeader(http.StatusOK)
		return
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

	// Allow img directory for avatar URL
	http.Handle("/img/", http.StripPrefix("/img/", http.FileServer(http.Dir("img"))))

	log.Printf("[INFO] Server listening on port %s", WebPort)
	log.Fatal(http.ListenAndServe(":"+WebPort, nil))
}
