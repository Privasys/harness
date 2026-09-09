// @ts-nocheck -- vendored AS SOURCE from websites/libs/attestation-view (the canonical
// shared attestation view; keep in sync on change). ts-nocheck: compiled here under
// dsh's stricter tsc flags which the canonical copy does not target; runtime is identical.
'use client';

import { useEffect, useMemo, useState } from 'react';
import type {
    AttestationExpectations,
    AttestationExtension,
    AttestationMode,
    AttestationResult,
    GPUAttestationResult,
    OsRelease,
    WorkloadRelease,
    QuoteVerifyResult
} from '../types';
import { PRIVASYS_OID, TEXT_OIDS } from '../types';
import { hexToPrintableText } from '../internal/use-copy';
import { verifyReportData } from '../use-attestation';
import { FieldRow } from './field-row';
import { Badge } from './badge';

// Read-only renderer for an AttestationResult.
//
// Sections rendered (in order):
//   1. Challenge-mode banner (only when result.challenge_mode)
//   2. TLS connection summary
//   3. x.509 certificate fields
//   4. Attestation quote fields (with the RA-TLS v2 report_data recipe
//      reproduced in the browser, in both deterministic and challenge mode)
//   5. Platform OID extensions
//   6. Workload OID extensions (if app_extensions present)
//
// RTMR event-log replay verification is intentionally out of scope
// here - it lives in a separate component (RtmrVerifier) in the
// developer-portal and will be ported as a follow-up.

const OID_DESCRIPTIONS: Record<string, string> = {
    'Config Merkle Root':
        'Hash of the enclave configuration tree. Changes if any config parameter is modified.',
    'Egress CA Hash':
        'Hash of the CA certificate used for egress TLS connections from the enclave.',
    'Runtime Version Hash':
        'Hash identifying the exact runtime version running inside the enclave.',
    'Combined Workloads Hash':
        'Aggregate hash of all loaded WASM workloads. Proves which code is running.',
    'DEK Origin':
        'Data Encryption Key origin - indicates how the enclave\'s encryption key was derived.',
    'Attestation Servers Hash':
        'Hash of the attestation server list the enclave trusts for quote verification.',
    'Workload Config Merkle Root':
        'Merkle root of this specific workload\'s configuration.',
    'Workload Code Hash':
        'SHA-256 hash of the compiled WASM bytecode for this workload.',
    'Workload Image Digest':
        'SHA-256 digest of the OCI image this workload was loaded from.',
    'Workload Image Ref':
        'Container image reference from which the workload was loaded.',
    'Container Image Ref':
        'Container image reference from which the workload was loaded.',
    'Workload Key Source':
        'Indicates how the workload\'s encryption keys are sourced and managed.',
    'Volume Encryption':
        'How this workload\'s encrypted data volume key was provisioned (vault-backed or operator-supplied).',
    'Workload Configuration Hash':
        'Hash of the workload\'s configuration metadata; reflects which settings and secrets are configured, never their values.',
    'Workload Permissions Hash':
        'Hash of the security permissions granted to this workload.',
    'Workload App ID':
        'Platform-assigned app identity (management app id), stamped by the measured runtime so it cannot be forged by the workload.',
    'AI Tools Digest':
        'SHA-256 over the canonical JSON of the MCP tool servers this AI enclave exposes.',
    'Image Profile':
        'Build flavor of the enclave VM image: "production" or "dev". Verifiers reject dev images unless explicitly opted in.',
    'Attested Dependency Set':
        'The fixed set of cross-enclave dependency identities this workload is pinned to, written by the runtime.',
    'Enclave Instance ID':
        'Platform-assigned identity of this enclave instance, stamped by the measured runtime.',
    'Authenticated State Root':
        'Root of the enclave\'s authenticated persistent state at the time the certificate was issued.'
};

