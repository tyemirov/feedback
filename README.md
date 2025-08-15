# Feedback Service (Go + drop-in web widget)

Tiny service that exposes a floating “Feedback” bubble for any site. Clicking opens a compact email-style form (Subject, optional Email, Message). Submissions are POSTed to your backend and optionally delivered via SMTP.

* Widget: `/widget.js` (no dependencies)
* API: `POST /v1/feedback`
* CORS origin-locked + optional Referer check
* IP rate limiting
* Dockerized

---

## Quick start (Docker Compose)

**docker-compose.yml**

```yaml
version: "3.9"
services:
  feedbacksvc:
    image: local/feedbacksvc:latest
    build: .
    restart: unless-stopped
    environment:
      SERVICE_BIND_ADDRESS: ":8080"
      # Allow only your sites to call POST /v1/feedback
      ALLOWED_ORIGINS: "https://movingmaps.example,https://www.yourdomain.com"
      # Optional: block requests whose Referer lacks any of these substrings
      ALLOWED_REFERER_SUBSTRINGS: "movingmaps.example,yourdomain.com"
      RATE_LIMIT_PER_MINUTE: "20"

      # SMTP is optional; if not set, submissions are logged to stdout
      SMTP_HOST: "smtp.mailprovider.com"
      SMTP_PORT: "587"
      SMTP_USERNAME: "smtp-user"
      SMTP_PASSWORD: "smtp-pass"
      SMTP_FROM: "feedback@yourdomain.com"
      SMTP_TO: "you@yourdomain.com"

      # Widget label (text on the floating button)
      WIDGET_BUTTON_TEXT: "Feedback"
    ports:
      - "8080:8080"
```

Bring it up:

```bash
docker compose up -d --build
```

Health check:

```bash
curl -fsS http://localhost:8080/healthz && echo "ok"
```

---

## Embed on your site

Place this just before `</body>` on **every page** where you want the bubble:

```html
<script
  src="https://feedback.yourdomain.com/widget.js"
  data-feedback-api="https://feedback.yourdomain.com"
  data-site-id="moving-maps-prod"
  data-position="right"
  data-accent="#2563eb"
  defer></script>
```

* `data-feedback-api` must point to your public backend origin.
* `data-site-id` is included in the message (helps when you run multiple sites).
* `data-position`: `right` or `left`.
* `data-accent`: any CSS color.

> The widget is always served at `/widget.js` (fixed path).

---

## API

### Submit feedback

```
POST /v1/feedback
Content-Type: application/json
Origin: <must match one of ALLOWED_ORIGINS>
```

**Request body**

```json
{
  "subject_line": "Issue with pricing page",
  "sender_email": "jane@example.com",
  "message_body": "There is a broken link in the second paragraph.",
  "page_url": "https://movingmaps.example/pricing",
  "website_identifier": "moving-maps-prod",
  "company_name": ""        // honeypot; leave empty
}
```

**Response (success)**

```json
{ "success": true, "message": "ok" }
```

**Response (error examples)**

```json
{ "success": false, "message": "message required" }
{ "success": false, "message": "invalid email" }
{ "success": false, "message": "rate limit exceeded" }
```

---

## Configuration

All configuration is via environment variables.

