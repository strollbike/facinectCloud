# StrollX modules on Facinect gateway

After Identity login, these app services use **Identity JWT** + `FACINECT_GATEWAY` (not Hostinger PHP JWT).

| StrollX service | Gateway |
|-----------------|---------|
| Auth | `/v1/auth/*` |
| WhatsApp / phone OTP | `/v1/auth/otp/request`, `/v1/auth/otp/verify` |
| Dashboard | members/students summary + booking list |
| Sports | `GET /v1/administration/facilities/{id}/sports` |
| Members list/enroll/status | `/v1/members/facilities/{id}/members` |
| Students list/enroll/attendance/plans/status | `/v1/students/...` |
| Administration / facility profile | `/v1/administration/...` |
| Enquiry CRUD | `/v1/enquiry/facilities/{id}/enquiries` |
| Enquiry voice | `/v1/ai/enquiry/transcribe` + `/v1/ai/enquiry/analyze` then enquiry create |
| Offers | `/v1/offers/facilities/{id}/offers` |
| Payments ledger | `/v1/payments/facilities/{id}/ledger` |
| Tournaments Phase-1 | `/v1/tournaments/...` list/create/update/status |
| Tournaments Phase-2 | entries, fixtures, score, score-live, template, publish |
| Tournaments Stage E/F | courts-board, entry checkout/confirm-payment via paygateway |
| Tournaments Stage G/H | public live-arena / score-live; ref login / unlock / validate-assignment |
| Referee admin (JWT) | `/v1/tournaments/referees`, `.../referees/assign-court` |
| Score live long-poll | `GET .../score-live?category=&after_id=&wait=` |
| Booking management / View / ledger | `/v1/booking/...` |
| Service notify / booking WhatsApp | `/v1/notifications/...` |
| Onboarding | `/v1/onboarding/*` (platform) |
| News | `/v1/news`, `/v1/news/save`, `/v1/news/inbox` |
| Organisation | `/v1/organisation/profile` |
| Controls sync | `/v1/controls/sync` |
| Privacy | `POST /v1/privacy` |
| Billing | `/v1/billing/entitlement`, catalog, claim_trial, checkout, confirm |

## Env for real providers

| Var | Service | Effect |
|-----|---------|--------|
| `GEMINI_API_KEY` | ai | Real analyze + audio transcribe (else stub) |
| `RAZORPAY_KEY_ID` / `RAZORPAY_KEY_SECRET` | paygateway | Real Razorpay orders (else stub provider) |
| `PAYGATEWAY_URL` | platform | Platform billing → paygateway (default `http://paygateway:8080`) |

## Still Hostinger / browser-only

| Area | Notes |
|------|-------|
| Legal HTML / bracket canvas / assign_referee HTML | Hostinger cards |
| Referee google/email login unlock | Soft stubs (token scoring is on gateway `/v1/tournaments/ref/*`) |

## Local run

```bash
flutter run --dart-define=FACINECT_GATEWAY=http://192.168.1.11:8080
```