export function AttestationResultView({
    result,
    quoteVerify,
    quoteVerifying,
    quoteVerifyError,
    expectations,
    onRefresh,
    onReset,
    extra,
    challenge,
    onChallengeChange,
    onRegenerateChallenge,
    loading,
    verifyQuoteUrl
}: {
    result: AttestationResult;
    quoteVerify?: QuoteVerifyResult | null;
    /** Set while the quote signature is being verified by the
     *  attestation server. Drives the spinner badge in the quote
     *  section header. */
    quoteVerifying?: boolean;
    /** Surfaced as a yellow warning badge in the quote section. */
    quoteVerifyError?: string | null;
    /** Optional expected hash values; when provided the matching
     *  workload extension rows render a green/red verification badge. */
    expectations?: AttestationExpectations;
    onRefresh?: () => void;
    onReset?: () => void;
    /**
     * Optional extra content rendered between the certificate section
     * and the platform-extensions section. Used by the developer
     * portal to drop in its RTMR event-log replay component.
     */
    extra?: React.ReactNode;
    /** Current challenge (hex). When provided alongside
     *  onChallengeChange / onRegenerateChallenge, an inline editor is
     *  rendered next to the action bar so the user can replay or
     *  modify the challenge without leaving the result view. */
    challenge?: string;
    onChallengeChange?: (next: string) => void;
    onRegenerateChallenge?: () => void;
    /** True while a re-inspect is in flight - disables Refresh and
     *  surfaces a spinner. */
    loading?: boolean;
    /** Full URL of the attestation server's verify-quote endpoint
     *  (e.g. `https://as.privasys.org/verify-quote`). When provided,
     *  the copy-paste verification snippet at the bottom of the view
     *  targets this URL. When omitted the snippet renders a clear
     *  placeholder so users know what to substitute. */
    verifyQuoteUrl?: string;
}) {
    const reportDataCheck = useReportDataCheck(result);
    const showChallengeEditor = challenge != null && (onChallengeChange || onRegenerateChallenge);

    return (
        <div className='space-y-6'>
            {result.challenge_mode && result.challenge && (
                <ChallengeBanner result={result} reportDataCheck={reportDataCheck} />
            )}

            {(onRefresh || onReset || showChallengeEditor) && (
                <div className='space-y-3'>
                    {showChallengeEditor && (
                        <ChallengeEditor
                            challenge={challenge!}
                            onChange={onChallengeChange}
                            onRegenerate={onRegenerateChallenge}
                            disabled={loading}
                        />
                    )}
                    <div className='flex items-center gap-3'>
                        {onRefresh && (
                            <button
                                type='button'
                                onClick={onRefresh}
                                disabled={loading}
                                className='rounded-lg border border-black/10 dark:border-white/10 px-4 py-2 text-sm font-medium hover:bg-black/5 dark:hover:bg-white/5 disabled:opacity-40'
                            >
                                {loading ? 'Refreshing...' : 'Refresh'}
                            </button>
                        )}
                        {result.pem && (
                            <DownloadPemButton pem={result.pem} />
                        )}
                        {onReset && (
                            <button
                                type='button'
                                onClick={onReset}
                                disabled={loading}
                                className='ml-auto rounded-lg border border-black/10 dark:border-white/10 px-4 py-2 text-sm font-medium hover:bg-black/5 dark:hover:bg-white/5 disabled:opacity-40'
                            >
                                New Challenge
                            </button>
                        )}
                    </div>
                </div>
            )}

            <Section title='TLS Connection'>
                <div className='grid grid-cols-2 gap-3 text-sm'>
                    <Stat label='Protocol' value={result.tls.version} />
                    <Stat label='Cipher Suite' value={result.tls.cipher_suite} />
                </div>
            </Section>

            <Section title='x.509 Certificate'>
                <div className='space-y-3'>
                    <FieldRow label='Subject' value={result.certificate.subject} description='The entity this certificate identifies.' />
                    <FieldRow label='Issuer' value={result.certificate.issuer} description='Self-signed for enclaves.' />
                    <FieldRow label='Serial Number' value={result.certificate.serial_number} />
                    <FieldRow label='Valid From' value={result.certificate.not_before} />
                    <FieldRow label='Valid Until' value={result.certificate.not_after} />
                    <FieldRow label='Signature Algorithm' value={result.certificate.signature_algorithm} />
                    <FieldRow
                        label='Public Key SHA-256'
                        value={result.certificate.public_key_sha256}
                        description="SHA-256 fingerprint of the subject's public key."
                    />
                </div>
            </Section>

            {result.quote && (
                <QuoteSection
                    quote={result.quote}
                    quoteVerify={quoteVerify}
                    quoteVerifying={quoteVerifying}
                    quoteVerifyError={quoteVerifyError}
                    reportDataCheck={reportDataCheck}
                    challengeMode={result.challenge_mode}
                    osRelease={result.os_release}
                />
            )}

            {result.gpu_attestation && (
                <GPUSection gpu={result.gpu_attestation} />
            )}

            {extra}

            {result.extensions.length > 0 && (
                <ExtensionsSection
                    title='Platform Attestation Extensions'
                    description='Platform-level x.509 extensions (OIDs 1.x, 2.x and 3.x). These bind the enclave runtime, configuration and state to the certificate key the quote commits to.'
                    extensions={result.extensions}
                    expectations={expectations}
                    cwasmHash={result.cwasm_hash}
                />
            )}

            {result.app_extensions && result.app_extensions.length > 0 && (
                <ExtensionsSection
                    title='Workload Attestation Extensions'
                    description='Per-workload x.509 extensions (OIDs 4.x to 7.x). These bind the specific application code, model and permissions to a dedicated attestation.'
                    extensions={result.app_extensions}
                    accent
                    expectations={expectations}
                    cwasmHash={result.cwasm_hash}
                    workloadRelease={result.workload_release}
                />
            )}

            {result.pem && (
                <PemSection
                    title='Platform PEM Certificate'
                    pem={result.pem}
                    copyKey='platform-pem'
                    downloadName='platform-certificate.pem'
                />
            )}

            {result.app_pem && (
                <PemSection
                    title='Workload PEM Certificate'
                    pem={result.app_pem}
                    copyKey='workload-pem'
                    downloadName='workload-certificate.pem'
                    accent
                />
            )}

            {result.certificate?.public_key_sha256 && result.quote?.report_data && attestationModeOf(result) && (
                <VerificationCodeSection
                    pubKeySha256={result.certificate.public_key_sha256}
                    mode={attestationModeOf(result)!}
                    quoteTime={result.quote.quote_time}
                    contextHex={result.quote.context}
                    hctxB64={result.quote.hctx}
                    gpuEvidenceB64={result.quote.gpu_evidence_base64}
                    reportData={result.quote.report_data}
                />
            )}

            {result.quote?.raw_base64 && !result.quote.is_mock && (
                <QuoteVerificationCodeSection
                    rawBase64={result.quote.raw_base64}
                    verifyQuoteUrl={verifyQuoteUrl}
                />
            )}
        </div>
    );
}

// --- internals ---------------------------------------------------------

type ReportDataCheck =
    | { state: 'idle' }
    | { state: 'verifying' }
    | { state: 'match'; computed: string; actual: string }
    | { state: 'mismatch'; computed: string; actual: string }
    | { state: 'error' };

// The attestation mode the verifying service used. `quote.attestation` is
// authoritative; older payloads without it are inferred from challenge_mode.
function attestationModeOf(result: AttestationResult): AttestationMode | undefined {
    const q = result.quote;
    if (!q) return undefined;
    if (q.attestation === 'challenge' || q.attestation === 'deterministic') return q.attestation;
    if (result.challenge_mode) return q.context && q.hctx ? 'challenge' : undefined;
    return q.quote_time ? 'deterministic' : undefined;
}

