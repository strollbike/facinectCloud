# Facinect Tournament — PHP → Go Feature Parity Matrix

**Date:** 2026-10-27  
**PHP source of truth:** `/home/dell/Downloads/fc-web/Facinect` (newest Oct 26 tree; strict superset of `07oct26`)  
**Go target:** `services/tournaments` in `facinect-microservices`  
**Clients:** StrollX `lib/services/tournaments_service.dart`, `apps/web` facility + public register

This document is Phase 1–3 of the migration brief. Status means **verified in source**, not aspirational.

Status legend:

| Tag | Meaning |
|-----|---------|
| **Done** | Go implements usable parity for StrollX/web path |
| **Partial** | Endpoint or table exists; rules thinner than PHP |
| **Missing** | Present in PHP; absent or stubbed in Go |
| **N/A** | Not found in PHP (do not invent) |
| **Web-only** | PHP website cards; Flutter may not call |

---

## 1. Legacy PHP inventory (summary)

Full absolute paths are listed in the audit notes below. Counts for primary tree:

| Area | Approx. files |
|------|----------------|
| `api/v2/tournament_*.php` + monolith `tournaments.php` | ~12 |
| `lib/Services/Tournament/*` engines | 24 |
| Supporting libs (expiry, payment options, bulk, ref token, …) | ~10 |
| `cards/*` staff UI/AJAX | ~25 |
| Public registration (`register.php`, `save.php`, …) | ~8 |
| Docs / deploy tests / migrations | ~8 |

**No dedicated tournament cron.** Expiry runs on list load (`TournamentExpiry.php`). Platform `cron.php` is WhatsApp/billing/students only.

---

## 2. Architecture (current Go)

```
StrollX / apps/web
        │
        ▼
Traefik  PathPrefix(/v1/tournaments) → facinect-tournaments:8080
        │
        ├─ JWT (facility claims) or X-Service-Key
        ├─ Public: /public/*, /ref/*
        └─ Postgres (shared identity DB) + EnsureSchema
                │
                └─ Notifications service (WhatsApp templates)
```

**Reuse (do not duplicate):** identity JWT, Traefik, notifications, paygateway (for future online pay), existing `TOURNAMENTS_*` compose service.

**Schema note:** Full tournament tables are created mainly by `EnsureSchema` on boot. Numbered SQL only has `007_tournaments.sql` + `008_tournament_categories.sql` (and `008` collides with enquiries). Versioned migrations for Phase-2 tables are a gap.

**Data model mismatch (critical):**

| PHP | Go |
|-----|-----|
| `registrations` | `tournament_entries` |
| `tour_matches` (+ optional next_match_* columns) | `tournament_fixtures` |
| `tour_score_events` | `tournament_score_events` |
| `tournament_financial_ledger` | Aggregated counts in `Ledger()` only |
| `referees` / `match_referee_assignments` / `court_referee_assignments` | Flattened `tournament_referees` + `ref_id` on fixtures |
| Org-owned (`facility_id` nullable) | Facility-scoped only |

---

## 3. Feature-parity matrix

### 3.1 Tournament lifecycle

| Feature | PHP | Go | Status | Notes / next work |
|---------|-----|----|--------|-------------------|
| List by facility | `tournament_list.php` | `GET .../facilities/{id}/tournaments` | **Done** | |
| Summary / active count | list helpers | `GET .../summary` | **Done** | |
| Create / update | `tournament_save.php` | POST create/update | **Done** | Categories nested on upsert |
| Soft cancel / status | publish + status fields | `POST .../status` | **Done** | StrollX “delete” → cancelled |
| Hard delete + cascade | `delete_tournament.php` | — | **Missing** | Optional; soft-cancel may be enough for Flutter |
| Publish | `tournament_publish.php` | `POST .../publish` | **Partial** | Go sets live/upcoming; PHP also upserts `publish_template`, encrypted tid URL |
| Registration link | encrypted tid → `register.php` | Client builds `/tournaments/register?tourId=` | **Partial** | Web page + public APIs exist on branch; need deploy |
| Template image | `tournament_template.php` | GET/POST/delete template | **Done** | |
| Expiry when date past | `TournamentExpiry` on list | — | **Missing** | Port as list-time or job |
| Org-owned (no facility) | migration 013 | — | **Missing** | Confirm product need before port |
| Onboarding (organiser) | `onboarding.php` + strollusers | — | **Missing** | Separate product surface |

