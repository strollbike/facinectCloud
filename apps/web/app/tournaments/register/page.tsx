'use client';

import { FormEvent, Suspense, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import styles from './register.module.css';

type Category = {
  id?: number;
  name?: string;
  category_name?: string;
  fee?: number;
  entry_fee?: number;
};

type Tournament = {
  id: number;
  name: string;
  start_date?: string;
  end_date?: string;
  venue_address?: string;
  status: string;
  slug?: string;
  categories?: Category[];
  contact_numbers?: string;
};

type FormField = {
  field_name: string;
  is_mandatory?: number | boolean;
  locked?: number | boolean;
};

const FIELD_LABELS: Record<string, string> = {
  first_name: 'First name',
  last_name: 'Last name',
  dob: 'Date of birth',
  gender: 'Gender',
  whatsapp_number: 'WhatsApp number',
  alternate_contact: 'Alternate contact',
  address: 'Address',
  club_or_academy: 'Club / academy',
  id_proof: 'ID proof',
  id_number: 'ID number',
  email: 'Email',
  player_name: 'Player name',
  phone: 'Phone',
  category: 'Category',
};

function labelFor(name: string) {
  return FIELD_LABELS[name] || name.replace(/_/g, ' ');
}

function isMandatory(f: FormField) {
  return f.is_mandatory === 1 || f.is_mandatory === true;
}

function catName(c: Category) {
  return (c.name || c.category_name || '').trim();
}

function catFee(c: Category) {
  const n = Number(c.fee ?? c.entry_fee ?? 0);
  return Number.isFinite(n) ? n : 0;
}

async function publicFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/v1/tournaments${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(init?.headers || {}),
    },
  });
  const data = (await res.json().catch(() => ({}))) as T & { error?: string; success?: boolean };
  if (!res.ok) {
    throw new Error(data.error || `http_${res.status}`);
  }
  return data;
}