function useReportDataCheck(result: AttestationResult): ReportDataCheck {
    const [check, setCheck] = useState<ReportDataCheck>({ state: 'idle' });

    useEffect(() => {
        const quote = result.quote;
        const reportData = quote?.report_data;
        const pubKey = result.certificate?.public_key_sha256;
        const mode = attestationModeOf(result);
        if (!quote || !reportData || !pubKey || !mode) {
            setCheck({ state: 'idle' });
            return;
        }
        // In challenge mode the context is the challenge the browser chose
        // (when it is 32 bytes) and hctx is the TLS exporter value of the
        // verifying service's connection. Prefer the browser's own challenge
        // as the context so a service that echoed a different one mismatches.
        const contextHex = mode === 'challenge'
            ? (result.challenge && result.challenge.length === 64 ? result.challenge : quote.context)
            : undefined;
        let cancelled = false;
        setCheck({ state: 'verifying' });
        verifyReportData({
            pubKeySha256Hex: pubKey,
            reportDataHex: reportData,
            mode,
            quoteTime: quote.quote_time,
            contextHex,
            hctxB64: quote.hctx,
            // GPU enclaves (confidential-ai) append SHA-256(GPU evidence) to
            // the binding; without it the check falsely reports a mismatch.
            gpuEvidenceB64: quote.gpu_evidence_base64
        }).then((r) => {
            if (cancelled) return;
            if (r.status === 'match') {
                setCheck({ state: 'match', computed: r.computed!, actual: r.actual! });
            } else if (r.status === 'mismatch') {
                setCheck({ state: 'mismatch', computed: r.computed!, actual: r.actual! });
            } else {
                setCheck({ state: 'error' });
            }
        });
        return () => {
            cancelled = true;
        };
    }, [result]);

    return check;
}

function ChallengeBanner({
    result,
    reportDataCheck
}: {
    result: AttestationResult;
    reportDataCheck: ReportDataCheck;
}) {
    const tone =
        reportDataCheck.state === 'match'
            ? 'border-emerald-200/50 bg-emerald-50/40 text-emerald-900 dark:border-emerald-500/20 dark:bg-emerald-900/10 dark:text-emerald-200'
            : reportDataCheck.state === 'mismatch' || reportDataCheck.state === 'error'
                ? 'border-red-200/50 bg-red-50/40 text-red-900 dark:border-red-500/20 dark:bg-red-900/10 dark:text-red-200'
                : 'border-amber-200/50 bg-amber-50/40 text-amber-900 dark:border-amber-500/20 dark:bg-amber-900/10 dark:text-amber-200';
    const iconColor =
        reportDataCheck.state === 'match'
            ? 'text-emerald-600 dark:text-emerald-400'
            : reportDataCheck.state === 'mismatch' || reportDataCheck.state === 'error'
                ? 'text-red-600 dark:text-red-400'
                : 'text-amber-600 dark:text-amber-400';
    return (
        <section className={`rounded-xl border p-4 ${tone}`}>
            <div className='flex items-start gap-3'>
                <LockIcon className={`mt-0.5 h-4 w-4 shrink-0 ${iconColor}`} />
                <div className='min-w-0 flex-1'>
                    <div className='mb-1 flex flex-wrap items-center gap-2'>
                        <h3 className='text-xs font-semibold'>Challenge Mode Active</h3>
                        {reportDataCheck.state === 'verifying' && (
                            <Badge tone='neutral'>
                                <Spinner /> Verifying...
                            </Badge>
                        )}
                        {reportDataCheck.state === 'match' && (
                            <Badge tone='ok'>{'\u2713 Match - connection binding verified'}</Badge>
                        )}
                        {reportDataCheck.state === 'mismatch' && (
                            <Badge tone='err'>✗ Mismatch</Badge>
                        )}
                        {reportDataCheck.state === 'error' && (
                            <Badge tone='err'>⚠ Verification error</Badge>
                        )}
                    </div>
                    {reportDataCheck.state === 'mismatch' || reportDataCheck.state === 'error' ? (
                        <p className='mb-2 text-[11px] font-medium'>
                            {reportDataCheck.state === 'error'
                                ? 'The report data could not be verified in your browser (the response is missing the context or the exporter value, or they have the wrong length). The quote’s binding to the verifier’s connection could not be confirmed: do not trust this enclave’s identity until this is resolved.'
                                : 'Report data mismatch. The quote’s ReportData is not SHA-512( SHA-256(public_key) || context || hctx ) for this certificate, your challenge and the exporter value the verifying service supplied, so the quote was not bound to that connection. This is a serious failure: do not trust this enclave’s identity. See the hash comparison in the quote section below.'}
                        </p>
                    ) : (
                        <p className='mb-2 text-[11px] opacity-70'>
                            The verifying service opened a TLS 1.3 connection to the enclave and asked for
                            evidence using your challenge as the context. The enclave folded that context
                            and the connection&rsquo;s TLS exporter value (hctx) into the quote&rsquo;s ReportData.
                            Your browser reproduces ReportData from the context you chose and the exporter
                            value the service supplied, which shows the quote was bound to that
                            verifier&rsquo;s connection and to this certificate key.
                        </p>
                    )}
                    <div className='mb-1 text-[11px] opacity-70'>Challenge sent:</div>
                    <code className='block break-all rounded bg-black/5 dark:bg-white/10 px-2 py-1 font-mono text-[11px]'>
                        {result.challenge}
                    </code>
                </div>
            </div>
        </section>
    );
}

function LockIcon({ className }: { className?: string }) {
    return (
        <svg viewBox='0 0 20 20' fill='currentColor' aria-hidden='true' className={className}>
            <path fillRule='evenodd' clipRule='evenodd' d='M10 1a4 4 0 0 0-4 4v3H5a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-7a2 2 0 0 0-2-2h-1V5a4 4 0 0 0-4-4Zm2 7V5a2 2 0 1 0-4 0v3h4Z' />
        </svg>
    );
}

function Spinner() {
    return (
        <svg className='h-3 w-3 animate-spin' viewBox='0 0 24 24' aria-hidden='true'>
            <circle className='opacity-25' cx='12' cy='12' r='10' stroke='currentColor' strokeWidth='4' fill='none' />
            <path className='opacity-75' fill='currentColor' d='M4 12a8 8 0 0 1 8-8v4a4 4 0 0 0-4 4H4Z' />
        </svg>
    );
}

