'use client';

import {
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type CSSProperties,
} from 'react';
import Script from 'next/script';
import { useSearchParams } from 'next/navigation';
import styles from './register.module.css';

type Category = {
  id?: number;
  name?: string;
  category_name?: string;
  fee?: number;
  entry_fee?: number;
  team_size?: number;
};

type Tournament = {
  id: number;
  name: string;
  start_date?: string;
  end_date?: string;
  venue_address?: string;
  status: string;
  slug?: string;
  rules?: string;
  categories?: Category[];
  contact_numbers?: string;
  allow_full_payment?: boolean | number;
  allow_advance_payment?: boolean | number;
  allow_spot_payment?: boolean | number;
  advance_amount?: number;
  entries_open?: number;
};

type FormField = {
  field_name: string;
  is_mandatory?: number | boolean;
  locked?: number | boolean;
};

type Checkout = {
  entry_id: number;
  payment_mode: string;
  amount: number;
  currency: string;
  order_id?: string;
  key_id?: string;
  provider?: string;
  spot?: boolean;
  entry?: { id: number; category?: string; payment_status?: string; payment_mode?: string };
};

declare global {
  interface Window {
    Razorpay?: new (opts: Record<string, unknown>) => { open: () => void };
  }
}

const FIELD_LABELS: Record<string, string> = {
  first_name: 'First name',
  last_name: 'Last name',
  dob: 'Date of birth',
  gender: 'Gender',
  whatsapp_number: 'WhatsApp Number',
  alternate_contact: 'Alternate contact',
  address: 'Address',
  club_or_academy: 'Club / academy',
  id_proof: 'ID proof',
  id_number: 'ID number',
  email: 'Email',
  player_name: 'Player name',
  phone: 'Phone',
  full_name: 'Full Name',
  name: 'Name',
};

function labelFor(name: string) {
  const key = name.toLowerCase().trim();
  return FIELD_LABELS[key] || name.replace(/_/g, ' ');
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

function catSize(c: Category) {
  const n = Number(c.team_size ?? 1);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 1;
}

function asFlag(v: boolean | number | undefined, fallback = true) {
  if (v === undefined || v === null) return fallback;
  if (typeof v === 'boolean') return v;
  return Number(v) === 1;
}

function formatINR(n: number) {
  return '₹' + Number(n || 0).toLocaleString('en-IN', { maximumFractionDigits: 2 });
}

function formatDateBadge(iso?: string) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' });
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
  const data = (await res.json().catch(() => ({}))) as T & { error?: string };
  if (!res.ok) {
    throw new Error(data.error || `http_${res.status}`);
  }
  return data;
}

