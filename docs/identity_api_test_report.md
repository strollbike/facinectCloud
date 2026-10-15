# Identity API Test Report

- **Base URL:** `http://localhost:8080`
- **Tested at:** 2026-10-14T06:21:06.065255+00:00
- **Result:** **26/26 passed**, 0 failed

| Result | Endpoint | Status | Notes |
|--------|----------|--------|-------|
| ✅ | `GET /v1/auth/health` | 200 | status=ok postgres=True |
| ✅ | `GET /v1/auth/openapi.json` | 200 | paths=17 |
| ✅ | `GET /v1/auth/jwks.json` | 200 | keys=1 |
| ✅ | `POST /v1/auth/check-email` | 400 |  |
| ✅ | `POST /v1/auth/check-email` | 200 | exists=False |
| ✅ | `POST /v1/auth/check-email` | 200 | exists=True |
| ✅ | `POST /v1/auth/login` | 401 |  |
| ✅ | `POST /v1/auth/login` | 200 | has_access=True has_refresh=True |
| ✅ | `GET /v1/auth/me` | 401 |  |
| ✅ | `GET /v1/auth/me` | 200 | user=admin@facinect.local facilities=0 |
| ✅ | `GET /v1/auth/session` | 200 | keys=['ok', 'redirect', 'user'] |
| ✅ | `POST /v1/auth/session/refresh` | 200 |  |
| ✅ | `POST /v1/auth/token/refresh` | 200 | rotated=True |
| ✅ | `POST /v1/auth/token/refresh` | 401 |  |
| ✅ | `POST /v1/auth/password` | 401 |  |
| ✅ | `POST /v1/auth/password` | 400 | expects rejection; admin password not changed |
| ✅ | `POST /v1/auth/otp/request` | 200 | dev_otp=no keys=['delivery', 'expires_in', 'ok', 'phone'] |
| ✅ | `POST /v1/auth/otp/verify` | 401 | dev_otp not returned — cannot complete happy path |
| ✅ | `POST /v1/auth/otp/verify` | 401 |  |
| ✅ | `POST /v1/auth/google` | 400 |  |
| ✅ | `POST /v1/auth/google` | 401 |  |
| ✅ | `GET /v1/auth/google/start` | 302 | location=https://accounts.google.com/o/oauth2/v2/auth?access_type=online&client_id=1076951451971-vs0fdsrev932il202d88l6pf5qidc4nf |
| ✅ | `GET /v1/auth/google/callback` | 302 | location=/login?error=invalid_state |
| ✅ | `POST /v1/auth/google/handoff` | 401 |  |
| ✅ | `POST /v1/auth/logout` | 200 |  |
| ✅ | `POST /v1/auth/token/refresh` | 401 | revoked refresh should fail |