function ChallengeEditor({
    challenge,
    onChange,
    onRegenerate,
    disabled
}: {
    challenge: string;
    onChange?: (next: string) => void;
    onRegenerate?: () => void;
    disabled?: boolean;
}) {
    return (
        <div className='rounded-lg border border-black/10 p-3 dark:border-white/10'>
            <label className='block text-[10px] uppercase tracking-wide text-black/40 dark:text-white/40'>
                Challenge context (hex)
            </label>
            <div className='mt-1 flex items-center gap-2'>
                <input
                    type='text'
                    value={challenge}
                    onChange={(e) => onChange?.(e.target.value)}
                    spellCheck={false}
                    disabled={!onChange || disabled}
                    className='min-w-0 flex-1 rounded border border-black/10 bg-white/60 px-2 py-1 font-mono text-[11px] dark:border-white/10 dark:bg-black/20'
                />
                {onRegenerate && (
                    <button
                        type='button'
                        onClick={onRegenerate}
                        disabled={disabled}
                        className='shrink-0 rounded border border-black/10 px-2 py-1 text-[11px] hover:bg-black/5 disabled:opacity-40 dark:border-white/10 dark:hover:bg-white/5'
                    >
                        Regenerate
                    </button>
                )}
            </div>
        </div>
    );
}

function QuoteSection({
    quote,
    quoteVerify,
    quoteVerifying,
    quoteVerifyError,
    reportDataCheck,
    challengeMode,
    osRelease
}: {
    quote: NonNullable<AttestationResult['quote']>;
    quoteVerify?: QuoteVerifyResult | null;
    quoteVerifying?: boolean;
    quoteVerifyError?: string | null;
    reportDataCheck: ReportDataCheck;
    challengeMode: boolean;
    osRelease?: OsRelease;
}) {
    const fields = useMemo(() => {
        const rows: Array<{ label: string; value: string; description?: string }> = [
            { label: 'Quote Type', value: quote.type, description: 'Attestation evidence format exchanged on the TLS connection after the handshake (RA-TLS v2).' }
        ];
        if (quote.tee) rows.push({ label: 'TEE', value: quote.tee, description: 'Evidence family reported by the enclave.' });
        if (quote.attestation) {
            rows.push({
                label: 'Attestation Mode',
                value: quote.attestation,
                description: quote.attestation === 'challenge'
                    ? 'Challenge mode: the quote is bound to the challenge context and to the TLS exporter value of the verifying service\'s connection.'
                    : 'Deterministic mode: the quote is bound to the certificate key and the quote minute, and may be served from the enclave\'s cache for up to 24 hours.'
            });
        }
        if (quote.quote_time) {
            rows.push({
                label: 'Quote Time',
                value: quote.quote_time,
                description: challengeMode
                    ? 'Minute at which the quote was minted (UTC).'
                    : 'Minute at which the quote was minted (UTC). In deterministic mode these exact bytes are folded into ReportData.'
            });
        }
        if (quote.context) rows.push({ label: 'Context', value: quote.context, description: 'The 32-byte challenge context the enclave used; equals the challenge you sent when it is 32 bytes.' });
        if (quote.hctx) rows.push({ label: 'Exporter Value (hctx)', value: quote.hctx, description: 'TLS exporter value (RFC 8446 section 7.5) of the verifying service\'s connection for this context, base64. Browsers expose no TLS exporter, so the service that held the connection supplies it.' });
        if (quote.format) rows.push({ label: 'Format', value: quote.format });
        if (quote.version != null) rows.push({ label: 'Version', value: String(quote.version) });
        if (quote.mr_enclave) rows.push({ label: 'MRENCLAVE', value: quote.mr_enclave, description: 'Hash of the enclave code and initial data.' });
        if (quote.mr_signer) rows.push({ label: 'MRSIGNER', value: quote.mr_signer, description: 'Hash of the enclave signer\'s public key.' });
        if (quote.mr_td) rows.push({ label: 'MR_TD', value: quote.mr_td, description: 'Measurement of the Trust Domain (TDX VM image and configuration).' });
        if (quote.rtmr0) rows.push({ label: 'RTMR[0]', value: quote.rtmr0, description: 'TD firmware (TDVF) and its configuration.' });
        if (quote.rtmr1) rows.push({ label: 'RTMR[1]', value: quote.rtmr1, description: 'EFI boot path: shim and GRUB binaries.' });
        if (quote.rtmr2) rows.push({ label: 'RTMR[2]', value: quote.rtmr2, description: 'OS boot: kernel, initrd and kernel command line (including dm-verity root hash).' });
        if (quote.rtmr3) rows.push({ label: 'RTMR[3]', value: quote.rtmr3, description: 'Application-defined measurements.' });
        if (quote.report_data) {
            rows.push({
                label: 'Report Data',
                value: quote.report_data,
                description: (challengeMode
                    ? 'SHA-512( SHA-256(public_key_DER) || context || hctx )'
                    : 'SHA-512( SHA-256(public_key_DER) || quote_time )')
                    + (quote.gpu_evidence_base64 ? ' with SHA-256(gpu_evidence) appended.' : '.')
            });
        }
        if (quote.gpu_evidence_base64) {
            rows.push({
                label: 'GPU Evidence',
                value: quote.gpu_evidence_base64,
                description: 'NVIDIA Confidential-Computing evidence envelope (SPDM report + certificate chain), base64, exchanged with the quote; its SHA-256 is folded into ReportData.'
            });
        }
        if (quoteVerify?.tcbStatus) {
            rows.push({
                label: 'TCB Status',
                value: quoteVerify.tcbStatus,
                description: 'Intel platform TCB status from PCS collateral. UpToDate and SWHardeningNeeded are the secure floor; other statuses require an explicit policy relaxation; Revoked is never accepted.'
            });
        }
        return rows;
    }, [quote, challengeMode, quoteVerify]);

    // Anchor the merged Enclave OS release + measurements verdict tag next to
    // the measurement rows the release is actually verified against — MRENCLAVE
    // for SGX; RTMR[1] and RTMR[2] for TDX (the EFI/OS-boot registers compared
    // to the release's Predicted RTMRs). NOT MR_TD, whose value the release
    // check does not gate on. Falls back to the header when no such row exists.
    const osReleaseAnchors: string[] = quote.mr_enclave
        ? ['MRENCLAVE']
        : quote.mr_td
            ? (['RTMR[1]', 'RTMR[2]'] as const).filter((l) => (l === 'RTMR[1]' ? quote.rtmr1 : quote.rtmr2))
            : [];
    const reportDataMismatch = reportDataCheck.state === 'mismatch' || reportDataCheck.state === 'error';

    return (
        <Section
            title={quote.type || 'Attestation Quote'}
            badge={
                <>
                    {reportDataMismatch && (
                        <Badge tone='err'>{'\u2717 report data mismatch'}</Badge>
                    )}
                    {quote.is_mock && (
                        <Badge tone='warn'>Mock</Badge>
                    )}
                    {quoteVerifying && (
                        <Badge tone='neutral'><Spinner /> Verifying...</Badge>
                    )}
                    {quoteVerify?.success && (
                        <Badge tone='ok'>{`\u2713 Verified - ${quoteVerify.message || `${quoteVerify.teeType?.toUpperCase() || 'Quote'} signature valid`}`}</Badge>
                    )}
                    {quoteVerify && !quoteVerify.success && (
                        <Badge tone='err'>{`\u2717 ${quoteVerify.error || 'Verification failed'}`}</Badge>
                    )}
                    {quoteVerify?.tcbStatus && (
                        <Badge tone={quoteVerify.tcbStatus === 'Revoked' ? 'err' : (quoteVerify.tcbStatus === 'UpToDate' || quoteVerify.tcbStatus === 'SWHardeningNeeded') ? 'ok' : 'warn'}>
                            {`TCB: ${quoteVerify.tcbStatus}`}
                        </Badge>
                    )}
                    {quoteVerifyError && (
                        <Badge tone='warn'>{`\u26a0 ${quoteVerifyError}`}</Badge>
                    )}
                    {osRelease && osReleaseAnchors.length === 0 && <OsReleaseBadge osRelease={osRelease} />}
                </>
            }
        >
            <div className='space-y-3'>
                {fields.map((f) => (
                    <FieldRow
                        key={f.label}
                        label={f.label}
                        value={f.value}
                        description={f.description}
                        badge={
                            <>
                                {f.label === 'Report Data' && reportDataCheck.state === 'match'
                                    ? <Badge tone='ok'>✓ Verified</Badge>
                                    : f.label === 'Report Data' && reportDataCheck.state === 'mismatch'
                                        ? <Badge tone='err'>✗ Mismatch</Badge>
                                        : f.label === 'Report Data' && reportDataCheck.state === 'error'
                                            ? <Badge tone='err'>⚠ Verification error</Badge>
                                            : f.label === 'Report Data' && reportDataCheck.state === 'verifying'
                                                ? <Badge tone='neutral'>Verifying...</Badge>
                                                : null}
                                {osRelease && osReleaseAnchors.includes(f.label) && (
                                    <OsReleaseBadge osRelease={osRelease} />
                                )}
                            </>
                        }
                    >
                        {f.label === 'Report Data' && reportDataCheck.state === 'mismatch' && (
                            <div className='mt-2 space-y-1.5 rounded-lg border border-red-200/50 bg-red-50/50 p-3 dark:border-red-800/30 dark:bg-red-900/10'>
                                <p className='text-[11px] font-medium text-red-700 dark:text-red-400'>
                                    {challengeMode
                                        ? 'Report data does not match this certificate, your challenge and the supplied exporter value.'
                                        : 'Report data does not match this certificate and quote time.'}
                                </p>
                                <p className='text-[11px] text-red-700/80 dark:text-red-400/80'>
                                    {challengeMode
                                        ? 'Expected SHA-512( SHA-256(public_key) || context || hctx ). A mismatch means the quote was not bound to the verifying service\'s connection; do not trust this enclave\'s identity.'
                                        : 'Expected SHA-512( SHA-256(public_key) || quote_time ). A mismatch means the quote does not commit to this certificate key and quote minute; do not trust this enclave\'s identity.'}
                                </p>
                                <DebugLine label='Computed' value={reportDataCheck.computed} />
                                <DebugLine label='Actual' value={reportDataCheck.actual} />
                            </div>
                        )}
                    </FieldRow>
                ))}
            </div>
        </Section>
    );
}

