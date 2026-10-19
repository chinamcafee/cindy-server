package sms

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/makecindy/cindy-server/internal/platform/config"
)

func testConfig(endpoint string) *config.Config {
	return &config.Config{
		Env:                      "dev",
		SMSProvider:              "aliyun",
		AliyunSMSAccessKeyID:     "test-ak",
		AliyunSMSAccessKeySecret: "test-secret",
		AliyunSMSSignName:        "Cindy",
		AliyunSMSTemplateCode:    "SMS_123456",
		AliyunSMSRegionID:        "cn-hangzhou",
		AliyunSMSEndpoint:        endpoint,
		AliyunSMSTemplateParam:   "code",
	}
}

func TestAliyunSendVerificationCode(t *testing.T) {
	var query map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = map[string]string{}
		for key, values := range r.URL.Query() {
			query[key] = values[0]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Code":"OK","Message":"OK","RequestId":"req-1","BizId":"biz-1"}`)
	}))
	defer server.Close()

	sender, err := New(testConfig(server.URL), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.SendVerificationCode(context.Background(), "+8613800000000", "654321"); err != nil {
		t.Fatal(err)
	}
	if query["Action"] != "SendSms" || query["PhoneNumbers"] != "+8613800000000" {
		t.Fatalf("unexpected params: %+v", query)
	}
	if query["SignName"] != "Cindy" || query["TemplateCode"] != "SMS_123456" {
		t.Fatalf("unexpected template params: %+v", query)
	}
	if !strings.Contains(query["TemplateParam"], `"code":"654321"`) {
		t.Fatalf("template param missing code: %s", query["TemplateParam"])
	}
	if query["Signature"] == "" || query["SignatureNonce"] == "" {
		t.Fatalf("missing signature fields: %+v", query)
	}
	if strings.Contains(query["Signature"], "test-secret") {
		t.Fatal("secret leaked into signature")
	}
}

func TestAliyunErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Code":"isv.BUSINESS_LIMIT_CONTROL","Message":"触发分钟级流控","RequestId":"req-2"}`)
	}))
	defer server.Close()
	sender, err := New(testConfig(server.URL), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.SendVerificationCode(context.Background(), "+8613800000000", "123456"); err == nil {
		t.Fatal("expected provider error")
	}
}

func TestLogSenderDisabledInProduction(t *testing.T) {
	cfg := &config.Config{Env: "prod", SMSProvider: "log"}
	sender, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if sender.Available() {
		t.Fatal("log sender must be unavailable in production")
	}
	if err := sender.SendVerificationCode(context.Background(), "+8613800000000", "123456"); err == nil {
		t.Fatal("expected not-configured error")
	}
}

func TestPercentEncoding(t *testing.T) {
	if got := percentEncode("a b*c~d/e"); got != "a%20b%2Ac~d%2Fe" {
		t.Fatalf("unexpected encoding: %s", got)
	}
}
