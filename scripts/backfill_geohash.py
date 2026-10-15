#!/usr/bin/env python3
"""Backfill geohash / geohash5 on app_facilities, app_areas, app_cities from lat/lon.

Matches Services/Facinect/lib/Geohash.php encode (precision 6 + 5).
Usage (inside compose network or via docker exec):

  python3 scripts/backfill_geohash.py \\
    --host 127.0.0.1 --port 5432 --db fac_identity --user identity --password ...
"""
from __future__ import annotations

import argparse
import sys

try:
    import psycopg2
except ImportError:
    print("psycopg2 required: pip install psycopg2-binary", file=sys.stderr)
    sys.exit(1)

BASE32 = "0123456789bcdefghjkmnpqrstuvwxyz"


def encode(lat: float, lon: float, precision: int = 6) -> str:
    precision = max(1, min(12, precision))
    min_lat, max_lat = -90.0, 90.0
    min_lon, max_lon = -180.0, 180.0
    bits: list[int] = []
    is_lon = True
    while len(bits) // 5 < precision:
        if is_lon:
            mid = (min_lon + max_lon) / 2
            if lon >= mid:
                bits.append(1)
                min_lon = mid
            else:
                bits.append(0)
                max_lon = mid
        else:
            mid = (min_lat + max_lat) / 2
            if lat >= mid:
                bits.append(1)
                min_lat = mid
            else:
                bits.append(0)
                max_lat = mid
        is_lon = not is_lon
    out = []
    for i in range(precision):
        idx = 0
        for j in range(5):
            idx = (idx << 1) | bits[i * 5 + j]
        out.append(BASE32[idx])
    return "".join(out)


def backfill(cur, table: str, id_col: str = "id") -> int:
    cur.execute(
        f"SELECT {id_col}, latitude, longitude FROM {table} "
        f"WHERE latitude <> 0 OR longitude <> 0"
    )
    rows = cur.fetchall()
    n = 0
    for rid, lat, lon in rows:
        lat_f = float(lat or 0)
        lon_f = float(lon or 0)
        if lat_f == 0 and lon_f == 0:
            continue
        g6 = encode(lat_f, lon_f, 6)
        g5 = encode(lat_f, lon_f, 5)
        cur.execute(
            f"UPDATE {table} SET geohash = %s, geohash5 = %s WHERE {id_col} = %s",
            (g6, g5, rid),
        )
        n += 1
    return n


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=5432)
    p.add_argument("--db", default="fac_identity")
    p.add_argument("--user", default="identity")
    p.add_argument("--password", default="identity_local_change_me")
    args = p.parse_args()

    conn = psycopg2.connect(
        host=args.host,
        port=args.port,
        dbname=args.db,
        user=args.user,
        password=args.password,
    )
    conn.autocommit = False
    cur = conn.cursor()
    total = 0
    for table in ("app_facilities", "app_areas", "app_cities"):
        n = backfill(cur, table)
        print(f"{table}: updated {n} rows")
        total += n
    conn.commit()
    cur.close()
    conn.close()
    print(f"done, {total} rows")


if __name__ == "__main__":
    main()