### 3.2 Categories, form, entries

| Feature | PHP | Go | Status | Notes |
|---------|-----|----|--------|-------|
| Categories + fees + team_size | `tournament_categories` | `tournament_categories` | **Done** | |
| Custom form fields | form_settings / design | `tournament_form_fields` JSONB | **Partial** | Stored; public register maps name/phone/gender only |
| List / create entries | `tournament_entries.php` | entries GET/POST | **Done** | |
| Public registration | `register.php` / `save.php` | `/public/.../register` | **Partial** | No full/advance/spot payment path; pending only |
| Toggle entries open/closed | `toggle_entries` | `POST .../entries/toggle` | **Done** | Stage B — `entries_open` column |
| Move registration category | `move_category` | `POST .../entries/move-category` | **Done** | Stage B |
| Bulk CSV import | `TournamentPlayersBulk` | `players/import` | **Partial** | Template ignores tournament-specific fields |
| Spot register (admin) | `spot_register` | `POST .../entries/spot-register` | **Done** | Stage B — one Go entry per player; no participants table |

### 3.3 Payments

| Feature | PHP | Go | Status | Notes |
|---------|-----|----|--------|-------|
| Allow full / advance / spot flags | `TournamentPaymentOptions` | columns + upsert | **Done** | Flags only |
| Advance amount | yes | `advance_amount` | **Done** | |
| MCP payment mode caps | `ClientControls` | — | **Missing** | Platform control |
| Online Razorpay registration | `save.php` + webhook | — | **Missing** | Must use **paygateway**, never trust client paid |
| Payment status update | entries API | `POST .../entries/{id}/payment` | **Done** | Manual status |
| Mark all paid | yes | `entries/mark-all-paid` | **Done** | |
| Coupons | `apply_coupon` | — | **Missing** | StrollX `_notOnGateway` |
| Financial ledger rows | `tournament_financial_ledger` | `GET .../ledger` counts | **Partial** | No per-txn ledger table |
| Receipts / txn refs | save_final / view_register | — | **Missing** | |
| Refunds / cancel registration | view_register flows | status strings only | **Partial** | No refund workflow |

### 3.4 Fixtures & formats

| Feature | PHP | Go | Status | Notes |
|---------|-----|----|--------|-------|
| Round-robin / pools | `PoolDistribution`, RoundRobin | `GenerateFixtures` all-pairs | **Partial** | Ignores pool count, pairing mode from client body |
| Knockout + byes | `KnockoutBracket` | `GenerateKnockout` | **Partial** | Simplified bracket; not matrix-tested vs PHP |
| Preliminary KO mode | FIXTURES_LOGIC.md | — | **Missing** | |
| Pairing: random / seeded / academy_separated | `PairingEngine` | — | **Missing** | |
| Hybrid pools → KO | `hybrid.php`, `AdvancementEngine` | — | **Missing** | |
| Fixture preview | `FixturePreview` | `fixtures/preview` | **Partial** | Metrics thinner than PHP |
| Generate next round | `generate_next_round` | — | **Missing** | StrollX `_notOnGateway` |
| Bracket lock after results | `BracketLock` | reset with force? | **Partial** | PHP HTTP 423; verify Go reset rules |
| Progression into next slot | `ProgressionEngine` | score update may set winner | **Partial** | No `next_match_id` chain like PHP |
| Walkover | yes | `fixtures/{id}/walkover` | **Done** | |
| Reset matches | yes | `fixtures/reset` | **Done** | |
| Scheduling / courts board | `SchedulingEngine`, `CourtsBoard` | — | **Missing** | StrollX `courtsBoard` `_notOnGateway` |
| Standings / ranking / tie-break | `StandingsEngine`, `RankingEngine` | — | **Missing** | |
| Sport-specific score adapters | `ScoreAdapters`, `SportRuleProvider` | integer score1/score2 | **Partial** | |
| Doubles / team side format | team_size driven | category team_size stored | **Partial** | Fixture sides are name strings |

