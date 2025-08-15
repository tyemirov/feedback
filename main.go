package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ApplicationConfiguration struct {
	ServiceBindAddress         string
	AllowedOriginList          []string
	AllowedRefererSubstrings   []string
	RateLimitPerMinute         int
	SmtpHost                   string
	SmtpPort                   int
	SmtpUsername               string
	SmtpPassword               string
	SmtpFromAddress            string
	SmtpToAddress              string
	WidgetMountPath            string
	WidgetButtonText           string
}

type FeedbackRequestPayload struct {
	SubjectLine             string `json:"subject_line"`
	SenderEmail             string `json:"sender_email"`
	MessageBody             string `json:"message_body"`
	PageUrl                 string `json:"page_url"`
	WebsiteIdentifier       string `json:"website_identifier"`
	SpamTrapCompanyName     string `json:"company_name"` // honeypot, should be empty
}

type FeedbackResponse struct {
	Success                 bool   `json:"success"`
	Message                 string `json:"message"`
}

type ClientRateWindow struct {
	RequestCount            int
	WindowStartTime         time.Time
}

type ApplicationState struct {
	Configuration           ApplicationConfiguration
	RateLimiterLock         sync.Mutex
	RateLimiterByIp         map[string]*ClientRateWindow
}

func main() {
	applicationConfiguration := loadConfigurationFromEnvironment()
	applicationState := &ApplicationState{
		Configuration:   applicationConfiguration,
		RateLimiterByIp: make(map[string]*ClientRateWindow),
	}

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/widget.js", applicationState.handleWidgetScript)
	httpMux.HandleFunc("/v1/feedback", applicationState.handleFeedbackSubmission)
	httpMux.HandleFunc("/healthz", func(httpResponseWriter http.ResponseWriter, httpRequest *http.Request) {
		httpResponseWriter.WriteHeader(http.StatusOK)
		_, _ = httpResponseWriter.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              applicationConfiguration.ServiceBindAddress,
		Handler:           httpMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("feedback service listening on %s", applicationConfiguration.ServiceBindAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func loadConfigurationFromEnvironment() ApplicationConfiguration {
	serviceBindAddress := getEnvWithDefault("SERVICE_BIND_ADDRESS", ":8080")
	allowedOriginCsv := getEnvWithDefault("ALLOWED_ORIGINS", "")
	allowedRefererCsv := getEnvWithDefault("ALLOWED_REFERER_SUBSTRINGS", "")
	rateLimitPerMinute := parseIntWithDefault(getEnvWithDefault("RATE_LIMIT_PER_MINUTE", "20"), 20)

	smtpHost := getEnvWithDefault("SMTP_HOST", "")
	smtpPort := parseIntWithDefault(getEnvWithDefault("SMTP_PORT", "587"), 587)
	smtpUsername := getEnvWithDefault("SMTP_USERNAME", "")
	smtpPassword := getEnvWithDefault("SMTP_PASSWORD", "")
	smtpFromAddress := getEnvWithDefault("SMTP_FROM", "")
	smtpToAddress := getEnvWithDefault("SMTP_TO", "")

	widgetMountPath := getEnvWithDefault("WIDGET_MOUNT_PATH", "/widget.js")
	widgetButtonText := getEnvWithDefault("WIDGET_BUTTON_TEXT", "Feedback")

	allowedOriginList := splitAndClean(allowedOriginCsv)
	allowedRefererSubstrings := splitAndClean(allowedRefererCsv)

	return ApplicationConfiguration{
		ServiceBindAddress:       serviceBindAddress,
		AllowedOriginList:        allowedOriginList,
		AllowedRefererSubstrings: allowedRefererSubstrings,
		RateLimitPerMinute:       rateLimitPerMinute,
		SmtpHost:                 smtpHost,
		SmtpPort:                 smtpPort,
		SmtpUsername:             smtpUsername,
		SmtpPassword:             smtpPassword,
		SmtpFromAddress:          smtpFromAddress,
		SmtpToAddress:            smtpToAddress,
		WidgetMountPath:          widgetMountPath,
		WidgetButtonText:         widgetButtonText,
	}
}

func splitAndClean(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return []string{}
	}
	rawItems := strings.Split(csv, ",")
	cleaned := make([]string, 0, len(rawItems))
	for _, rawItem := range rawItems {
		trimmed := strings.TrimSpace(rawItem)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}

func parseIntWithDefault(raw string, defaultValue int) int {
	parsed, parseErr := strconv.Atoi(strings.TrimSpace(raw))
	if parseErr != nil {
		return defaultValue
	}
	return parsed
}

func getEnvWithDefault(name string, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return defaultValue
	}
	return value
}

func (applicationState *ApplicationState) handleWidgetScript(httpResponseWriter http.ResponseWriter, httpRequest *http.Request) {
	if httpRequest.Method != http.MethodGet {
		httpResponseWriter.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	httpResponseWriter.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	// Small, framework-free widget. Uses dataset attributes on the script tag.
	// No single-letter variables are used intentionally.
	widgetButtonText := applicationState.Configuration.WidgetButtonText
	scriptContent := generateWidgetScript(widgetButtonText)
	_, _ = httpResponseWriter.Write([]byte(scriptContent))
}

func generateWidgetScript(widgetButtonText string) string {
	// Inline minimal CSS and JS to avoid extra requests.
	// The script discovers its own <script> tag to read data attributes.
	return `(function(){
"use strict";
var scriptElements = document.getElementsByTagName("script");
var currentScriptElement = scriptElements[scriptElements.length - 1];
var apiBaseUrl = currentScriptElement.getAttribute("data-feedback-api") || (window.location.origin);
var websiteIdentifier = currentScriptElement.getAttribute("data-site-id") || "";
var bubblePosition = currentScriptElement.getAttribute("data-position") || "right";
var bubbleAccent = currentScriptElement.getAttribute("data-accent") || "#2563eb";
var bubbleZIndex = currentScriptElement.getAttribute("data-zindex") || "2147483000";

var styleElement = document.createElement("style");
styleElement.type = "text/css";
styleElement.textContent =
".feedback-bubble-container{position:fixed;bottom:20px;"+(bubblePosition==="left"?"left:20px;":"right:20px;")+"z-index:"+bubbleZIndex+";font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Cantarell,Noto Sans,sans-serif}"+
".feedback-bubble-button{display:inline-flex;align-items:center;justify-content:center;border-radius:9999px;padding:12px 16px;background:"+bubbleAccent+";color:#fff;border:none;cursor:pointer;box-shadow:0 8px 24px rgba(0,0,0,.15);font-size:14px}"+
".feedback-modal-overlay{position:fixed;inset:0;background:rgba(0,0,0,.4);display:none;z-index:"+bubbleZIndex+"}"+
".feedback-modal{position:fixed;bottom:"+(bubblePosition==="left"?"80px;left:20px;":"80px;right:20px;")+"max-width:360px;width:92vw;background:#fff;border-radius:12px;box-shadow:0 12px 36px rgba(0,0,0,.2);padding:16px;display:none;z-index:"+bubbleZIndex+"}"+
".feedback-modal h3{margin:0 0 8px 0;font-size:16px}"+
".feedback-field{display:flex;flex-direction:column;margin-bottom:10px}"+
".feedback-field label{font-size:12px;margin-bottom:6px;color:#111}"+
".feedback-field input,.feedback-field textarea{border:1px solid #d1d5db;border-radius:8px;padding:10px;font-size:14px;outline:none}"+
".feedback-field textarea{min-height:120px;resize:vertical}"+
".feedback-actions{display:flex;gap:8px;justify-content:flex-end;margin-top:8px}"+
".feedback-button{border-radius:8px;border:1px solid #d1d5db;background:#f8fafc;padding:8px 12px;font-size:14px;cursor:pointer}"+
".feedback-button.primary{background:"+bubbleAccent+";border-color:"+bubbleAccent+";color:#fff}"+
".feedback-banner{font-size:12px;margin-top:8px;display:none}"+
".feedback-banner.ok{color:#065f46}"+
".feedback-banner.err{color:#991b1b}";
document.head.appendChild(styleElement);

var containerElement = document.createElement("div");
containerElement.className = "feedback-bubble-container";

var buttonElement = document.createElement("button");
buttonElement.className = "feedback-bubble-button";
buttonElement.setAttribute("type","button");
buttonElement.textContent = ` + "`" + widgetButtonText + "`" + `;

var overlayElement = document.createElement("div");
overlayElement.className = "feedback-modal-overlay";

var modalElement = document.createElement("div");
modalElement.className = "feedback-modal";

var modalTitleElement = document.createElement("h3");
modalTitleElement.textContent = "Send feedback";

var fieldSubjectElement = document.createElement("div");
fieldSubjectElement.className = "feedback-field";
var labelSubjectElement = document.createElement("label");
labelSubjectElement.textContent = "Subject";
labelSubjectElement.setAttribute("for","feedback-subject-input");
var inputSubjectElement = document.createElement("input");
inputSubjectElement.id = "feedback-subject-input";
inputSubjectElement.placeholder = "Short subject";
fieldSubjectElement.appendChild(labelSubjectElement);
fieldSubjectElement.appendChild(inputSubjectElement);

var fieldEmailElement = document.createElement("div");
fieldEmailElement.className = "feedback-field";
var labelEmailElement = document.createElement("label");
labelEmailElement.textContent = "Your email (optional)";
labelEmailElement.setAttribute("for","feedback-email-input");
var inputEmailElement = document.createElement("input");
inputEmailElement.id = "feedback-email-input";
inputEmailElement.type = "email";
inputEmailElement.placeholder = "name@example.com";
fieldEmailElement.appendChild(labelEmailElement);
fieldEmailElement.appendChild(inputEmailElement);

var fieldMessageElement = document.createElement("div");
fieldMessageElement.className = "feedback-field";
var labelMessageElement = document.createElement("label");
labelMessageElement.textContent = "Message";
labelMessageElement.setAttribute("for","feedback-message-textarea");
var textareaMessageElement = document.createElement("textarea");
textareaMessageElement.id = "feedback-message-textarea";
textareaMessageElement.placeholder = "Share your feedback or ask a question";
fieldMessageElement.appendChild(labelMessageElement);
fieldMessageElement.appendChild(textareaMessageElement);

// Honeypot
var fieldCompanyTrapElement = document.createElement("input");
fieldCompanyTrapElement.type = "text";
fieldCompanyTrapElement.name = "company_name";
fieldCompanyTrapElement.style.display = "none";

var actionRowElement = document.createElement("div");
actionRowElement.className = "feedback-actions";
var cancelButtonElement = document.createElement("button");
cancelButtonElement.className = "feedback-button";
cancelButtonElement.textContent = "Cancel";
cancelButtonElement.type = "button";
var sendButtonElement = document.createElement("button");
sendButtonElement.className = "feedback-button primary";
sendButtonElement.textContent = "Send";
sendButtonElement.type = "button";

var bannerElement = document.createElement("div");
bannerElement.className = "feedback-banner";

actionRowElement.appendChild(cancelButtonElement);
actionRowElement.appendChild(sendButtonElement);

modalElement.appendChild(modalTitleElement);
modalElement.appendChild(fieldSubjectElement);
modalElement.appendChild(fieldEmailElement);
modalElement.appendChild(fieldMessageElement);
modalElement.appendChild(fieldCompanyTrapElement);
modalElement.appendChild(actionRowElement);
modalElement.appendChild(bannerElement);

containerElement.appendChild(buttonElement);
document.body.appendChild(containerElement);
document.body.appendChild(overlayElement);
document.body.appendChild(modalElement);

function openModal(){
overlayElement.style.display = "block";
modalElement.style.display = "block";
inputSubjectElement.focus();
}
function closeModal(){
overlayElement.style.display = "none";
modalElement.style.display = "none";
bannerElement.style.display = "none";
}

buttonElement.addEventListener("click", function(){ openModal(); });
overlayElement.addEventListener("click", function(){ closeModal(); });
cancelButtonElement.addEventListener("click", function(){ closeModal(); });

sendButtonElement.addEventListener("click", function(){
var trimmedSubject = (inputSubjectElement.value || "").trim();
var trimmedEmail = (inputEmailElement.value || "").trim();
var trimmedMessage = (textareaMessageElement.value || "").trim();
var currentPageUrl = window.location.href;

if(trimmedMessage.length === 0){
bannerElement.textContent = "Message is required.";
bannerElement.className = "feedback-banner err";
bannerElement.style.display = "block";
return;
}

var requestPayload = {
subject_line: trimmedSubject,
sender_email: trimmedEmail,
message_body: trimmedMessage,
page_url: currentPageUrl,
website_identifier: websiteIdentifier,
company_name: fieldCompanyTrapElement.value || ""
};

bannerElement.textContent = "Sending...";
bannerElement.className = "feedback-banner";
bannerElement.style.display = "block";

fetch(apiBaseUrl + "/v1/feedback", {
method: "POST",
headers: {"Content-Type":"application/json"},
body: JSON.stringify(requestPayload),
credentials: "omit"
}).then(function(fetchResponse){
if(!fetchResponse.ok){ throw new Error("Request failed with status "+fetchResponse.status); }
return fetchResponse.json();
}).then(function(parsedResponse){
if(parsedResponse && parsedResponse.success){
bannerElement.textContent = "Thank you! Your feedback was sent.";
bannerElement.className = "feedback-banner ok";
bannerElement.style.display = "block";
setTimeout(function(){ closeModal(); inputSubjectElement.value=""; inputEmailElement.value=""; textareaMessageElement.value=""; }, 900);
} else {
throw new Error((parsedResponse && parsedResponse.message) ? parsedResponse.message : "Unknown error");
}
}).catch(function(unexpectedError){
bannerElement.textContent = "Failed to send feedback.";
bannerElement.className = "feedback-banner err";
bannerElement.style.display = "block";
});
});
})();`
}

func (applicationState *ApplicationState) handleFeedbackSubmission(httpResponseWriter http.ResponseWriter, httpRequest *http.Request) {
	if httpRequest.Method == http.MethodOptions {
		applicationState.writeCorsPreflight(httpResponseWriter, httpRequest)
		return
	}
	if httpRequest.Method != http.MethodPost {
		httpResponseWriter.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !applicationState.isOriginAllowed(httpRequest) || !applicationState.isRefererAllowed(httpRequest) {
		httpResponseWriter.WriteHeader(http.StatusForbidden)
		return
	}

	clientIpAddress := extractClientIpAddress(httpRequest)
	if !applicationState.isWithinRateLimit(clientIpAddress) {
		httpResponseWriter.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: false, Message: "rate limit exceeded"})
		return
	}

	httpRequest.Body = http.MaxBytesReader(httpResponseWriter, httpRequest.Body, 64*1024)
	jsonDecoder := json.NewDecoder(httpRequest.Body)
	jsonDecoder.DisallowUnknownFields()
	var feedbackPayload FeedbackRequestPayload
	if err := jsonDecoder.Decode(&feedbackPayload); err != nil {
		httpResponseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: false, Message: "invalid payload"})
		return
	}
	if strings.TrimSpace(feedbackPayload.SpamTrapCompanyName) != "" {
		httpResponseWriter.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: true, Message: "ok"})
		return
	}

	if strings.TrimSpace(feedbackPayload.MessageBody) == "" {
		httpResponseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: false, Message: "message required"})
		return
	}
	if len(feedbackPayload.MessageBody) > 8000 {
		httpResponseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: false, Message: "message too long"})
		return
	}
	if feedbackPayload.SenderEmail != "" && !looksLikeEmailAddress(feedbackPayload.SenderEmail) {
		httpResponseWriter.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: false, Message: "invalid email"})
		return
	}

	userAgentValue := httpRequest.Header.Get("User-Agent")
	nowTime := time.Now().UTC()
	emailSubject := buildEmailSubject(feedbackPayload)
	emailBody := buildEmailBody(feedbackPayload, userAgentValue, clientIpAddress, nowTime)

	sendErr := applicationState.deliverEmailIfConfigured(emailSubject, emailBody)
	if sendErr != nil {
		log.Printf("email delivery error: %v", sendErr)
		httpResponseWriter.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: false, Message: "delivery failed"})
		return
	}

	applicationState.writeCorsOk(httpResponseWriter, httpRequest)
	httpResponseWriter.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(httpResponseWriter).Encode(FeedbackResponse{Success: true, Message: "ok"})
}

