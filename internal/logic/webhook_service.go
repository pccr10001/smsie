package logic

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/internal/repository"
	"github.com/pccr10001/smsie/pkg/logger"
)

type WebhookService struct {
	repo *repository.WebhookRepository
}

func NewWebhookService(repo *repository.WebhookRepository) *WebhookService {
	return &WebhookService{repo: repo}
}

func (s *WebhookService) Dispatch(sms *model.SMS) {
	webhooks, err := s.repo.FindByICCID(sms.ICCID)
	if err != nil {
		logger.Log.Errorf("Failed to fetch webhooks for ICCID %s: %v", sms.ICCID, err)
		return
	}

	for _, wh := range webhooks {
		go s.sendWebhook(wh, sms)
	}
}

func (s *WebhookService) sendWebhook(wh model.Webhook, sms *model.SMS) {
	// 1. Render Template
	content := sms.Content
	if wh.Template != "" {
		tmpl, err := template.New("msg").Parse(wh.Template)
		if err == nil {
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, sms); err == nil {
				content = buf.String()
			}
		}
	}

	// 2. Format Payload based on Platform
	var payload []byte
	var err error

	switch wh.Platform {
	case "telegram":
		// Auto-encode for Telegram: assumes URL contains chat_id or is handled by receiver.
		// We construct the body: {"text": content} as required by commonly used bots or webhook adapters.
		// If URL is `https://api.telegram.org/bot<token>/sendMessage`, user should append `?chat_id=...` or strictly use JSON.
		body := map[string]interface{}{
			"text":       content,
			"parse_mode": "Markdown",
		}
		if wh.ChannelID != "" {
			body["chat_id"] = wh.ChannelID
		}
		if strings.Contains(wh.URL, "slack.com") {
			body = map[string]interface{}{"text": content}
		}
		payload, err = json.Marshal(body)

	default:
		// Generic JSON
		body := map[string]interface{}{
			"text": content,
			"sms":  sms,
		}
		payload, err = json.Marshal(body)
	}

	if err != nil {
		logger.Log.Errorf("Failed to marshal webhook payload: %v", err)
		return
	}

	// 3. Send Request
	req, err := http.NewRequest("POST", wh.URL, bytes.NewBuffer(payload))
	if err != nil {
		logger.Log.Errorf("Failed to create request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Log.Errorf("Failed to send webhook to %s: %v", wh.URL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logger.Log.Errorf("Webhook %s returned status: %d", wh.URL, resp.StatusCode)
	} else {
		logger.Log.Infof("Webhook sent to %s", wh.URL)
	}
}