### 3.5 Live scoring & public boards

| Feature | PHP | Go | Status | Notes |
|---------|-----|----|--------|-------|
| Score update + events | ScoreLiveBus | score + `tournament_score_events` | **Done** | |
| Long-poll score-live | `score_live.php` | `GET .../score-live` | **Partial** | Auth-only; PHP has public variants |
| Public live arena / board | `live_arena`, `public_live_board` | — | **Missing** | |
| Bracket canvas + wait | `bracket_canvas`, `bracket_wait` | — | **Missing** | Web-only PHP |
| Follow match + FCM prefs | `score_follows` | — | **Missing** | |
| Score → push notify | `TournamentScoreNotify` | registration confirm WA only | **Partial** | |

### 3.6 Referees

| Feature | PHP | Go | Status | Notes |
|---------|-----|----|--------|-------|
| Assign / unassign court | referee_manage | assign-court / unassign-court | **Done** | |
| Assign / unassign match | yes | assign-match / unassign-match | **Done** | |
| Ref link token | AES `TournamentRefToken` | `tournament_ref_tokens` | **Partial** | Different token model |
| Public ref score API | `referee.php` | `/ref/matches|score|save|history` | **Done** | |
| Validate referee | yes | always `valid: true` | **Partial** | Stub |
| WhatsApp OTP login | `RefereeWhatsAppOtp` | — | **Missing** | |
| Referee / organiser onboarding | onboarding tables | — | **Missing** | |
| Owner history | yes | `referees/owner-history` | **Done** | |

### 3.7 Notifications & WhatsApp

| Feature | PHP | Go | Status | Notes |
|---------|-----|----|--------|-------|
| Notify contacts | `notify_contacts` | `notify-contacts` | **Done** | Via notifications service |
| Broadcast WhatsApp | `broadcast_whatsapp` | — | **Missing** | StrollX `_notOnGateway` |
| Registration confirmation WA | save paths | paid create → notify | **Partial** | |
| Template language / Meta | facility WA | notifications service | **Done** (platform) | Outside tournaments package |

### 3.8 Web / Flutter surfaces

| Feature | PHP | Go / cloud | Status | Notes |
|---------|-----|------------|--------|-------|
| Facility portal list UI | cards + Flutter | `apps/web/.../tournaments` | **Partial** | Phase-1 UI |
| Public register page | `register.php` | `apps/web/tournaments/register` | **Partial** | Unmerged branch; needs VPS rebuild |
| StrollX gateway coverage | api/v2 | most Phase-1/2 wired | **Partial** | See `_notOnGateway` list below |

**StrollX still `_notOnGateway`:**  
`courtsBoard`, `generateNextRound`, `applyCoupon`, `savePaymentOptions` (flags often via upsert), `broadcastWhatsapp`.  
**Stage B wired to gateway:** `toggleEntries`, `moveRegistrationCategory`, `spotRegister`.

---

## 4. PHP → Go endpoint map (priority for StrollX)