function GPUSection({ gpu }: { gpu: GPUAttestationResult }) {
    const fields = useMemo(() => {
        const rows: Array<{ label: string; value: string; description?: string }> = [];
        if (gpu.driver) rows.push({ label: 'Driver', value: gpu.driver, description: 'NVIDIA driver version reported in the signed attestation report.' });
        if (gpu.vbios) rows.push({ label: 'VBIOS', value: gpu.vbios, description: 'GPU VBIOS version reported in the signed attestation report.' });
        if (gpu.cc_environment) rows.push({ label: 'CC Environment', value: gpu.cc_environment, description: 'Confidential-Computing environment (PRODUCTION means production silicon, not a debug/simulation part).' });
        if (gpu.gpu_uuid) rows.push({ label: 'GPU UUID', value: gpu.gpu_uuid, description: 'Unique identifier of the attesting GPU.' });
        return rows;
    }, [gpu]);

    return (
        <Section
            title='NVIDIA GPU (Confidential Computing)'
            badge={
                <>
                    {gpu.verified
                        ? <Badge tone='ok'>{'✓ Verified - genuine NVIDIA GPU, authentic nonce-bound report'}</Badge>
                        : <Badge tone='err'>{`✗ ${gpu.error || 'GPU attestation failed'}`}</Badge>}
                    {gpu.verified && (
                        gpu.measurements_verified
                            ? <Badge tone='ok'>{'✓ Measurements matched (RIM)'}</Badge>
                            : <Badge tone='warn'>Measurements unverified</Badge>
                    )}
                </>
            }
        >
            <p className='mb-3 text-xs text-black/40 dark:text-white/40'>
                The GPU signed a fresh, nonce-bound SPDM attestation report whose certificate
                chain roots at NVIDIA&rsquo;s Device Identity CA. The report data also binds this
                GPU to the CPU enclave&rsquo;s certificate, proving the two are co-located.
                {!gpu.measurements_verified && ' Firmware/VBIOS measurements are not yet matched against a signed NVIDIA reference manifest (RIM).'}
            </p>
            <div className='space-y-3'>
                {fields.map((f) => (
                    <FieldRow key={f.label} label={f.label} value={f.value} description={f.description} />
                ))}
            </div>
        </Section>
    );
}

