// @ts-nocheck -- vendored AS SOURCE from websites/libs/attestation-view (the canonical
// shared attestation view; keep in sync on change). ts-nocheck: compiled here under
// dsh's stricter tsc flags which the canonical copy does not target; runtime is identical.
'use client';

import { useCallback, useEffect, useState } from 'react';
import { AttestationResultView } from './attestation-result-view';
import { AttestationStatusBadge, attestationStatusOf } from './badge';
import { useAttestation, type AttestationState } from '../use-attestation';
import { PRIVASYS_OID, type AttestationExpectations } from '../types';

// One target the composite view should attest. Mirrors the inputs to
// useAttestation, plus a human label and an optional kind hint used for
// the summary line ("AI container", "Tool: lightpanda", etc.).
export interface AttestationTargetConfig {
    /** Stable id for React keys. Typically the server name or "ai". */
    id: string;
    /** Display label, e.g. "AI container" or "Tool: lightpanda". */
    label: string;
    /** Optional one-line description shown under the label. */
    description?: string;
    /** mgmt-service /attest proxy URL for this enclave. */
    attestUrl: string;
    /** Attestation server /verify-quote URL. */
    verifyQuoteUrl?: string;
    /** Expected digests for green-badge stamping. */
    expectations?: AttestationExpectations;
}

export type AggregateAttestationStatus = 'verifying' | 'verified' | 'failed';

export interface CompositeAttestationViewProps {
    targets: AttestationTargetConfig[];
    /** When true, every row auto-inspects on mount. */
    autoInspect?: boolean;
    /** Bearer token used when calling each row's attest URL. Forwarded
     *  unchanged — use a thunk for short-lived tokens. */
    attestToken?: string | (() => Promise<string>);
    /** Bearer-token thunk used when calling each row's verify-quote URL.
     *  Distinct from attestToken because verify-quote (challenge mode)
     *  requires a token whose `aud` claim equals the attestation server
     *  identifier, while the attest URL takes the user's main session
     *  token. */
    verifyQuoteAuth?: () => Promise<string>;
    /** Called whenever the aggregate verification status changes.
     *  'verifying' while any row is still in flight; 'verified' when
     *  every row's quote AND digest checks pass; 'failed' otherwise. */

    onAggregateStatus?: (_status: AggregateAttestationStatus) => void;
}

/**
 * Per-target attestation verdict: the canonical roll-up of quote + digest
 * checks for a single enclave. Exported so consumers can render their own
 * status affordance (e.g. an inline badge co-located with a tool row)
 * against exactly the same logic the composite view uses.
 */
export interface AttestationSummary {
    ready: boolean;     // an inspection has completed (success or hard error)
    quoteOk: boolean;   // verify-quote returned ok
    digestsOk: boolean; // every supplied expectation matched its OID
    error?: string;
}

// Back-compat internal alias.
type RowSummary = AttestationSummary;

export function CompositeAttestationView({ targets, autoInspect = true, attestToken, verifyQuoteAuth, onAggregateStatus }: CompositeAttestationViewProps) {
    const [rows, setRows] = useState<Record<string, RowSummary>>({});

    const onRowSummary = useCallback((id: string, summary: RowSummary) => {
        setRows(prev => {
            const cur = prev[id];
            if (cur && cur.ready === summary.ready
                && cur.quoteOk === summary.quoteOk
                && cur.digestsOk === summary.digestsOk
                && cur.error === summary.error) {
                return prev;
            }
            return { ...prev, [id]: summary };
        });
    }, []);

    const total = targets.length;
    const ready = targets.filter(t => rows[t.id]?.ready).length;
    const allReady = ready === total && total > 0;
    const allOk = allReady && targets.every(t => {
        const r = rows[t.id];
        return r && r.quoteOk && r.digestsOk;
    });

    // Push aggregate status up to consumers (e.g. the sidebar pill).
    // 'verifying' covers the zero-target bootstrap case too so the
    // sidebar doesn't flash green before any row has reported.
    useEffect(() => {
        if (!onAggregateStatus) return;
        if (total === 0 || !allReady) {
            onAggregateStatus('verifying');
        } else if (allOk) {
            onAggregateStatus('verified');
        } else {
            onAggregateStatus('failed');
        }
    }, [onAggregateStatus, total, allReady, allOk]);

    return (
        <div className='flex flex-col gap-6'>
            <SummaryBanner total={total} ready={ready} allOk={allOk} />
            {targets.map(t => (
                <AttestationRow
                    key={t.id}
                    target={t}
                    autoInspect={autoInspect}
                    attestToken={attestToken}
                    verifyQuoteAuth={verifyQuoteAuth}
                    onSummary={s => onRowSummary(t.id, s)}
                />
            ))}
        </div>
    );
}