| PHP action | Go route | Parity |
|------------|----------|--------|
| list / details / create / update | facility + tournament CRUD | Done |
| publish / registration_link | publish + client URL | Partial |
| entries list / payment / mark_all_paid | entries + payment | Done |
| form_fields | form-fields | Done |
| generate_fixtures | POST fixtures | Partial (RR) |
| generate_knockout | POST fixtures/knockout | Partial |
| preview | fixtures/preview | Partial |
| update_score / save_match | fixtures/{id}/score | Done |
| walkover / reset | walkover / reset | Done |
| template upload | template | Done |
| ledger | ledger | Partial |
| players_template / bulk_import | players-template / import | Partial |
| notify_contacts | notify-contacts | Done |
| ref assign / ref link / ref score | referees + /ref/* | Mostly Done |
| toggle_entries | — | Missing |
| move_category | — | Missing |
| generate_next_round | — | Missing |
| courts_board | — | Missing |
| apply_coupon / spot_register / broadcast_whatsapp | — | Missing |
| save.php online pay | paygateway + webhook | Missing |
| standings / hybrid / pairing / preliminary | engines | Missing |

---

## 5. Proposed implementation stages (small, testable)

Do **not** claim parity until stage tests pass against PHP rules / fixtures matrix.

| Stage | Scope | Exit criteria |
|-------|--------|---------------|
| **A** | Ship public register (web + public APIs) already coded | PR merged; VPS `tournaments`+`web` rebuilt; copy-link opens form |
| **B** | StrollX missing admin entry ops: toggle entries, move category, spot register | Gateway routes + Dart wired; tests |
| **C** | Fixture engine parity: pools, pairing modes, preliminary KO, next round, bracket lock, progression (`next_match_*`) | Port PHP `deploy/test_knockout_matrix.php` cases to Go tests (3,4,15,17,19,43…) |
| **D** | Hybrid + standings + ranking/tie-break | Compare standings for same sample inputs as PHP |
| **E** | Courts board + scheduling | Matches PHP CourtsBoard conflicts/up-next |
| **F** | Payments: paygateway orders + idempotent webhook → entry paid; ledger rows; coupons if still required | Never trust client `paid` |
| **G** | Public live board / score follows / score push | Only if product still needs PHP spectator surfaces |
| **H** | Referee OTP + validate + onboarding | After F/G priority confirmed |
| **I** | Schema: numbered migrations for all EnsureSchema tables; dual-write/cutover plan if MySQL PHP still live | Documented rollback |

---

## 6. Ambiguities (ask before changing behavior)

1. **Is production still writing PHP MySQL (`tour_matches` / `registrations`) in parallel with Go Postgres?** Cutover strategy depends on this.
2. **Must org-owned tournaments (null facility) be supported in Go?** PHP has it; Go is facility-only.
3. **Online Razorpay for public registration — still required**, or is spot/manual payment enough for cloud cutover?
4. **Spectator PHP boards (bracket_canvas, live_arena) — still in product roadmap** for cloud web?
5. **Hard delete** vs soft cancel — Flutter uses status cancel today.
6. **Dual legacy tables** (`tournament_matches` vs `tour_matches`) — confirm only `tour_matches` is authoritative.

---

## 7. Commands (current repo)

```bash
# Unit tests (Docker; host may lack Go)
docker run --rm -v "$PWD/services/tournaments:/src" -w /src golang:1.22-alpine \
  sh -c 'apk add --no-cache git >/dev/null && go mod tidy && go test ./... -count=1'

# Local compose
docker compose up -d --build tournaments web

# Prod VPS (after merge)
sudo docker compose -f docker-compose.prod.yml up -d --build tournaments web
```

---

## 8. Implemented / partial / pending (roll-up)

| Bucket | Items |
|--------|--------|
| **Implemented (usable)** | Facility CRUD, status, categories, entries CRUD, payment status, mark-all-paid, RR generate (basic), KO generate (basic), score, walkover, reset, preview (basic), template, publish (basic), form-fields, notify-contacts, ledger counts, referee court/match assign, ref-link + public ref scoring, score-live (auth), public register APIs + Next page (local/unmerged) |
| **Partial** | Publish/registration link, KO/RR vs PHP engines, form extras storage, payments (flags only), ledger, progression/lock, referee validate/token model, OpenAPI/docs |
| **Pending (PHP has it)** | toggle/move/spot/coupon/broadcast; pools/pairing/preliminary/hybrid/next-round/courts/standings/ranking; Razorpay registration + webhooks; public live/bracket/follows; referee OTP/onboarding; expiry job; org-owned; hard delete; versioned migrations for all tables |

---

## 9. Primary PHP file list (absolute)

See audit inventory: engines under  
`/home/dell/Downloads/fc-web/Facinect/lib/Services/Tournament/`,  
APIs under  
`/home/dell/Downloads/fc-web/Facinect/api/v2/`,  
public flow  
`/home/dell/Downloads/fc-web/Facinect/register.php`, `save.php`,  
cards under  
`/home/dell/Downloads/fc-web/Facinect/cards/`.

---

*Next action after product answers §6: execute Stage A (public register PR), then Stage B/C in order.*