function ExtensionsSection({
    title,
    description,
    extensions,
    accent = false,
    expectations,
    cwasmHash,
    workloadRelease
}: {
    title: string;
    description: string;
    extensions: AttestationExtension[];
    accent?: boolean;
    expectations?: AttestationExpectations;
    cwasmHash?: string;
    workloadRelease?: WorkloadRelease;
}) {
    return (
        <section
            className={`rounded-xl border p-5 ${
                accent
                    ? 'border-emerald-200/50 bg-emerald-50/30 dark:border-emerald-500/20 dark:bg-emerald-900/10'
                    : 'border-black/10 dark:border-white/10'
            }`}
        >
            <h2 className='mb-3 text-sm font-semibold'>{title}</h2>
            <p className='mb-4 text-xs text-black/40 dark:text-white/40'>{description}</p>
            <div className='space-y-4'>
                {extensions.map((ext) => {
                    const printable = TEXT_OIDS.has(ext.oid) ? hexToPrintableText(ext.value_hex) : null;
                    const display = printable ? `${printable}  (${ext.value_hex})` : ext.value_hex;
                    // OID 4.2 (Workload Image Digest): show the published-package
                    // link + digest-match verdict (the app-code analogue of the
                    // Enclave OS release link), when management-service resolved it.
                    const isImageDigest = ext.oid === PRIVASYS_OID.APP_CODE_HASH;
                    const badge = isImageDigest && workloadRelease
                        ? <WorkloadReleaseBadge release={workloadRelease} />
                        : checkExpectation(ext, expectations, cwasmHash);
                    return (
                        <div
                            key={ext.oid}
                            className='border-b border-black/5 pb-3 last:border-0 last:pb-0 dark:border-white/5'
                        >
                            <FieldRow
                                label={ext.label}
                                value={display}
                                description={OID_DESCRIPTIONS[ext.label]}
                                badge={badge}
                            />
                            <span className='mt-1 block font-mono text-[10px] text-black/30 dark:text-white/30'>
                                {ext.oid}
                            </span>
                        </div>
                    );
                })}
            </div>
        </section>
    );
}

// Returns a verification badge (or null) when an expected hash is
// supplied for this extension's OID. Comparison is case-insensitive
// and tolerates a leading 'sha256:' prefix on the expected value.
function checkExpectation(
    ext: AttestationExtension,
    expectations?: AttestationExpectations,
    cwasmHash?: string
): React.ReactNode {
    const norm = (s?: string) => (s || '').replace(/^sha256:/i, '').toLowerCase();
    const actual = norm(ext.value_hex);
    let expected: string | undefined;
    let label: string | undefined;
    if (ext.oid === PRIVASYS_OID.APP_CODE_HASH) {
        expected = norm(expectations?.workloadImageDigest) || norm(cwasmHash);
        label = expectations?.labels?.workloadImageDigest
            || (cwasmHash ? 'matches uploaded CWASM hash' : 'matches expected workload image digest');
    } else if (ext.oid === PRIVASYS_OID.MODEL_DIGEST) {
        expected = norm(expectations?.modelDigest);
        label = expectations?.labels?.modelDigest || 'matches expected AI model digest';
    } else if (ext.oid === PRIVASYS_OID.APP_ID) {
        expected = norm(expectations?.appId);
        label = expectations?.labels?.appId || 'matches expected app identity';
    } else if (ext.oid === PRIVASYS_OID.TOOLS_DIGEST) {
        expected = norm(expectations?.toolsDigest);
        label = expectations?.labels?.toolsDigest || 'matches expected AI tool set';
    }
    if (!expected) return null;
    return actual === expected
        ? <Badge tone='ok'>{`\u2713 Verified - ${label}`}</Badge>
        : <Badge tone='err'>{`\u2717 Mismatch - does not ${label}`}</Badge>;
}

// OsReleaseBadge renders ONE merged pill: the Enclave OS release link and the
// measurements-match verdict together. Colour follows the verdict — green only
// when measurements verify, red on mismatch, neutral when the verdict is
// unknown — so the link is never green while the measurements do not match.
// Anchored next to the measurement rows the release is checked against
// (MRENCLAVE for SGX, RTMR[1]/RTMR[2] for TDX).
function OsReleaseBadge({ osRelease }: { osRelease: OsRelease }) {
    const verified = osRelease.status === 'verified';
    const mismatch = osRelease.status === 'mismatch';
    const tag = osRelease.tag || 'release';
    const verdict = verified
        ? '✓ Measurements match '
        : mismatch
            ? '✗ Measurements do not match '
            : '';
    const arrow = osRelease.url ? ' ↗' : '';
    const text = `${verdict}Enclave OS ${tag}${arrow}`;
    const tone = verified
        ? 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400'
        : mismatch
            ? 'bg-red-100 text-red-700 hover:bg-red-200 dark:bg-red-900/30 dark:text-red-400'
            : 'bg-black/5 text-black/60 hover:bg-black/10 dark:bg-white/10 dark:text-white/60';
    const title = mismatch
        ? 'This enclave\'s measurements do not match the Enclave OS release it is linked to'
        : 'The official Enclave OS release this enclave runs, and whether its measurements match';
    if (!osRelease.url) {
        return <Badge tone={verified ? 'ok' : mismatch ? 'err' : 'neutral'}>{text}</Badge>;
    }
    return (
        <a
            href={osRelease.url}
            target='_blank'
            rel='noreferrer'
            title={title}
            className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium ${tone}`}
        >
            {text}
        </a>
    );
}

// WorkloadReleaseBadge renders ONE merged pill for the Workload Image Digest
// (OID 4.2): whether the deployed workload matches the build it came from, plus
// a link to that build \u2014 the published GHCR package / GitHub release for
// containers, or the reproducible-app-builder Actions run for wasm. Colour
// follows the verdict (green only on match, red on mismatch, neutral when
// unknown), mirroring the Enclave OS release pill.
function WorkloadReleaseBadge({ release }: { release: WorkloadRelease }) {
    const match = release.matches === true;
    const mismatch = release.matches === false;
    const label = release.label || 'build';
    const verdict = match ? '\u2713 digest match \u00b7 ' : mismatch ? '\u2717 digest mismatch \u00b7 ' : '';
    const arrow = release.url ? ' \u2197' : '';
    const text = `${verdict}${label}${arrow}`;
    const tone = match
        ? 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400'
        : mismatch
            ? 'bg-red-100 text-red-700 hover:bg-red-200 dark:bg-red-900/30 dark:text-red-400'
            : 'bg-black/5 text-black/60 hover:bg-black/10 dark:bg-white/10 dark:text-white/60';
    const title = mismatch
        ? 'The deployed workload does not match the build it is linked to'
        : 'The build this workload was produced from; open it to verify independently';
    if (!release.url) {
        return <Badge tone={match ? 'ok' : mismatch ? 'err' : 'neutral'}>{text}</Badge>;
    }
    return (
        <a
            href={release.url}
            target='_blank'
            rel='noreferrer'
            title={title}
            className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium ${tone}`}
        >
            {text}
        </a>
    );
}