function SummaryBanner({ total, ready, allOk }: { total: number; ready: number; allOk: boolean }) {
    if (total === 0) {
        return (
            <div className='rounded-xl border border-black/10 p-4 text-sm text-black/60 dark:border-white/10 dark:text-white/60'>
                Nothing to attest yet.
            </div>
        );
    }
    if (ready < total) {
        return (
            <div className='rounded-xl border border-black/10 p-4 text-sm text-black/60 dark:border-white/10 dark:text-white/60'>
                Verifying {ready}/{total} components...
            </div>
        );
    }
    if (allOk) {
        return (
            <div className='rounded-xl border border-emerald-500/30 bg-emerald-50/50 p-4 text-sm text-emerald-800 dark:border-emerald-400/20 dark:bg-emerald-900/10 dark:text-emerald-200'>
                <strong>All {total} components verified.</strong> Hardware quotes valid; every
                expected workload, model and tool-set digest matches.
            </div>
        );
    }
    return (
        <div className='rounded-xl border border-red-300/40 bg-red-50/40 p-4 text-sm text-red-700 dark:border-red-500/20 dark:bg-red-900/10 dark:text-red-300'>
            <strong>One or more components failed attestation.</strong> See the per-row details
            below for which quotes did not verify or which digests did not match.
        </div>
    );
}

function AttestationRow({
    target,
    autoInspect,
    attestToken,
    verifyQuoteAuth,
    onSummary
}: {
    target: AttestationTargetConfig;
    autoInspect: boolean;
    attestToken?: string | (() => Promise<string>);
    verifyQuoteAuth?: () => Promise<string>;
    onSummary: (_summary: RowSummary) => void;
}) {
    const [state, actions] = useAttestation({
        attestUrl: target.attestUrl,
        verifyQuoteUrl: target.verifyQuoteUrl,
        token: attestToken,
        verifyQuoteToken: verifyQuoteAuth,
        autoInspect: autoInspect && Boolean(target.attestUrl),
        autoVerifyQuote: autoInspect && Boolean(target.verifyQuoteUrl)
    });

    // Collapsed by default — each row is long, the user opens what they
    // care about. Background attestation still runs (autoInspect above),
    // so the header status pill is accurate without expanding.
    const [open, setOpen] = useState(false);

    // Compute the per-row summary on every render and notify the parent.
    // useAttestation already debounces network work; this is cheap.
    const summary = computeAttestationSummary(state, target.expectations);
    useEffect(() => {
        onSummary(summary);
    }, [onSummary, summary.ready, summary.quoteOk, summary.digestsOk, summary.error]);

    const panelId = `attest-row-${target.id}`;
    return (
        <section className='rounded-xl border border-black/10 dark:border-white/10'>
            <button
                type='button'
                onClick={() => setOpen(o => !o)}
                aria-expanded={open}
                aria-controls={panelId}
                className={`flex w-full items-center justify-between gap-3 p-4 text-left transition-colors hover:bg-black/[0.02] dark:hover:bg-white/[0.02] ${open ? 'border-b border-black/10 dark:border-white/10' : ''}`}
            >
                <div className='flex min-w-0 items-center gap-2'>
                    <ChevronIcon open={open} />
                    <div className='min-w-0'>
                        <div className='truncate text-sm font-semibold text-[var(--color-text-primary)]'>
                            {target.label}
                        </div>
                        {target.description && (
                            <div className='truncate text-xs text-[var(--color-text-secondary)]'>
                                {target.description}
                            </div>
                        )}
                    </div>
                </div>
                <RowStatus summary={summary} />
            </button>
            {open && (
                <div id={panelId} className='p-4'>
                    {!target.attestUrl ? (
                        <div className='text-sm text-black/60 dark:text-white/60'>
                            No attest URL configured for this component.
                        </div>
                    ) : state.error && !state.result ? (
                        <div className='space-y-3 text-sm text-red-700 dark:text-red-300'>
                            <div>{state.error}</div>
                            <button
                                type='button'
                                onClick={() => void actions.inspect()}
                                className='rounded-lg border border-black/10 px-3 py-1.5 text-xs font-medium hover:bg-black/5 dark:border-white/10 dark:hover:bg-white/5'
                            >
                                Retry
                            </button>
                        </div>
                    ) : !state.result ? (
                        <div className='text-sm text-black/60 dark:text-white/60'>Inspecting...</div>
                    ) : (
                        <AttestationResultView
                            result={state.result}
                            quoteVerify={state.quoteVerify}
                            quoteVerifying={state.verifying}
                            quoteVerifyError={state.quoteVerifyError}
                            expectations={target.expectations}
                            loading={state.loading}
                            challenge={state.challenge}
                            onChallengeChange={actions.setChallenge}
                            onRegenerateChallenge={actions.regenerateChallenge}
                            onRefresh={() => void actions.inspect()}
                            onReset={() => void actions.newChallenge()}
                            verifyQuoteUrl={target.verifyQuoteUrl}
                        />
                    )}
                </div>
            )}
        </section>
    );
}