| Variable                     | Required | Example                                                 | Notes                                                               |
| ---------------------------- | -------- | ------------------------------------------------------- | ------------------------------------------------------------------- |
| `SERVICE_BIND_ADDRESS`       | no       | `:8080`                                                 | Listen address.                                                     |
| `ALLOWED_ORIGINS`            | **yes**  | `https://movingmaps.example,https://www.yourdomain.com` | Exact origins allowed to POST (CORS & server-side enforced).        |
| `ALLOWED_REFERER_SUBSTRINGS` | no       | `movingmaps.example,yourdomain.com`                     | Additional server-side check; any substring match passes.           |
| `RATE_LIMIT_PER_MINUTE`      | no       | `20`                                                    | Per-IP requests/minute.                                             |
| `WIDGET_BUTTON_TEXT`         | no       | `Feedback`                                              | Button label on the floating bubble.                                |
| `SMTP_HOST`                  | no       | `smtp.mailprovider.com`                                 | If unset, messages are logged only.                                 |
| `SMTP_PORT`                  | no       | `587`                                                   | Integer.                                                            |
| `SMTP_USERNAME`              | no       | `smtp-user`                                             | Optional (supports unauthenticated SMTP if both user & pass empty). |
| `SMTP_PASSWORD`              | no       | `smtp-pass`                                             | Optional.                                                           |
| `SMTP_FROM`                  | no       | `feedback@yourdomain.com`                               | Required if SMTP is used.                                           |
| `SMTP_TO`                    | no       | `you@yourdomain.com`                                    | Required if SMTP is used.                                           |

> Minimal working config: set `ALLOWED_ORIGINS`. Everything else can be defaults. For email delivery, also set `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, `SMTP_TO` (and creds if needed).

---

## Reverse proxy (nginx) example

```nginx
server {
  listen 443 ssl http2;
  server_name feedback.yourdomain.com;

  # ssl_certificate ...; ssl_certificate_key ...;

  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_read_timeout 30s;
  }
}
```

Ensure your frontend pages load the widget via `https://feedback.yourdomain.com/widget.js` and `data-feedback-api="https://feedback.yourdomain.com"`.
Set `ALLOWED_ORIGINS="https://movingmaps.example,https://www.yourdomain.com"` (your actual site origins, **not** the feedback service origin).

---

## Local curl test

```bash
curl -i -X POST http://localhost:8080/v1/feedback \
  -H 'Origin: https://movingmaps.example' \
  -H 'Content-Type: application/json' \
  -d '{"subject_line":"Test","sender_email":"you@example.com","message_body":"Hello","page_url":"https://movingmaps.example"}'
```

Expected: `HTTP/1.1 200 OK` and `{"success":true,"message":"ok"}`.

If you see `403 Forbidden`, confirm `ALLOWED_ORIGINS` contains the exact `Origin` value you used in the header.

---

## Behavior & safeguards

* **CORS + server checks**: Origin must match `ALLOWED_ORIGINS`. Optional Referer substring check adds belt-and-suspenders.
* **Rate limiting**: Per-IP, sliding minute window (configurable).
* **Spam trap**: Hidden `company_name` field—bots filling it are accepted as no-ops.
* **Payload limits**: 64 KiB request body; `message_body` max 8000 chars; simple email format validation.

---

## Build from source (optional)

```bash
go build -trimpath -o feedbacksvc ./main.go
./feedbacksvc
```

---

## Embed UI knobs (optional)

* `data-position="left"` to move bubble to bottom-left.
* `data-accent="#0ea5e9"` to change the button color.
* `data-site-id="my-site-key"` to tag submissions by site.

---

## Troubleshooting

* **403**: `Origin` mismatch → fix `ALLOWED_ORIGINS` to include your exact site origin(s) including scheme and host.
* **429**: Too many requests → raise `RATE_LIMIT_PER_MINUTE` if needed.
* **200 but no email**: SMTP not configured → set `SMTP_*` envs; check container logs for “SMTP not configured; logging only”.
* **Behind proxy**: Ensure `X-Forwarded-For` is set so rate limiting uses client IP properly.

---

## Payload schema (for reference)

```json
{
  "subject_line": "string (optional)",
  "sender_email": "string (optional, must look like an email)",
  "message_body": "string (required, <= 8000 chars)",
  "page_url": "string (optional)",
  "website_identifier": "string (optional)",
  "company_name": "string (honeypot; leave empty)"
}
```

---

That’s it—drop the script on your pages, set `ALLOWED_ORIGINS`, and (optionally) wire up SMTP.

