# Tournaments — test cases (categories + create/update)

Scope: StrollX tournament form create/edit, gateway `/v1/tournaments`, payment flags, members WhatsApp key wiring in prod compose.

## Automated (CI / local)

| ID | Case | Command / check | Expected |
|----|------|-----------------|----------|
| A1 | Category name / fee / team size helpers | `go test ./internal/tournaments/ -run TestCategory` | PASS |
| A2 | Package compile | `go test ./internal/tournaments/ -count=1` | PASS |
| A3 | StrollX analyzer | `flutter analyze lib/services/tournaments_service.dart lib/pages/admin_tournament_form_page.dart` | No errors |

## Manual — happy path

| ID | Steps | Expected |
|----|-------|----------|
| H1 | Create tournament with 2 categories (Open fee 500 size 1; U18 fee 300 size 2) → Save | Toast success; GET details returns both categories with fees/sizes |
| H2 | Reopen Edit | Category fields filled; category `id` present for each |
| H3 | Rename Open → Mens Open, change fee to 600 → Save | Toast success; details show Mens Open / 600; entries previously in `Open` now `Mens Open` |
| H4 | Add third category Doubles → Save | Three categories returned |
| H5 | Change name/date only, leave categories → Save | Metadata updates; categories unchanged |
| H6 | Toggle Advance payment + advance amount → Save | `payment_options.allow_advance_payment=1`, `advance_amount` matches |
| H7 | Publish tournament | Status becomes upcoming/live per API |

## Manual — failure / edge

| ID | Case | Steps | Expected |
|----|------|-------|----------|
| F1 | Empty categories | Remove all category names → continue | Client: “Add at least one category.” No POST |
| F2 | Team size &lt; 1 | Size 0 → continue | Client validation error |
| F3 | Duplicate category names | Two rows named Open → Save | API `duplicate_category` / error toast |
| F4 | Delete category in use | Category has entries/fixtures → remove row → Save | API `category_in_use`; categories unchanged |
| F5 | Delete unused category | Category with no entries → remove → Save | Category removed |
| F6 | Unauthorized | Expired JWT → Save | 401; toast error |
| F7 | Wrong facility | Update another facility’s id | 403 `facility_mismatch` |
| F8 | False success regression | Edit category → Save → reopen | Must show new values (not silent drop) |
| F9 | Gateway stub | Features still on `_notOnGateway` | Clear “not on gateway yet” message |

## Members payment reminder (compose)

| ID | Case | Expected |
|----|------|----------|
| M1 | `docker-compose.prod.yml` members has `NOTIFICATIONS_SERVICE_KEY` | Present |
| M2 | After recreate, `wc -c` of key in members = notifications | Equal (e.g. 64) |
| M3 | Pay Reminder from StrollX | `whatsapp` ≥ 1 or clear Meta error (not `notify_http_401`) |

## VPS verify after deploy

```bash
cd ~/facinect-microservices
git pull   # after PR merge / approved push
sudo docker compose -f docker-compose.prod.yml --env-file .env.prod up -d --build tournaments members
sudo docker exec facinect-members sh -c 'printf %s "$NOTIFICATIONS_SERVICE_KEY" | wc -c'
```

Then StrollX hot restart (`R`) and run H1–H4, F8.
