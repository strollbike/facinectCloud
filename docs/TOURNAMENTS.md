# Tournaments service (Phase 1)

Go microservice for **list / create / update** tournament metadata.

Prefix: `/v1/tournaments`  
Auth: identity JWT (`Authorization: Bearer`) and/or `X-Service-Key` (`TOURNAMENTS_SERVICE_KEY`). Local may omit key.

## Local

```bash
docker compose up -d --build tournaments
./scripts/migrate.sh   # applies 007_tournaments.sql (also EnsureSchema on boot)
```

- Health: http://localhost:8080/v1/tournaments/health  
- Docs: http://localhost:8080/docs?spec=tournaments  

## Flow

1. `POST .../tournaments` — create (draft/upcoming/…)  
2. `GET .../tournaments` — list (`status` optional)  
3. `GET/POST .../tournaments/{id}` — details / update  
4. `POST .../tournaments/{id}/status` — status only  
5. `GET .../tournaments/summary` — active count (dashboard)

## Env

| Variable | Purpose |
|----------|---------|
| `TOURNAMENTS_SERVICE_KEY` | Optional local; recommended in production |
| `JWT_PUBLIC_KEY_PATH` | RS256 public key (same as identity) |
| `JWT_ISSUER` / `JWT_AUDIENCE` | Must match identity tokens |

Phase 2: entries, fixtures/KO, live scoring, publish, form fields.

Categories + payment flags persist on create/update (`tournament_categories`, `allow_*_payment`). See `docs/tournaments_test_cases.md`.

No MySQL stored procedures — plain SQL + transactions.