function ChevronIcon({ open }: { open: boolean }) {
    return (
        <svg
            aria-hidden='true'
            className={`h-4 w-4 shrink-0 text-[var(--color-text-secondary)] transition-transform ${open ? 'rotate-90' : ''}`}
            viewBox='0 0 20 20'
            fill='currentColor'
        >
            <path d='M7.05 4.55a1 1 0 0 1 1.4 0l4 4a1 1 0 0 1 0 1.4l-4 4a1 1 0 1 1-1.4-1.4L10.3 9.25 7.05 6a1 1 0 0 1 0-1.45z' />
        </svg>
    );
}

function RowStatus({ summary }: { summary: RowSummary }) {
    const { status, reason } = attestationStatusOf(summary, true);
    return <AttestationStatusBadge status={status} reason={reason} />;
}

/**
 * Roll up a {@link useAttestation} state into an {@link AttestationSummary}:
 * quote-signature validity AND every supplied expected digest matching its
 * OID extension. Exported for consumers that render their own status UI.
 */
export function computeAttestationSummary(
    state: AttestationState,
    expectations: AttestationExpectations | undefined
): AttestationSummary {
    const ready = Boolean(state.result || state.error);
    if (!ready) {
        return { ready: false, quoteOk: false, digestsOk: false };
    }
    if (state.error && !state.result) {
        return { ready: true, quoteOk: false, digestsOk: false, error: state.error };
    }
    const quoteOk = !state.quoteVerifyError
        && (!state.verifying)
        && (state.quoteVerify ? state.quoteVerify.success : true);

    let digestsOk = true;
    if (expectations && state.result) {
        // A co-located container app presents its OWN leaf: its identity
        // lands in `app_extensions`, while `extensions` carries the hosting
        // enclave's (and is empty for some app types). Look the app's arc up
        // in app_extensions FIRST, falling back to the enclave's — otherwise
        // every co-located app reads as "digest mismatch" even when its
        // digest matches exactly, which is what the expanded view already
        // shows correctly (it renders app_extensions).
        const appExts = state.result.app_extensions ?? [];
        const exts = state.result.extensions ?? [];
        const find = (list: Array<{ oid: string; value_hex?: string }>, oid: string) =>
            list.find((e) => e.oid === oid)?.value_hex?.toLowerCase();
        const get = (oid: string) => find(appExts, oid) ?? find(exts, oid);
        const norm = (v?: string) => (v || '').toLowerCase().replace(/^0x/, '');
        // RA-TLS v2 OID scheme: app code hash 4.2, app id 4.1, and the
        // app-registered AI model / tools digests under the 5.4.* arc.
        const checks: Array<[string[], string | undefined]> = [
            [[PRIVASYS_OID.APP_CODE_HASH], expectations.workloadImageDigest],
            [[PRIVASYS_OID.MODEL_DIGEST], expectations.modelDigest],
            [[PRIVASYS_OID.APP_ID], expectations.appId],
            [[PRIVASYS_OID.TOOLS_DIGEST], expectations.toolsDigest]
        ];
        for (const [oids, want] of checks) {
            if (!want) continue;
            const have = oids.map(get).find(Boolean);
            if (!have || have !== norm(want)) {
                digestsOk = false;
                break;
            }
        }
    }
    return { ready: true, quoteOk, digestsOk };
}
