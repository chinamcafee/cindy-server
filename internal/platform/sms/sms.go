package sms

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/makecindy/cindy-server/internal/platform/config"
)

var ErrNotConfigured = errors.New("sms provider is not configured")

type Sender interface {
	SendVerificationCode(ctx context.Context, phone, code string) error
	Available() bool
	Provider() string
}

func New(cfg *config.Config, logger *slog.Logger) (Sender, error) {
	provider := cfg.SMSProvider
	if provider == "auto" {
		if cfg.AliyunSMSReady() {
			provider = "aliyun"
		} else {
			provider = "log"
		}
	}
	switch provider {
	case "aliyun":
		if !cfg.AliyunSMSReady() {
			return nil, ErrNotConfigured
		}
		return &AliyunSender{
			accessKeyID:   cfg.AliyunSMSAccessKeyID,
			accessSecret:  cfg.AliyunSMSAccessKeySecret,
			signName:      cfg.AliyunSMSSignName,
			templateCode:  cfg.AliyunSMSTemplateCode,
			regionID:      cfg.AliyunSMSRegionID,
			endpoint:      strings.TrimRight(cfg.AliyunSMSEndpoint, "/"),
			templateParam: cfg.AliyunSMSTemplateParam,
			client:        &http.Client{Timeout: 10 * time.Second},
		}, nil
	case "log":
		return &logSender{logger: logger, dev: cfg.Env != "prod"}, nil
	default:
		return nil, fmt.Errorf("unsupported sms provider %q", provider)
	}
}

type logSender struct {
	logger *slog.Logger
	dev    bool
}

func (s *logSender) SendVerificationCode(_ context.Context, phone, code string) error {
	if !s.dev {
		return ErrNotConfigured
	}
	s.logger.Info("sms_code_dev", "phone", MaskPhone(phone), "code", code)
	return nil
}

func (s *logSender) Available() bool { return s.dev }

func (s *logSender) Provider() string { return "log" }

type AliyunSender struct {
	accessKeyID   string
	accessSecret  string
	signName      string
	templateCode  string
	regionID      string
	endpoint      string
	templateParam string
	client        *http.Client
}

func (s *AliyunSender) Available() bool { return true }

func (s *AliyunSender) Provider() string { return "aliyun" }

func (s *AliyunSender) SendVerificationCode(ctx context.Context, phone, code string) error {
	params := map[string]string{
		"AccessKeyId":      s.accessKeyID,
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     phone,
		"RegionId":         s.regionID,
		"SignName":         s.signName,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   uuid.NewString(),
		"SignatureVersion": "1.0",
		"TemplateCode":     s.templateCode,
		"TemplateParam":    fmt.Sprintf(`{"%s":"%s"}`, s.templateParam, code),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2017-05-25",
	}
	signedURL := s.signedURL(params)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("aliyun sms request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("aliyun sms read response: %w", err)
	}
	var result struct {
		Code      string `json:"Code"`
		Message   string `json:"Message"`
		RequestID string `json:"RequestId"`
		BizID     string `json:"BizId"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("aliyun sms invalid response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("aliyun sms HTTP %d: %s (%s)", resp.StatusCode, result.Code, result.Message)
	}
	if result.Code != "OK" {
		return fmt.Errorf("aliyun sms %s: %s (requestId %s)", result.Code, result.Message, result.RequestID)
	}
	return nil
}

func (s *AliyunSender) signedURL(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, percentEncode(key)+"="+percentEncode(params[key]))
	}
	canonical := strings.Join(parts, "&")
	stringToSign := "GET&%2F&" + percentEncode(canonical)
	mac := hmac.New(sha1.New, []byte(s.accessSecret+"&"))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return s.endpoint + "/?" + canonical + "&Signature=" + percentEncode(signature)
}

func percentEncode(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", c))
	}
	return b.String()
}

func MaskPhone(phone string) string {
	if len(phone) <= 4 {
		return "***"
	}
	return strings.Repeat("*", len(phone)-4) + phone[len(phone)-4:]
}