function getCategoryRules(tournamentName: string, category: string) {
  let gender = '';
  let minYear: number | null = null;
  const name = `${tournamentName || ''} ${category || ''}`.toUpperCase();
  if (/\b(WOMEN'?S?|WOMENS|FEMALE|GIRLS?|LADIES)\b/.test(name) || /\bG\b/.test(name)) {
    gender = 'Female';
  } else if (/\b(MEN'?S?|MENS|MALE|BOYS?)\b/.test(name) || /\bB\b/.test(name)) {
    gender = 'Male';
  }
  if (name.includes('U-9')) minYear = 2017;
  else if (name.includes('U-11')) minYear = 2015;
  else if (name.includes('U-13')) minYear = 2013;
  else if (name.includes('U-15')) minYear = 2011;
  else if (name.includes('U-17')) minYear = 2009;
  else if (name.includes('U-19')) minYear = 2007;
  return { gender, minYear };
}

function isPhoneField(name: string) {
  const flink = name.toLowerCase();
  return (
    flink.includes('whatsapp') ||
    flink.includes('mobile') ||
    flink.includes('phone') ||
    flink.includes('contact') ||
    flink === 'wa' ||
    flink.includes('wa number') ||
    flink.includes('wa no')
  );
}

function isClubAcademyField(name: string) {
  const flink = name.toLowerCase();
  return flink.includes('club') || flink.includes('academy');
}

function pad2(n: number) {
  return String(n).padStart(2, '0');
}

function daysInMonth(year: number, month: number) {
  return new Date(year, month, 0).getDate();
}

function parseIsoDob(iso: string | undefined) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec((iso || '').trim());
  if (!m) return { y: 0, mo: 0, d: 0 };
  return { y: Number(m[1]), mo: Number(m[2]), d: Number(m[3]) };
}

function toIsoDob(y: number, mo: number, d: number) {
  if (!y || !mo || !d) return '';
  return `${y}-${pad2(mo)}-${pad2(d)}`;
}

function formatDobDisplay(iso: string) {
  const { y, mo, d } = parseIsoDob(iso);
  if (!y || !mo || !d) return '';
  return `${pad2(d)}/${pad2(mo)}/${y}`;
}

function defaultDobYear(minYear: number | null) {
  const now = new Date();
  let y = now.getFullYear() - 18;
  const first = minYear && minYear > 1900 ? minYear : 1900;
  if (y < first) y = first;
  if (y > now.getFullYear()) y = now.getFullYear();
  return y;
}

/** Year → Month → Day DOB (1900/minYear → today). Stores yyyy-MM-dd; shows DD/MM/YYYY. */
function DobField({
  value,
  minYear,
  error,
  onChange,
}: {
  value: string;
  minYear: number | null;
  error?: boolean;
  onChange: (iso: string) => void;
}) {
  const now = new Date();
  const maxY = now.getFullYear();
  const maxMo = now.getMonth() + 1;
  const maxD = now.getDate();
  const firstY = minYear && minYear > 1900 ? minYear : 1900;

  // Draft parts so Year/Month stick before Day is chosen (parent value is only set when complete).
  const parsed = parseIsoDob(value);
  const [year, setYear] = useState(parsed.y || 0);
  const [month, setMonth] = useState(parsed.mo || 0);
  const [day, setDay] = useState(parsed.d || 0);

  // Sync from parent only when a complete ISO date is provided (do not wipe draft on '').
  useEffect(() => {
    const p = parseIsoDob(value);
    if (p.y && p.mo && p.d) {
      setYear(p.y);
      setMonth(p.mo);
      setDay(p.d);
    }
  }, [value]);

  const years: number[] = [];
  for (let y = maxY; y >= firstY; y--) years.push(y);

  const months: number[] = [];
  if (year) {
    const lastMo = year === maxY ? maxMo : 12;
    for (let m = 1; m <= lastMo; m++) months.push(m);
  }

  const days: number[] = [];
  if (year && month) {
    let lastD = daysInMonth(year, month);
    if (year === maxY && month === maxMo) lastD = Math.min(lastD, maxD);
    for (let d = 1; d <= lastD; d++) days.push(d);
  }

  function publish(y: number, mo: number, d: number) {
    if (!y || !mo || !d) {
      onChange('');
      return;
    }
    let useD = d;
    const maxForMonth = daysInMonth(y, mo);
    let lastAllowed = maxForMonth;
    if (y === maxY && mo === maxMo) lastAllowed = Math.min(maxForMonth, maxD);
    if (useD > lastAllowed) useD = lastAllowed;
    const cand = new Date(y, mo - 1, useD);
    const today = new Date(maxY, maxMo - 1, maxD);
    if (cand > today) {
      setYear(maxY);
      setMonth(maxMo);
      setDay(maxD);
      onChange(toIsoDob(maxY, maxMo, maxD));
      return;
    }
    if (useD !== d) setDay(useD);
    onChange(toIsoDob(y, mo, useD));
  }

  return (
    <div className={styles.dobWrap}>
      <div className={styles.dobRow}>
        <select
          className={`${styles.select} ${error ? styles.fieldError : ''}`}
          aria-label="Birth year"
          value={year || ''}
          onChange={(e) => {
            const y = Number(e.target.value) || 0;
            setYear(y);
            let mo = month;
            let d = day;
            if (!y) {
              setMonth(0);
              setDay(0);
              onChange('');
              return;
            }
            if (y === maxY && mo > maxMo) {
              mo = 0;
              d = 0;
              setMonth(0);
              setDay(0);
            } else if (mo && d) {
              const lastD = daysInMonth(y, mo);
              if (d > lastD) {
                d = lastD;
                setDay(lastD);
              }
            } else {
              d = 0;
              setDay(0);
            }
            publish(y, mo, d);
          }}
        >
          <option value="">Year</option>
          {years.map((y) => (
            <option key={y} value={y}>
              {y}
            </option>
          ))}
        </select>
        <select
          className={`${styles.select} ${error ? styles.fieldError : ''}`}
          aria-label="Birth month"
          value={month || ''}
          disabled={!year}
          onChange={(e) => {
            const mo = Number(e.target.value) || 0;
            setMonth(mo);
            let d = day;
            if (!mo) {
              setDay(0);
              onChange('');
              return;
            }
            if (d) {
              const lastD = daysInMonth(year, mo);
              const capped =
                year === maxY && mo === maxMo ? Math.min(lastD, maxD) : lastD;
              if (d > capped) {
                d = capped;
                setDay(capped);
              }
            }
            publish(year, mo, d);
          }}
        >
          <option value="">Month</option>
          {months.map((m) => (
            <option key={m} value={m}>
              {pad2(m)}
            </option>
          ))}
        </select>
        <select
          className={`${styles.select} ${error ? styles.fieldError : ''}`}
          aria-label="Birth day"
          value={day || ''}
          disabled={!year || !month}
          onChange={(e) => {
            const d = Number(e.target.value) || 0;
            setDay(d);
            if (!d) {
              onChange('');
              return;
            }
            publish(year, month, d);
          }}
        >
          <option value="">Day</option>
          {days.map((d) => (
            <option key={d} value={d}>
              {pad2(d)}
            </option>
          ))}
        </select>
      </div>
      <div className={styles.dobHint}>
        {year && month && day && value ? (
          <>
            Selected: <b>{formatDobDisplay(value)}</b>
          </>
        ) : (
          <>
            Select year → month → day
            {!year ? ` (suggested year: ${defaultDobYear(minYear)})` : ''}
          </>
        )}
      </div>
    </div>
  );
}

type PlayerValues = Record<string, string>;

function RegisterInner() {
  const params = useSearchParams();
  const tourIdParam = params.get('tourId') || params.get('tournamentId') || params.get('tid') || '';
  const slugParam = (params.get('slug') || '').trim();

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [closed, setClosed] = useState(false);
  const [tournament, setTournament] = useState<Tournament | null>(null);
  const [fields, setFields] = useState<FormField[]>([]);
  const [templateUrl, setTemplateUrl] = useState('');
  const [showTemplate, setShowTemplate] = useState(false);
  const [templateDismissed, setTemplateDismissed] = useState(false);

  const [category, setCategory] = useState('');
  const [step, setStep] = useState(0); // 0 = not built, 1..n player steps, -1 = review
  const [players, setPlayers] = useState<PlayerValues[]>([]);
  const [fieldErrors, setFieldErrors] = useState<Record<string, boolean>>({});
  const [ageErrors, setAgeErrors] = useState<Record<string, boolean>>({});

  const [payMode, setPayMode] = useState('');
  const [agreed, setAgreed] = useState(false);
  const [showRules, setShowRules] = useState(false);
  const [busy, setBusy] = useState(false);
  const [success, setSuccess] = useState<{
    category: string;
    code: string;
    payment_mode?: string;
    payment_status?: string;
  } | null>(null);

  const tourId = useMemo(() => {
    const n = parseInt(tourIdParam, 10);
    return Number.isFinite(n) && n > 0 ? n : 0;
  }, [tourIdParam]);

  const categories = tournament?.categories || [];
  const singleCategory = categories.length === 1;
  const selectedCat = categories.find((c) => catName(c) === category) || categories[0];
  const teamSize = selectedCat ? catSize(selectedCat) : 0;
  const entryFee = selectedCat ? catFee(selectedCat) : 0;
  const rulesText =
    (tournament?.rules || '').trim() ||
    'Rules and Regulations for this tournament have not been uploaded yet.';

  const payOpts = useMemo(
    () => ({
      allow_full_payment: asFlag(tournament?.allow_full_payment, true),
      allow_advance_payment: asFlag(tournament?.allow_advance_payment, false),
      allow_spot_payment: asFlag(tournament?.allow_spot_payment, false),
      advance_amount: Number(tournament?.advance_amount || 0),
      pay_at_label: 'Pay at academy',
    }),
    [tournament],
  );

  const catRules = useMemo(
    () => getCategoryRules(tournament?.name || '', category),
    [tournament?.name, category],
  );

  const formFields = useMemo(() => {
    const list = Array.isArray(fields) ? [...fields] : [];
    const hasPhone = list.some((f) => isPhoneField(f.field_name || ''));
    if (!hasPhone) {
      list.unshift({ field_name: 'WhatsApp Number', is_mandatory: 1 });
    }
    return list;
  }, [fields]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      setError('');
      setClosed(false);
      try {
        if (!tourId && !slugParam) throw new Error('missing_tournament');
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
        let tplUrl = '';
        try {
          const tpl = await publicFetch<{ template?: { image_url?: string } | null }>(
            `/public/tournaments/${tour.id}/template`,
          );
          tplUrl = (tpl.template?.image_url || '').trim();
        } catch {
          /* optional */
        }
        if (cancelled) return;
        setTournament(tour);
        setFields(ff.fields || []);
        setTemplateUrl(tplUrl);
        if (tplUrl) setShowTemplate(true);
        const cats = tour.categories || [];
        if (cats.length === 1) {
          setCategory(catName(cats[0]));
        }
      } catch (e) {
        if (cancelled) return;
        const msg = e instanceof Error ? e.message : 'load_failed';
        if (msg === 'registration_closed') setClosed(true);
        else setError(msg);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [tourId, slugParam]);

  const buildAccordion = useCallback(
    (cat: string) => {
      const c = categories.find((x) => catName(x) === cat);
      if (!c) {
        setPlayers([]);
        setStep(0);
        return;
      }
      const size = catSize(c);
      const rules = getCategoryRules(tournament?.name || '', cat);
      const blank: PlayerValues[] = [];
      for (let i = 0; i < size; i++) {
        const row: PlayerValues = {};
        for (const f of formFields) {
          const n = f.field_name;
          const fl = n.toLowerCase();
          if (fl.includes('gender') && rules.gender) row[n] = rules.gender;
          else row[n] = '';
        }
        blank.push(row);
      }
      setPlayers(blank);
      setStep(1);
      setAgreed(false);
      setPayMode('');
      setFieldErrors({});
      setAgeErrors({});
      setSuccess(null);
    },
    [categories, formFields, tournament?.name],
  );

  useEffect(() => {
    if (singleCategory && category && step === 0 && !loading && tournament) {
      buildAccordion(category);
    }
  }, [singleCategory, category, step, loading, tournament, buildAccordion]);

  function setPlayerField(pi: number, name: string, value: string) {
    setPlayers((prev) => {
      const next = prev.map((p, i) => (i === pi ? { ...p, [name]: value } : p));
      return next;
    });
    setFieldErrors((prev) => {
      const key = `${pi}:${name}`;
      if (!prev[key]) return prev;
      const n = { ...prev };
      delete n[key];
      return n;
    });
  }

  function validatePlayer(pi: number) {
    const card = players[pi] || {};
    const errs: Record<string, boolean> = {};
    const ages: Record<string, boolean> = {};
    let ok = true;
    for (const f of formFields) {
      const n = f.field_name;
      const fl = n.toLowerCase();
      if (isMandatory(f) && !String(card[n] || '').trim()) {
        errs[`${pi}:${n}`] = true;
        ok = false;
      }
      if ((fl.includes('date') || fl.includes('dob')) && catRules.minYear && card[n]) {
        const birthYear = new Date(card[n]).getFullYear();
        if (birthYear < catRules.minYear) {
          errs[`${pi}:${n}`] = true;
          ages[`${pi}:${n}`] = true;
          ok = false;
        }
      }
    }
    setFieldErrors((prev) => ({ ...prev, ...errs }));
    setAgeErrors((prev) => ({ ...prev, ...ages }));
    return ok;
  }

  function nextStep(n: number) {
    if (!validatePlayer(n - 1)) return;
    setStep(n + 1);
  }

  function reviewFinal() {
    let allValid = true;
    for (let i = 0; i < players.length; i++) {
      if (!validatePlayer(i)) allValid = false;
    }
    if (!allValid) return;
    setStep(-1);
    // auto-select sole payment option
    const opts = availablePayOptions();
    if (opts.length === 1) setPayMode(opts[0].mode);
  }

  function availablePayOptions() {
    const fee = entryFee;
    const out: { mode: string; amount: number; label: string }[] = [];
    if (fee <= 0) return out;
    const configuredAdvance = Math.max(0, payOpts.advance_amount || 0);
    const advance = fee > 0 ? Math.min(fee, configuredAdvance) : configuredAdvance;
    if (payOpts.allow_full_payment) {
      out.push({
        mode: 'full',
        amount: fee,
        label: `Pay Full Amount (${formatINR(fee)})`,
      });
    }
    if (payOpts.allow_advance_payment && advance > 0) {
      const balance = Math.max(0, fee - advance);
      const balLabel =
        balance > 0 ? ` — balance ${formatINR(balance)} later` : ' — covers full entry fee';
      out.push({
        mode: 'advance',
        amount: advance,
        label: `Pay Advance (${formatINR(advance)})${balLabel}`,
      });
    }
    if (payOpts.allow_spot_payment) {
      out.push({
        mode: 'spot',
        amount: 0,
        label: `${payOpts.pay_at_label} — register now, pay (${formatINR(fee)}) later`,
      });
    }
    return out;
  }

  const payOptions = availablePayOptions();
  const reviewing = step === -1;
  const canSubmit =
    reviewing &&
    agreed &&
    !busy &&
    (entryFee <= 0 || (payOptions.length > 0 && !!payMode));

  function captainPhone() {
    const card = players[0] || {};
    for (const [k, v] of Object.entries(card)) {
      if (isPhoneField(k) && String(v || '').trim()) return String(v).trim();
    }
    return '';
  }

  function playerPayload() {
    return players.map((p) => {
      const obj: Record<string, string> = {};
      const fn = p.first_name || p['First Name'] || '';
      const ln = p.last_name || p['Last Name'] || '';
      const full =
        `${fn} ${ln}`.trim() ||
        p.player_name ||
        p['Full Name'] ||
        p.name ||
        p['Participant Name'] ||
        '';
      obj.n = full;
      obj.player_name = full;
      obj.first_name = fn;
      obj.last_name = ln;
      for (const [k, v] of Object.entries(p)) {
        if (!v) continue;
        obj[k] = String(v);
        if (isPhoneField(k)) {
          obj.phone = String(v);
          obj.whatsapp_number = String(v);
        }
        if (k.toLowerCase().includes('gender')) obj.gender = String(v);
        if (k.toLowerCase().includes('email')) obj.email = String(v);
      }
      return obj;
    });
  }

  async function handlePayment() {
    if (!tournament || !category) return;
    if (entryFee > 0 && payOptions.length > 0 && !payMode) {
      alert('Please select a payment option.');
      return;
    }
    const mode = entryFee <= 0 ? 'full' : payMode || 'full';
    setBusy(true);
    try {
      const payload = {
        category,
        payment_mode: mode,
        phone: captainPhone(),
        players: playerPayload(),
        player_name: playerPayload()[0]?.player_name || '',
        gender: playerPayload()[0]?.gender || '',
        email: playerPayload()[0]?.email || '',
      };
      const res = await publicFetch<{
        entry?: { id: number; category?: string; payment_status?: string; payment_mode?: string };
        checkout?: Checkout;
        checkout_error?: string;
        message?: string;
      }>(`/public/tournaments/${tournament.id}/register`, {
        method: 'POST',
        body: JSON.stringify(payload),
      });

      const entry = res.entry;
      const checkout = res.checkout;
      if (!entry?.id) throw new Error('register_failed');

      if (mode === 'spot' || !checkout || checkout.spot || (checkout.amount || 0) <= 0) {
        setSuccess({
          category: entry.category || category,
          code: String(entry.id),
          payment_mode: entry.payment_mode || mode,
          payment_status: entry.payment_status || (mode === 'spot' ? 'Pending' : 'pending'),
        });
        setBusy(false);
        return;
      }

      if (!checkout.order_id || !checkout.key_id) {
        throw new Error(res.checkout_error || 'checkout_unavailable');
      }
      if (!window.Razorpay) throw new Error('razorpay_sdk_missing');

      const rzp = new window.Razorpay({
        key: checkout.key_id,
        amount: Math.round((checkout.amount || 0) * 100),
        currency: checkout.currency || 'INR',
        name: tournament.name,
        order_id: checkout.order_id,
        theme: { color: '#4f46e5' },
        handler: async (response: {
          razorpay_payment_id?: string;
          razorpay_order_id?: string;
          razorpay_signature?: string;
        }) => {
          try {
            await publicFetch(`/public/entries/${entry.id}/confirm-payment`, {
              method: 'POST',
              body: JSON.stringify({
                order_id: response.razorpay_order_id || checkout.order_id,
                payment_id: response.razorpay_payment_id,
                signature: response.razorpay_signature,
              }),
            });
            setSuccess({
              category: entry.category || category,
              code: String(entry.id),
              payment_mode: checkout.payment_mode || mode,
              payment_status: 'Paid',
            });
          } catch (e) {
            alert(e instanceof Error ? e.message : 'confirm_failed');
          } finally {
            setBusy(false);
          }
        },
        modal: {
          ondismiss: () => setBusy(false),
        },
      });
      rzp.open();
    } catch (e) {
      alert(e instanceof Error ? e.message : 'register_failed');
      setBusy(false);
    }
  }

  if (loading) {
    return (
      <div className={styles.page}>
        <div className={styles.disabledCard}>
          <h1>Loading…</h1>
          <p>Fetching tournament registration.</p>
        </div>
      </div>
    );
  }

  if (closed) {
    return (
      <div className={styles.page}>
        <div className={styles.disabledCard}>
          <span style={{ fontSize: 60, display: 'block', marginBottom: 20 }}>🚫</span>
          <h1>Registration Closed</h1>
          <p>
            Registration for <strong>{tournament?.name || 'this tournament'}</strong> is currently
            closed or has reached its capacity.
          </p>
        </div>
      </div>
    );
  }

  if (error || !tournament) {
    return (
      <div className={styles.page}>
        <div className={styles.disabledCard}>
          <h1>Invalid Link</h1>
          <p>{error || 'Tournament not found.'}</p>
        </div>
      </div>
    );
  }

  const layoutBlur = showTemplate && !!templateUrl && !templateDismissed;

  return (
    <div className={styles.page}>
      <link
        href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;600;700;800&display=swap"
        rel="stylesheet"
      />
      <Script src="https://checkout.razorpay.com/v1/checkout.js" strategy="lazyOnload" />

      {showTemplate && templateUrl && !templateDismissed ? (
        <div className={styles.modalOverlay}>
          <div className={styles.templateModalCard}>
            <button
              type="button"
              className={styles.closeTemplate}
              onClick={() => {
                setTemplateDismissed(true);
                setShowTemplate(false);
              }}
              aria-label="Close"
            >
              &times;
            </button>
            <img src={templateUrl} alt="Tournament Template" className={styles.templateImg} />
          </div>
        </div>
      ) : null}

      <div
        className={`${styles.mainLayout} ${layoutBlur ? styles.blurEffect : ''}`}
        id="mainLayoutContent"
      >
        <div className={styles.container}>
          {success ? (
            <div className={styles.successBanner}>
              <h1>Registration Successful</h1>
              <p>
                Category: <b>{success.category}</b>
                <br />
                Registration code: <b>{success.code}</b>
                {success.payment_mode ? (
                  <>
                    <br />
                    Payment: <b>{success.payment_mode}</b>
                  </>
                ) : null}
                {success.payment_status ? (
                  <>
                    <br />
                    Status in admin: <b>{success.payment_status}</b>
                    {String(success.payment_status).toLowerCase() === 'pending' ? (
                      <span style={{ fontWeight: 500 }}>
                        {' '}
                        — Spot/Advance entries appear under <b>Pending</b>, not Confirmed (Paid).
                      </span>
                    ) : null}
                  </>
                ) : null}
              </p>
              <p style={{ margin: '12px 0 0', fontSize: 13, color: '#059669' }}>
                App push confirmation was sent to the captain (if they have the Facinect app).
              </p>
            </div>
          ) : null}

          <div className={styles.headerSection}>
            <h1>{tournament.name}</h1>
            {tournament.start_date ? (
              <div className={styles.dateBadge}>
                <i className="fa-regular fa-calendar-days" /> {formatDateBadge(tournament.start_date)}
              </div>
            ) : null}
            <p className={styles.muted}>Complete your registration & payment</p>
          </div>

          {!singleCategory ? (
            <div>
              <span className={styles.sectionLabel}>01. Select Category</span>
              <select
                className={styles.select}
                value={category}
                onChange={(e) => {
                  const v = e.target.value;
                  setCategory(v);
                  if (v) buildAccordion(v);
                  else {
                    setPlayers([]);
                    setStep(0);
                  }
                }}
              >
                <option value="">-- Select Category --</option>
                {categories.map((c) => (
                  <option key={catName(c)} value={catName(c)}>
                    {catName(c)} ({formatINR(catFee(c))})
                  </option>
                ))}
              </select>
            </div>
          ) : (
            <div className={styles.sectionLabel} style={{ marginBottom: 8 }}>
              Category: <b>{category}</b> ({formatINR(entryFee)})
            </div>
          )}

          {step !== 0 && players.length > 0 ? (
            <div style={{ marginTop: 25 }}>
              {players.map((p, i) => {
                const active = reviewing || step === i + 1;
                return (
                  <div
                    key={i}
                    className={`${styles.playerCard} ${active ? styles.playerCardActive : ''}`}
                  >
                    <div className={styles.playerHeader}>
                      <span>
                        {i === 0 && teamSize > 1 ? (
                          <>
                            <i
                              className="fa-solid fa-crown"
                              style={{ color: 'var(--accent)' } as CSSProperties}
                            />{' '}
                            Captain
                          </>
                        ) : (
                          `Participant ${i + 1}`
                        )}
                      </span>
                      <small style={{ color: 'var(--primary)' }}>
                        {i + 1} of {teamSize}
                      </small>
                    </div>
                    <div className={styles.formGrid}>
                      {formFields.map((f) => {
                        const fname = f.field_name;
                        const flink = fname.toLowerCase();
                        const req = isMandatory(f);
                        const errKey = `${i}:${fname}`;
                        const err = !!fieldErrors[errKey];
                        const star = req ? <span className={styles.req}>*</span> : null;

                        if (flink === 'full name' || flink === 'name') {
                          return (
                            <div key={fname} className={`${styles.fieldGroup} ${styles.span2}`}>
                              <label>Participant Name {star}</label>
                              <div className={styles.nameRow}>
                                <input
                                  className={`${styles.field} ${err ? styles.fieldError : ''}`}
                                  placeholder="First Name"
                                  value={p.first_name || ''}
                                  onChange={(e) => setPlayerField(i, 'first_name', e.target.value)}
                                />
                                <input
                                  className={`${styles.field} ${err ? styles.fieldError : ''}`}
                                  placeholder="Last Name"
                                  value={p.last_name || ''}
                                  onChange={(e) => setPlayerField(i, 'last_name', e.target.value)}
                                />
                              </div>
                            </div>
                          );
                        }

                        if (flink === 'first_name') {
                          return (
                            <div key={fname} className={styles.fieldGroup}>
                              <label>
                                {labelFor(fname)} {star}
                              </label>
                              <input
                                className={`${styles.field} ${err ? styles.fieldError : ''}`}
                                placeholder="First Name"
                                value={p[fname] || ''}
                                onChange={(e) => setPlayerField(i, fname, e.target.value)}
                              />
                            </div>
                          );
                        }
                        if (flink === 'last_name') {
                          return (
                            <div key={fname} className={styles.fieldGroup}>
                              <label>
                                {labelFor(fname)} {star}
                              </label>
                              <input
                                className={`${styles.field} ${err ? styles.fieldError : ''}`}
                                placeholder="Last Name"
                                value={p[fname] || ''}
                                onChange={(e) => setPlayerField(i, fname, e.target.value)}
                              />
                            </div>
                          );
                        }

                        if (isPhoneField(fname)) {
                          return (
                            <div key={fname} className={styles.fieldGroup}>
                              <label>
                                {labelFor(fname)} {star}
                              </label>
                              <div className={styles.waBox}>
                                <input
                                  type="tel"
                                  maxLength={10}
                                  inputMode="numeric"
                                  placeholder="10 Digits"
                                  className={`${styles.field} ${err ? styles.fieldError : ''}`}
                                  value={p[fname] || ''}
                                  onChange={(e) =>
                                    setPlayerField(i, fname, e.target.value.replace(/\D/g, '').slice(0, 10))
                                  }
                                />
                                <i className={`fa-brands fa-whatsapp ${styles.waIcon}`} />
                              </div>
                            </div>
                          );
                        }

                        if (flink.includes('gender')) {
                          const isMale = catRules.gender === 'Male';
                          const isFemale = catRules.gender === 'Female';
                          const isFixed = isMale || isFemale;
                          return (
                            <div key={fname} className={styles.fieldGroup}>
                              <label>
                                {labelFor(fname)} {star}
                              </label>
                              <select
                                className={`${styles.select} ${err ? styles.fieldError : ''}`}
                                value={p[fname] || ''}
                                disabled={isFixed}
                                style={
                                  isFixed
                                    ? { backgroundColor: '#f1f5f9', cursor: 'not-allowed' }
                                    : undefined
                                }
                                onChange={(e) => setPlayerField(i, fname, e.target.value)}
                              >
                                <option value="">Select</option>
                                <option value="Male">Male</option>
                                <option value="Female">Female</option>
                              </select>
                            </div>
                          );
                        }

                        if (flink.includes('date') || flink.includes('dob')) {
                          return (
                            <div key={fname} className={styles.fieldGroup}>
                              <label>
                                {labelFor(fname)} {star}
                              </label>
                              <DobField
                                key={`dob-${i}-${fname}-${category}-${teamSize}`}
                                value={p[fname] || ''}
                                minYear={catRules.minYear}
                                error={err}
                                onChange={(iso) => setPlayerField(i, fname, iso)}
                              />
                              {ageErrors[errKey] ? (
                                <span className={styles.ageErrorMsg}>
                                  Year must be {catRules.minYear} or later.
                                </span>
                              ) : null}
                            </div>
                          );
                        }

                        if (isClubAcademyField(fname)) {
                          return (
                            <div key={fname} className={styles.fieldGroup}>
                              <label>
                                {labelFor(fname)} {star}
                              </label>
                              <input
                                className={`${styles.field} ${err ? styles.fieldError : ''}`}
                                placeholder={`Enter ${labelFor(fname)}`}
                                value={p[fname] || ''}
                                onChange={(e) => setPlayerField(i, fname, e.target.value)}
                              />
                              {i > 0 ? (
                                <button
                                  type="button"
                                  className={styles.sameAsBtn}
                                  onClick={() => {
                                    const captain = players[0] || {};
                                    let src = String(captain[fname] || '').trim();
                                    if (!src) {
                                      for (const [k, v] of Object.entries(captain)) {
                                        if (isClubAcademyField(k) && String(v || '').trim()) {
                                          src = String(v).trim();
                                          break;
                                        }
                                      }
                                    }
                                    if (!src) {
                                      alert('Captain club / academy is empty.');
                                      return;
                                    }
                                    setPlayerField(i, fname, src);
                                  }}
                                >
                                  Same as Captain
                                </button>
                              ) : null}
                            </div>
                          );
                        }

                        return (
                          <div key={fname} className={styles.fieldGroup}>
                            <label>
                              {labelFor(fname)} {star}
                            </label>
                            <input
                              className={`${styles.field} ${err ? styles.fieldError : ''}`}
                              placeholder={`Enter ${labelFor(fname)}`}
                              value={p[fname] || ''}
                              onChange={(e) => setPlayerField(i, fname, e.target.value)}
                            />
                          </div>
                        );
                      })}
                    </div>
                    {!reviewing ? (
                      <div className={styles.btnRow}>
                        {i > 0 ? (
                          <button
                            type="button"
                            className={`${styles.btn} ${styles.btnBack}`}
                            onClick={() => setStep(i)}
                          >
                            Back
                          </button>
                        ) : null}
                        {i < teamSize - 1 ? (
                          <button
                            type="button"
                            className={`${styles.btn} ${styles.btnNext}`}
                            onClick={() => nextStep(i + 1)}
                          >
                            Next
                          </button>
                        ) : (
                          <button
                            type="button"
                            className={`${styles.btn} ${styles.btnNext}`}
                            onClick={reviewFinal}
                          >
                            Review Form
                          </button>
                        )}
                      </div>
                    ) : null}
                  </div>
                );
              })}
            </div>
          ) : null}

          {step !== 0 && entryFee >= 0 && players.length > 0 ? (
            <div className={styles.summaryBox} style={{ display: 'flex' }}>
              <div
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  fontSize: 14,
                  color: '#64748b',
                }}
              >
                <span>Registration Entry Fee:</span>
                <span>{formatINR(entryFee)}</span>
              </div>
            </div>
          ) : null}

          {reviewing && entryFee > 0 ? (
            <div className={styles.paymentBox}>
              <span className={styles.sectionLabel}>Payment option</span>
              {payOptions.length === 0 ? (
                <p className={styles.payWarn}>
                  Online payment options are disabled for this tournament.
                </p>
              ) : (
                payOptions.map((opt) => (
                  <div
                    key={opt.mode}
                    className={`${styles.paymentOption} ${
                      payMode === opt.mode ? styles.paymentOptionChecked : ''
                    }`}
                  >
                    <input
                      type="radio"
                      name="paymentAmount"
                      id={`pay_${opt.mode}`}
                      checked={payMode === opt.mode}
                      onChange={() => setPayMode(opt.mode)}
                    />
                    <label htmlFor={`pay_${opt.mode}`}>
                      {opt.mode === 'full' ? (
                        <>
                          Pay Full Amount (<b>{formatINR(opt.amount)}</b>)
                        </>
                      ) : null}
                      {opt.mode === 'advance' ? (
                        <>
                          Pay Advance (<b>{formatINR(opt.amount)}</b>)
                          {entryFee > opt.amount
                            ? ` — balance ${formatINR(entryFee - opt.amount)} later`
                            : ' — covers full entry fee'}
                        </>
                      ) : null}
                      {opt.mode === 'spot' ? (
                        <>
                          {payOpts.pay_at_label} — register now, pay (
                          <b>{formatINR(entryFee)}</b>) later
                        </>
                      ) : null}
                    </label>
                  </div>
                ))
              )}
            </div>
          ) : null}

          {reviewing ? (
            <div
              className={styles.termsContainer}
              onClick={() => {
                if (!agreed) setShowRules(true);
                else {
                  setAgreed(false);
                }
              }}
            >
              <input
                type="checkbox"
                id="agreeCheck"
                checked={agreed}
                readOnly
                onClick={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  if (!agreed) setShowRules(true);
                  else setAgreed(false);
                }}
              />
              <label htmlFor="agreeCheck">
                I agree to the{' '}
                <a
                  href="#"
                  onClick={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setShowRules(true);
                  }}
                >
                  Terms & Conditions and Rules
                </a>
              </label>
            </div>
          ) : null}

          {reviewing ? (
            <button
              type="button"
              className={`${styles.btn} ${styles.btnSubmit}`}
              disabled={!canSubmit}
              onClick={handlePayment}
            >
              {busy ? (
                <>
                  <i className="fa-solid fa-spinner fa-spin" /> Initializing...
                </>
              ) : payMode === 'spot' || entryFee <= 0 ? (
                <>
                  Confirm Registration <i className="fa-solid fa-paper-plane" />
                </>
              ) : (
                <>
                  Pay & Submit Registration <i className="fa-solid fa-paper-plane" />
                </>
              )}
            </button>
          ) : null}
        </div>

        <aside className={styles.sidebarRules}>
          <span className={styles.sectionLabel}>Rules & Regulations</span>
          <h3 style={{ margin: '5px 0 15px 0', fontSize: 18 }}>Tournament Guidelines</h3>
          <div className={styles.rulesContentText}>{rulesText}</div>
          {templateUrl && templateDismissed ? (
            <img
              src={templateUrl}
              alt="Tournament Template"
              className={styles.sidebarTemplateImg}
              onClick={() => {
                setShowTemplate(true);
                setTemplateDismissed(false);
              }}
            />
          ) : null}
        </aside>
      </div>

      {showRules ? (
        <div className={`${styles.modalOverlay} ${styles.modalOverlayDark}`}>
          <div className={styles.rulesModalCard}>
            <div className={styles.rulesModalBody}>
              <h3 style={{ marginTop: 0, color: 'var(--primary)' }}>Rules & Regulations</h3>
              <div style={{ whiteSpace: 'pre-line', color: '#475569', fontSize: 14 }}>
                {rulesText}
              </div>
            </div>
            <div className={styles.rulesModalFooter}>
              <button
                type="button"
                className={`${styles.btn} ${styles.btnNext}`}
                style={{ width: '100%' }}
                onClick={() => {
                  setAgreed(true);
                  setShowRules(false);
                }}
              >
                I Have Read & Agree to the Rules
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

export default function TournamentRegisterPage() {
  return (
    <Suspense
      fallback={
        <div className={styles.page}>
          <div className={styles.disabledCard}>
            <h1>Loading…</h1>
          </div>
        </div>
      }
    >
      <RegisterInner />
    </Suspense>
  );
}