function Section({
    title,
    badge,
    children
}: {
    title: string;
    badge?: React.ReactNode;
    children: React.ReactNode;
}) {
    return (
        <section className='rounded-xl border border-black/10 dark:border-white/10 p-5'>
            <div className='mb-3 flex flex-wrap items-center gap-2'>
                <h2 className='text-sm font-semibold'>{title}</h2>
                {badge}
            </div>
            {children}
        </section>
    );
}

function Stat({ label, value }: { label: string; value: string }) {
    return (
        <div>
            <div className='text-xs text-black/50 dark:text-white/50'>{label}</div>
            <div className='mt-0.5 font-mono text-xs'>{value}</div>
        </div>
    );
}


function DebugLine({ label, value }: { label: string; value: string }) {
    return (
        <div>
            <span className='text-[10px] text-red-600/60 dark:text-red-400/60'>{label}</span>
            <code className='mt-0.5 block break-all rounded bg-red-100/50 px-1.5 py-0.5 font-mono text-[10px] text-red-800 dark:bg-red-900/20 dark:text-red-300'>
                {value}
            </code>
        </div>
    );
}

function DownloadPemButton({ pem }: { pem: string }) {
    return (
        <button
            type='button'
            onClick={() => {
                const blob = new Blob([pem], { type: 'application/x-pem-file' });
                const url = URL.createObjectURL(blob);
                const a = document.createElement('a');
                a.href = url;
                a.download = 'enclave-certificate.pem';
                a.click();
                URL.revokeObjectURL(url);
            }}
            className='rounded-lg border border-black/10 dark:border-white/10 px-4 py-2 text-sm font-medium hover:bg-black/5 dark:hover:bg-white/5'
        >
            Download PEM
        </button>
    );
}

function CopyButton({ text, copyKey }: { text: string; copyKey: string }) {
    const [copied, setCopied] = useState<string | null>(null);
    return (
        <button
            type='button'
            onClick={() => {
                void navigator.clipboard.writeText(text);
                setCopied(copyKey);
                setTimeout(() => setCopied(null), 1800);
            }}
            className='text-xs text-black/40 hover:text-black/70 dark:text-white/40 dark:hover:text-white/70'
        >
            {copied === copyKey ? 'Copied!' : 'Copy'}
        </button>
    );
}

function PemSection({
    title,
    pem,
    copyKey,
    downloadName,
    accent = false
}: {
    title: string;
    pem: string;
    copyKey: string;
    downloadName: string;
    accent?: boolean;
}) {
    return (
        <section
            className={`rounded-xl border p-5 ${
                accent
                    ? 'border-emerald-200/50 bg-emerald-50/30 dark:border-emerald-500/20 dark:bg-emerald-900/10'
                    : 'border-black/10 dark:border-white/10'
            }`}
        >
            <div className='mb-3 flex items-center justify-between'>
                <h2 className='text-sm font-semibold'>{title}</h2>
                <div className='flex items-center gap-3'>
                    <button
                        type='button'
                        onClick={() => {
                            const blob = new Blob([pem], { type: 'application/x-pem-file' });
                            const url = URL.createObjectURL(blob);
                            const a = document.createElement('a');
                            a.href = url;
                            a.download = downloadName;
                            a.click();
                            URL.revokeObjectURL(url);
                        }}
                        className='text-xs text-black/40 hover:text-black/70 dark:text-white/40 dark:hover:text-white/70'
                    >
                        Download
                    </button>
                    <CopyButton text={pem} copyKey={copyKey} />
                </div>
            </div>
            <pre className='max-h-48 overflow-y-auto whitespace-pre-wrap break-all rounded-lg bg-black/5 p-3 font-mono text-[11px] dark:bg-white/5'>
                {pem}
            </pre>
        </section>
    );
}