function RegisterInner() {
  const params = useSearchParams();
  const tourIdParam = params.get('tourId') || params.get('tournamentId') || '';
  const slugParam = (params.get('slug') || '').trim();

  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [okMsg, setOkMsg] = useState('');
  const [tournament, setTournament] = useState<Tournament | null>(null);
  const [fields, setFields] = useState<FormField[]>([]);
  const [values, setValues] = useState<Record<string, string>>({});
  const [category, setCategory] = useState('');

  const tourId = useMemo(() => {
    const n = parseInt(tourIdParam, 10);
    return Number.isFinite(n) && n > 0 ? n : 0;
  }, [tourIdParam]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      setError('');
      setOkMsg('');
      try {
        if (!tourId && !slugParam) {
          throw new Error('missing_tournament');
        }
        let tour: Tournament;
        if (tourId > 0) {
          const res = await publicFetch<{ tournament?: Tournament; data?: Tournament }>(
            `/public/tournaments/${tourId}`,
          );
          tour = (res.tournament || res.data)!;
        } else {
          const res = await publicFetch<{ tournament?: Tournament; data?: Tournament }>(
            `/public/by-slug/${encodeURIComponent(slugParam)}`,
          );
          tour = (res.tournament || res.data)!;
        }
        if (!tour?.id) throw new Error('not_found');
        const ff = await publicFetch<{ fields?: FormField[] }>(
          `/public/tournaments/${tour.id}/form-fields`,
        );
        if (cancelled) return;
        setTournament(tour);
        const cats = tour.categories || [];
        if (cats.length === 1) setCategory(catName(cats[0]));
        const list = Array.isArray(ff.fields) ? ff.fields : [];
        setFields(list.filter((f) => f?.field_name && f.field_name !== 'category'));
        setValues({});
      } catch (e) {
        if (cancelled) return;
        const code = e instanceof Error ? e.message : 'load_failed';
        const map: Record<string, string> = {
          missing_tournament: 'This registration link is incomplete.',
          not_found: 'Tournament not found.',
          registration_closed: 'Registration is closed for this tournament.',
          invalid_tournament: 'Invalid tournament.',
        };
        setError(map[code] || code);
        setTournament(null);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [tourId, slugParam]);

  function setField(name: string, value: string) {
    setValues((prev) => ({ ...prev, [name]: value }));
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!tournament) return;
    setBusy(true);
    setError('');
    setOkMsg('');
    try {
      for (const f of fields) {
        if (isMandatory(f) && !String(values[f.field_name] || '').trim()) {
          throw new Error(`${labelFor(f.field_name)} is required`);
        }
      }
      const cats = tournament.categories || [];
      if (cats.length > 0 && !category.trim()) {
        throw new Error('Please select a category');
      }
      const body: Record<string, string> = {
        ...values,
        category: category.trim(),
        gender: (values.gender || '').trim(),
        player_name: [
          values.player_name,
          [values.first_name, values.last_name].filter(Boolean).join(' '),
        ]
          .map((s) => (s || '').trim())
          .find(Boolean) || '',
        phone: (values.whatsapp_number || values.phone || '').trim(),
        email: (values.email || '').trim(),
      };
      await publicFetch(`/public/tournaments/${tournament.id}/register`, {
        method: 'POST',
        body: JSON.stringify(body),
      });
      setOkMsg('You are registered. The facility will confirm payment details if needed.');
      setValues({});
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Registration failed';
      const map: Record<string, string> = {
        registration_closed: 'Registration is closed for this tournament.',
        category_required: 'Please select a category.',
        invalid_category: 'That category is not available.',
        phone_required: 'WhatsApp / phone number is required.',
        invalid_entry: 'Please enter your name.',
      };
      setError(map[msg] || msg);
    } finally {
      setBusy(false);
    }
  }

  const categories = tournament?.categories || [];

  return (
    <div className={styles.page}>
      <div className={styles.shell}>
        <div className={styles.brand}>
          <div className={styles.brandMark}>F</div>
          <div className={styles.brandName}>Facinect</div>
        </div>

        <div className={styles.card}>
          {loading && <p className={styles.center}>Loading registration…</p>}

          {!loading && error && !tournament && (
            <div className={`${styles.banner} ${styles.bannerError}`}>{error}</div>
          )}

          {!loading && tournament && (
            <>
              <h1 className={styles.title}>{tournament.name}</h1>
              <p className={styles.meta}>
                {tournament.start_date ? (
                  <>
                    <strong>{tournament.start_date}</strong>
                    {tournament.end_date && tournament.end_date !== tournament.start_date
                      ? ` – ${tournament.end_date}`
                      : ''}
                    <br />
                  </>
                ) : null}
                {tournament.venue_address || 'Venue TBA'}
                {tournament.status ? (
                  <>
                    <br />
                    Status: <strong>{tournament.status}</strong>
                  </>
                ) : null}
              </p>

              {error ? (
                <div className={`${styles.banner} ${styles.bannerError}`}>{error}</div>
              ) : null}
              {okMsg ? (
                <div className={`${styles.banner} ${styles.bannerOk}`}>{okMsg}</div>
              ) : null}

              {!okMsg && (
                <form onSubmit={onSubmit}>
                  {categories.length > 0 && (
                    <div className={styles.field}>
                      <label className={styles.label} htmlFor="category">
                        Category *
                      </label>
                      <select
                        id="category"
                        className={styles.select}
                        value={category}
                        onChange={(ev) => setCategory(ev.target.value)}
                        required
                      >
                        <option value="">Select category</option>
                        {categories.map((c) => {
                          const name = catName(c);
                          const fee = catFee(c);
                          return (
                            <option key={c.id ?? name} value={name}>
                              {name}
                              {fee > 0 ? ` — ₹${fee}` : ''}
                            </option>
                          );
                        })}
                      </select>
                      {category
                        ? (() => {
                            const c = categories.find((x) => catName(x) === category);
                            const fee = c ? catFee(c) : 0;
                            return fee > 0 ? (
                              <span className={styles.fee}>Entry fee ₹{fee}</span>
                            ) : null;
                          })()
                        : null}
                    </div>
                  )}

                  {fields.map((f) => {
                    const name = f.field_name;
                    const required = isMandatory(f);
                    if (name === 'gender') {
                      return (
                        <div className={styles.field} key={name}>
                          <label className={styles.label} htmlFor={name}>
                            {labelFor(name)}
                            {required ? ' *' : ''}
                          </label>
                          <select
                            id={name}
                            className={styles.select}
                            value={values[name] || ''}
                            onChange={(ev) => setField(name, ev.target.value)}
                            required={required}
                          >
                            <option value="">Select</option>
                            <option value="Male">Male</option>
                            <option value="Female">Female</option>
                            <option value="Other">Other</option>
                          </select>
                        </div>
                      );
                    }
                    const type =
                      name === 'dob'
                        ? 'date'
                        : name.includes('contact') || name.includes('whatsapp') || name === 'phone'
                          ? 'tel'
                          : name === 'email'
                            ? 'email'
                            : 'text';
                    return (
                      <div className={styles.field} key={name}>
                        <label className={styles.label} htmlFor={name}>
                          {labelFor(name)}
                          {required ? ' *' : ''}
                        </label>
                        <input
                          id={name}
                          className={styles.input}
                          type={type}
                          value={values[name] || ''}
                          onChange={(ev) => setField(name, ev.target.value)}
                          required={required}
                          autoComplete="off"
                        />
                      </div>
                    );
                  })}

                  <button className={styles.submit} type="submit" disabled={busy}>
                    {busy ? 'Submitting…' : 'Register'}
                  </button>
                </form>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

export default function TournamentRegisterPage() {
  return (
    <Suspense fallback={<div className={styles.page}><p className={styles.center}>Loading…</p></div>}>
      <RegisterInner />
    </Suspense>
  );
}