func (applicationState *ApplicationState) writeCorsPreflight(httpResponseWriter http.ResponseWriter, httpRequest *http.Request) {
	originHeader := httpRequest.Header.Get("Origin")
	if applicationState.isOriginInAllowlist(originHeader) {
		httpResponseWriter.Header().Set("Access-Control-Allow-Origin", originHeader)
		httpResponseWriter.Header().Set("Vary", "Origin")
		httpResponseWriter.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		httpResponseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
	httpResponseWriter.WriteHeader(http.StatusNoContent)
}

func (applicationState *ApplicationState) writeCorsOk(httpResponseWriter http.ResponseWriter, httpRequest *http.Request) {
	originHeader := httpRequest.Header.Get("Origin")
	if applicationState.isOriginInAllowlist(originHeader) {
		httpResponseWriter.Header().Set("Access-Control-Allow-Origin", originHeader)
		httpResponseWriter.Header().Set("Vary", "Origin")
	}
	httpResponseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func (applicationState *ApplicationState) isOriginAllowed(httpRequest *http.Request) bool {
	originHeader := httpRequest.Header.Get("Origin")
	return applicationState.isOriginInAllowlist(originHeader)
}

func (applicationState *ApplicationState) isRefererAllowed(httpRequest *http.Request) bool {
	if len(applicationState.Configuration.AllowedRefererSubstrings) == 0 {
		return true
	}
	refererHeader := httpRequest.Header.Get("Referer")
	if strings.TrimSpace(refererHeader) == "" {
		return false
	}
	for _, allowedSubstring := range applicationState.Configuration.AllowedRefererSubstrings {
		if strings.Contains(refererHeader, allowedSubstring) {
			return true
		}
	}
	return false
}

func (applicationState *ApplicationState) isOriginInAllowlist(originValue string) bool {
	if strings.TrimSpace(originValue) == "" {
		return false
	}
	for _, allowedOrigin := range applicationState.Configuration.AllowedOriginList {
		if subtle.ConstantTimeCompare([]byte(originValue), []byte(allowedOrigin)) == 1 {
			return true
		}
	}
	return false
}

func extractClientIpAddress(httpRequest *http.Request) string {
	xForwardedForHeader := httpRequest.Header.Get("X-Forwarded-For")
	if xForwardedForHeader != "" {
		parts := strings.Split(xForwardedForHeader, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	hostPart, _, splitErr := net.SplitHostPort(strings.TrimSpace(httpRequest.RemoteAddr))
	if splitErr == nil && hostPart != "" {
		return hostPart
	}
	return "unknown"
}

func (applicationState *ApplicationState) isWithinRateLimit(clientIpAddress string) bool {
	nowTime := time.Now()
	applicationState.RateLimiterLock.Lock()
	defer applicationState.RateLimiterLock.Unlock()

	existing, exists := applicationState.RateLimiterByIp[clientIpAddress]
	if !exists {
		applicationState.RateLimiterByIp[clientIpAddress] = &ClientRateWindow{
			RequestCount:    1,
			WindowStartTime: nowTime,
		}
		return true
	}
	if nowTime.Sub(existing.WindowStartTime) >= time.Minute {
		existing.RequestCount = 1
		existing.WindowStartTime = nowTime
		return true
	}
	if existing.RequestCount >= applicationState.Configuration.RateLimitPerMinute {
		return false
	}
	existing.RequestCount++
	return true
}

func looksLikeEmailAddress(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if !strings.Contains(candidate, "@") {
		return false
	}
	if strings.HasPrefix(candidate, "@") || strings.HasSuffix(candidate, "@") {
		return false
	}
	return true
}

func buildEmailSubject(payload FeedbackRequestPayload) string {
	trimmedSubject := strings.TrimSpace(payload.SubjectLine)
	if trimmedSubject == "" {
		trimmedSubject = "Website feedback"
	}
	if strings.TrimSpace(payload.SenderEmail) != "" {
		return fmt.Sprintf("[Feedback] %s — %s", trimmedSubject, payload.SenderEmail)
	}
	return fmt.Sprintf("[Feedback] %s", trimmedSubject)
}

func buildEmailBody(payload FeedbackRequestPayload, userAgent string, clientIp string, when time.Time) string {
	var buffer bytes.Buffer
	buffer.WriteString("New website feedback\n\n")
	if strings.TrimSpace(payload.SubjectLine) != "" {
		buffer.WriteString("Subject: " + strings.TrimSpace(payload.SubjectLine) + "\n")
	}
	if strings.TrimSpace(payload.SenderEmail) != "" {
		buffer.WriteString("Sender email: " + strings.TrimSpace(payload.SenderEmail) + "\n")
	}
	if strings.TrimSpace(payload.WebsiteIdentifier) != "" {
		buffer.WriteString("Website id: " + strings.TrimSpace(payload.WebsiteIdentifier) + "\n")
	}
	if strings.TrimSpace(payload.PageUrl) != "" {
		buffer.WriteString("Page url: " + strings.TrimSpace(payload.PageUrl) + "\n")
	}
	if strings.TrimSpace(userAgent) != "" {
		buffer.WriteString("User agent: " + strings.TrimSpace(userAgent) + "\n")
	}
	if strings.TrimSpace(clientIp) != "" {
		buffer.WriteString("Client ip: " + strings.TrimSpace(clientIp) + "\n")
	}
	buffer.WriteString("Received at (UTC): " + when.Format(time.RFC3339) + "\n\n")
	buffer.WriteString("Message:\n")
	buffer.WriteString(strings.TrimSpace(payload.MessageBody))
	buffer.WriteString("\n")
	return buffer.String()
}

func (applicationState *ApplicationState) deliverEmailIfConfigured(subject string, body string) error {
	if strings.TrimSpace(applicationState.Configuration.SmtpHost) == "" ||
		strings.TrimSpace(applicationState.Configuration.SmtpToAddress) == "" ||
		strings.TrimSpace(applicationState.Configuration.SmtpFromAddress) == "" {
		log.Printf("SMTP not configured; logging only. Subject=%q BodyFirst80=%q", subject, previewFirstN(body, 80))
		return nil
	}

	emailHeaders := make(map[string]string)
	emailHeaders["From"] = applicationState.Configuration.SmtpFromAddress
	emailHeaders["To"] = applicationState.Configuration.SmtpToAddress
	emailHeaders["Subject"] = subject
	emailHeaders["MIME-Version"] = "1.0"
	emailHeaders["Content-Type"] = "text/plain; charset=UTF-8"
	var rawMessage bytes.Buffer
	for headerName, headerValue := range emailHeaders {
		rawMessage.WriteString(headerName + ": " + headerValue + "\r\n")
	}
	rawMessage.WriteString("\r\n")
	rawMessage.WriteString(body)

	smtpAddress := fmt.Sprintf("%s:%d", applicationState.Configuration.SmtpHost, applicationState.Configuration.SmtpPort)
	var smtpAuth smtp.Auth
	if applicationState.Configuration.SmtpUsername != "" || applicationState.Configuration.SmtpPassword != "" {
		smtpAuth = smtp.PlainAuth("", applicationState.Configuration.SmtpUsername, applicationState.Configuration.SmtpPassword, applicationState.Configuration.SmtpHost)
	}
	return smtp.SendMail(smtpAddress, smtpAuth, applicationState.Configuration.SmtpFromAddress, []string{applicationState.Configuration.SmtpToAddress}, rawMessage.Bytes())
}

func previewFirstN(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