function VerificationCodeSection({
    pubKeySha256,
    mode,
    quoteTime,
    contextHex,
    hctxB64,
    gpuEvidenceB64,
    reportData
}: {
    pubKeySha256: string;
    mode: AttestationMode;
    /** Deterministic mode: the exact quote_time string ("YYYY-MM-DDTHH:MMZ"). */
    quoteTime?: string;
    /** Challenge mode: hex of the 32-byte challenge context. */
    contextHex?: string;
    /** Challenge mode: base64 TLS exporter value of the verifying service's connection. */
    hctxB64?: string;
    /** Base64 GPU evidence envelope; SHA-256 of it is appended when present. */
    gpuEvidenceB64?: string;
    reportData: string;
}) {
    const challenge = mode === 'challenge';
    const formula = challenge
        ? 'SHA-512( SHA-256(public_key_DER) || context || hctx )'
        : 'SHA-512( SHA-256(public_key_DER) || quote_time )';
    const snippet = [
        '// Report Data verification (RA-TLS v2) - paste in your browser console',
        `const pubkeySha256 = "${pubKeySha256}";  // SHA-256 of the certificate's SubjectPublicKeyInfo DER`,
        ...(challenge
            ? [
                `const context      = "${contextHex || ''}";  // 32-byte challenge context (hex)`,
                `const hctxB64      = "${hctxB64 || ''}";  // TLS exporter value of the verifier's connection (base64, 32 bytes)`
            ]
            : [
                `const quoteTime    = "${quoteTime || ''}";  // quote minute, exact ASCII bytes`
            ]),
        `const gpuEvidence  = "${gpuEvidenceB64 || ''}";  // base64 GPU evidence envelope, empty when the enclave has no GPU`,
        `const reportData   = "${reportData}";`,
        '',
        'const hex2buf = h => new Uint8Array((h.match(/.{2}/g) || []).map(b => parseInt(b, 16)));',
        'const b642buf = b => b ? Uint8Array.from(atob(b), c => c.charCodeAt(0)) : new Uint8Array(0);',
        'const buf2hex = b => [...new Uint8Array(b)].map(x => x.toString(16).padStart(2, \'0\')).join(\'\');',
        '',
        '(async () => {',
        `  // report_data = ${formula}`,
        '  //               with SHA-256(gpu_evidence) appended when GPU evidence is present',
        challenge
            ? '  let binding = new Uint8Array([...hex2buf(context), ...b642buf(hctxB64)]);'
            : '  let binding = new TextEncoder().encode(quoteTime);',
        '  if (gpuEvidence) {',
        '    const gpuFold = await crypto.subtle.digest(\'SHA-256\', b642buf(gpuEvidence));',
        '    binding = new Uint8Array([...binding, ...new Uint8Array(gpuFold)]);',
        '  }',
        '  const input = new Uint8Array([...hex2buf(pubkeySha256), ...binding]);',
        '  const hash  = await crypto.subtle.digest(\'SHA-512\', input);',
        '  const computed = buf2hex(hash);',
        '  const actual   = reportData.toLowerCase();',
        '  console.log("computed:   ", computed);',
        '  console.log("report_data:", actual);',
        '  console.log(computed === actual ? "\\u2713 MATCH" : "\\u2717 MISMATCH");',
        '})();'
    ].join('\n');
    return (
        <section className='rounded-xl border border-black/10 p-5 dark:border-white/10'>
            <div className='mb-3 flex items-center justify-between'>
                <h2 className='text-sm font-semibold'>Verification Code</h2>
                <CopyButton text={snippet} copyKey='verify-snippet' />
            </div>
            <p className='mb-3 text-xs text-black/40 dark:text-white/40'>
                Copy this snippet and paste it in your browser&rsquo;s developer console to
                independently verify that <code className='text-[11px]'>{formula} == report_data</code>
                {challenge
                    ? '. The context is the challenge you chose; the exporter value is supplied by the verifying service that held the TLS connection, since browsers expose no TLS exporter.'
                    : '. This shows the quote commits to the certificate key and to the quote minute.'}
            </p>
            <pre className='max-h-56 overflow-y-auto whitespace-pre-wrap break-all rounded-lg bg-black/5 p-3 font-mono text-[11px] dark:bg-white/5'>
                {snippet}
            </pre>
        </section>
    );
}

function QuoteVerificationCodeSection({
    rawBase64,
    verifyQuoteUrl
}: {
    rawBase64: string;
    verifyQuoteUrl?: string;
}) {
    // The attestation server is OIDC-protected (audience
    // `attestation-server`, issuers `https://privasys.id` and
    // `https://relay.privasys.org`). The snippet below mints a token
    // via the Privasys Auth SDK when available on the page, and
    // otherwise falls back to a clear "paste a bearer token" comment
    // so users can experiment from any console.
    const url = verifyQuoteUrl || 'https://as.privasys.org/verify-quote';
    const snippet = [
        '// SGX/TDX Quote signature verification - paste in your browser console',
        '// Sends the raw quote to the Privasys Attestation Server for cryptographic verification.',
        '//',
        '// The attestation server requires an OIDC bearer token with',
        '//   - audience: "attestation-server"',
        '//   - issuer:   "https://privasys.id"  (or "https://relay.privasys.org" for app-bound tokens)',
        '//',
        '// If you are on a Privasys front-end that has signed you in to privasys.id,',
        '// the snippet will mint a short-lived audience-scoped token for you.',
        '// Otherwise sign in at https://privasys.id and paste an access_token below.',
        '',
        `const ATTESTATION_SERVER = "${url}";`,
        '',
        `const quoteBase64 = "${rawBase64}";`,
        '',
        'async function getToken() {',
        '  // 1. Hosted Privasys Auth SDK (chat.privasys.org, developer-portal, explorer\u2026):',
        '  const sdk = window.PrivasysAuth || window.privasysAuth;',
        '  if (sdk && typeof sdk.getTokenForAudience === "function") {',
        '    return await sdk.getTokenForAudience("attestation-server");',
        '  }',
        '  // 2. Manual fallback: paste a bearer token here.',
        '  const MANUAL_TOKEN = "";  // <-- paste your access_token here',
        '  if (MANUAL_TOKEN) return MANUAL_TOKEN;',
        '  throw new Error("No Privasys Auth SDK on this page. Sign in at https://privasys.id and paste an access_token into MANUAL_TOKEN above.");',
        '}',
        '',
        '(async () => {',
        '  const token = await getToken();',
        '  const resp = await fetch(ATTESTATION_SERVER, {',
        '    method: "POST",',
        '    headers: { "Content-Type": "application/json", "Authorization": "Bearer " + token },',
        '    body: JSON.stringify({ quote: quoteBase64 })',
        '  });',
        '  const r = await resp.json();',
        '  console.log(r.success ? "\\u2713 VERIFIED" : "\\u2717 FAILED", r);',
        '})();'
    ].join('\n');
    return (
        <section className='rounded-xl border border-black/10 p-5 dark:border-white/10'>
            <div className='mb-3 flex items-center justify-between'>
                <h2 className='text-sm font-semibold'>Quote Verification Code</h2>
                <CopyButton text={snippet} copyKey='quote-verify-snippet' />
            </div>
            <p className='mb-3 text-xs text-black/40 dark:text-white/40'>
                Copy this snippet and paste it in your browser&rsquo;s developer console to
                independently verify the SGX/TDX quote signature and certificate chain via the
                Privasys Attestation Server. The snippet auto-mints an audience-scoped token
                when the Privasys Auth SDK is available; otherwise paste one from{' '}
                <a href='https://privasys.id' className='underline' target='_blank' rel='noreferrer'>privasys.id</a>.
            </p>
            <pre className='max-h-56 overflow-y-auto whitespace-pre-wrap break-all rounded-lg bg-black/5 p-3 font-mono text-[11px] dark:bg-white/5'>
                {snippet}
            </pre>
        </section>
    );
}
