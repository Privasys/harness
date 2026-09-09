// @ts-nocheck -- vendored AS SOURCE from websites/libs/attestation-view (the canonical
// shared attestation view; keep in sync on change). ts-nocheck: compiled here under
// dsh's stricter tsc flags which the canonical copy does not target; runtime is identical.
// Shared attestation types.
//
// These mirror the JSON shape returned by management-service
// `GET /api/v1/apps/{id}/attest` and consumed by:
//   * developer.privasys.org dashboard "Attestation" tab,
//   * explorer.privasys.org (once ported to Next.js),
//   * chat.privasys.org info drawer.
//
// Source of truth: platform/management-service/handlers.go AttestApp.
// Keep in sync; the developer-portal copy in
// `apps/fronts/developer.privasys.org/lib/types.ts` will be replaced
// with re-exports from this lib in a follow-up.

export interface AttestationCertificate {
    subject: string;
    issuer: string;
    serial_number: string;
    not_before: string;
    not_after: string;
    signature_algorithm: string;
    public_key_sha256: string;
}

// RA-TLS v2 attestation mode. The evidence is exchanged on the TLS
// connection after a normal TLS 1.3 handshake; the certificate itself
// carries no quote.
export type AttestationMode = 'deterministic' | 'challenge';

export interface AttestationQuote {
    // Evidence family, e.g. "TDX Quote" / "SGX Quote" (display label).
    type: string;
    // TEE family as sent in the attest message: "sgx" | "tdx" | "tdx-gpu" | "sev-snp".
    tee?: string;
    is_mock: boolean;
    version?: number;
    report_data?: string;
    raw_base64?: string;
    // The attestation mode management-service used when it dialled the
    // enclave: "challenge" when a challenge query was sent, "deterministic"
    // otherwise. Selects the report_data recipe below.
    attestation?: AttestationMode;
    // Minute at which the quote was minted, ASCII "YYYY-MM-DDTHH:MMZ". In
    // deterministic mode it is the binding value:
    //   report_data = SHA-512( SHA-256(SPKI_DER) || quote_time )
    quote_time?: string;
    // Challenge mode only. Hex of the 32-byte challenge context; equals the
    // challenge the browser sent when that challenge is exactly 32 bytes.
    context?: string;
    // Challenge mode only. Base64 of the 32-byte TLS exporter value
    // (RFC 8446 section 7.5, label "EXPORTER-privasys-ratls-attest-v2",
    // context = `context`) of management-service's own connection to the
    // enclave. Browsers expose no TLS exporter, so the verifier that held
    // the connection supplies it; the browser then reproduces
    //   report_data = SHA-512( SHA-256(SPKI_DER) || context || hctx )
    // which shows the quote was bound to that verifier's connection.
    hctx?: string;
    // Base64 of the NVIDIA GPU evidence envelope, present for GPU enclaves.
    // Both recipes append SHA-256(gpu_evidence) to the bound value when it
    // is present. The GPU evidence no longer lives in a certificate extension.
    gpu_evidence_base64?: string;
    // The management-service's own verdicts, computed with the full recipe
    // (including the GPU-evidence fold). `report_data_verified` is the
    // mode-independent verdict; `challenge_verified` is set in challenge
    // mode, `deterministic_verified` otherwise. A `false` here means the
    // quote does not commit to the certificate key and binding value, and
    // the enclave's identity must not be trusted.
    report_data_verified?: boolean;
    challenge_verified?: boolean;
    deterministic_verified?: boolean;
    mr_enclave?: string;
    mr_signer?: string;
    mr_td?: string;
    rtmr0?: string;
    rtmr1?: string;
    rtmr2?: string;
    rtmr3?: string;
    format?: string;
}

export interface AttestationExtension {
    oid: string;
    label: string;
    value_hex: string;
}

// The official Enclave OS GitHub release the attesting enclave runs. Captured
// when the enclave is registered on the platform and stamped onto the
// attestation payload by management-service, so the link is authoritative and
// instant (no measurement lookup at view time).
export interface OsRelease {
    url: string;
    tag: string;
    // Verification of the enclave's measurements against the release
    // (management-service): 'verified' | 'mismatch' | 'unverified' | ''.
    status?: string;
}

// WorkloadRelease is the app-code analogue of OsRelease: the build the deployed
// version's workload came from, plus whether the ATTESTED workload digest
// (OID 4.2) matches that build's output. Stamped by management-service.
// Containers link to the published GHCR package / GitHub release; wasm apps link
// to the reproducible-app-builder Actions run that produced the .cwasm.
export interface WorkloadRelease {
    url: string;      // GHCR package / release page (container) or build-run URL (wasm)
    label?: string;   // e.g. "v0.5.2" (container) or "reproducible build" (wasm)
    digest?: string;  // expected bare hex digest (image digest / .cwasm SHA-256)
    matches?: boolean; // attested OID 4.2 == expected digest (omitted when unknown)
}

export interface AttestationTLS {
    version: string;
    cipher_suite: string;
}

export interface EventLogDigest {
    pcr: number;
    event_type: number;
    digest: string;       // SHA-384 hex
    data_text?: string;   // Human-readable when decodable
}

export interface AppEvent {
    timestamp: string;
    pcr: number;
    digest_sha384: string;
    digest_sha256: string;
    type: string;        // 'container_load' | 'container_unload'
    description: string;
}

// NVIDIA GPU Confidential-Computing attestation verdict, produced by the
// attestation server when the evidence exchange carries GPU evidence
// (the tdx-gpu combined case, `quote.gpu_evidence_base64`). Mirrors the
// attestation-server GPUAttestationResult; populated by management-service
// AttestApp.
export interface GPUAttestationResult {
    verified: boolean;
    // measurements_verified is true only once firmware/VBIOS measurements are
    // matched against a signed NVIDIA RIM. verified can hold (genuine device,
    // CC mode, authentic nonce-bound report) while this is still false.
    measurements_verified: boolean;
    gpu_uuid?: string;
    driver?: string;
    vbios?: string;
    cc_environment?: string;
    status?: string;
    error?: string;
}

export interface AttestationResult {
    certificate: AttestationCertificate;
    pem: string;
    quote: AttestationQuote | null;
    extensions: AttestationExtension[];
    tls: AttestationTLS;
    // NVIDIA GPU attestation verdict, present when the evidence exchange
    // carried GPU evidence (quote.gpu_evidence_base64) and it was verified by
    // the attestation server. Renders a dedicated GPU section in the view.
    gpu_attestation?: GPUAttestationResult | null;
    // Per-workload (SNI) certificate data
    app_extensions?: AttestationExtension[];
    app_pem?: string;
    app_quote?: AttestationQuote | null;
    // Challenge mode: true when a challenge query was sent; `challenge` is
    // the hex the browser chose (quote.context echoes it when 32 bytes).
    challenge_mode: boolean;
    challenge?: string;
    // Stored CWASM hash for verification against APP_CODE_HASH (OID 4.2)
    cwasm_hash?: string;
    // TCG2 event log for RTMR replay verification (TDX only)
    event_log_events?: EventLogDigest[];
    event_log_source?: string;
    // Application-level RTMR[3] events from the enclave manager
    app_events?: AppEvent[];
    // The Enclave OS release this enclave runs (set at registration).
    os_release?: OsRelease;
    // The container package the deployed workload was built from + digest match.
    workload_release?: WorkloadRelease;
}

// Result of POST /api/v1/verify-quote (attestation-server proxy).
export interface QuoteVerifyResult {
    success: boolean;
    status: string;
    teeType?: string;
    mrenclave?: string;
    mrsigner?: string;
    mrtd?: string;
    isvProdId?: number;
    isvSvn?: number;
    tcbDate?: string;
    advisoryIds?: string[];
    // Intel platform TCB status derived from PCS collateral (e.g. "UpToDate",
    // "ConfigurationAndSWHardeningNeeded"). Present when the attestation server
    // ran the TCB check (SGX_TCB_MODE report/enforce).
    tcbStatus?: string;
    message?: string;
    error?: string;
}

// Well-known Privasys OIDs, RA-TLS v2 scheme (enclave-os oids +
// ra-tls-clients docs/ratls-v2.md). The certificate carries identity and
// configuration extensions only; the quote and GPU evidence travel in the
// post-handshake evidence exchange, so there is no quote OID any more.
//
// Platform arcs (runtime-stamped, hosting enclave):
//   1.1 runtime version hash, 1.2 image profile, 1.3 enclave instance id,
//   2.1 config merkle root, 2.2 egress CA hash, 2.3 attestation servers
//   hash, 2.4 combined workloads hash, 3.1 DEK origin, 3.2 authenticated
//   state root.
// Workload arcs (per app leaf):
//   4.1 app id, 4.2 app code hash / image digest, 4.3 image ref,
//   5.1 workload config merkle root, 5.2 config hash, 5.4.* app-registered
//   values (Confidential AI model digest 5.4.5, tools digest 5.4.7),
//   6.1 key source / volume encryption, 7.1 attested dependency set.
export const PRIVASYS_OID = {
    RUNTIME_VERSION_HASH: '1.3.6.1.4.1.65230.1.1',
    IMAGE_PROFILE: '1.3.6.1.4.1.65230.1.2',
    ENCLAVE_INSTANCE_ID: '1.3.6.1.4.1.65230.1.3',
    CONFIG_MERKLE_ROOT: '1.3.6.1.4.1.65230.2.1',
    EGRESS_CA_HASH: '1.3.6.1.4.1.65230.2.2',
    ATTESTATION_SERVERS_HASH: '1.3.6.1.4.1.65230.2.3',
    COMBINED_WORKLOADS_HASH: '1.3.6.1.4.1.65230.2.4',
    DEK_ORIGIN: '1.3.6.1.4.1.65230.3.1',
    AUTHENTICATED_STATE_ROOT: '1.3.6.1.4.1.65230.3.2',
    APP_ID: '1.3.6.1.4.1.65230.4.1',
    APP_CODE_HASH: '1.3.6.1.4.1.65230.4.2',
    IMAGE_REF: '1.3.6.1.4.1.65230.4.3',
    WORKLOAD_CONFIG_MERKLE_ROOT: '1.3.6.1.4.1.65230.5.1',
    CONFIG_HASH: '1.3.6.1.4.1.65230.5.2',
    MODEL_DIGEST: '1.3.6.1.4.1.65230.5.4.5',
    TOOLS_DIGEST: '1.3.6.1.4.1.65230.5.4.7',
    KEY_SOURCE: '1.3.6.1.4.1.65230.6.1',
    DEPENDENCY_SET: '1.3.6.1.4.1.65230.7.1'
} as const;

// OIDs whose value bytes are UTF-8 strings, not raw hashes.
export const TEXT_OIDS: ReadonlySet<string> = new Set([
    PRIVASYS_OID.IMAGE_REF,
    PRIVASYS_OID.KEY_SOURCE
]);

// Optional expected values that the consumer can supply so the
// attestation view can render a green/red verification badge next
// to the matching workload extension. All values are lowercase hex
// strings; comparisons are case-insensitive.
export interface AttestationExpectations {
    /** Expected APP_CODE_HASH (OID 4.2). For container apps this is the
     *  registry image digest (sha256 hex without the algorithm prefix).
     *  For WASM apps this is the CWASM module SHA-256. */
    workloadImageDigest?: string;
    /** Expected MODEL_DIGEST (OID 5.4.5) - SHA-256 of the active AI model. */
    modelDigest?: string;
    /** Expected TOOLS_DIGEST (OID 5.4.7) - sha256 over the canonical JSON
     *  of the configured MCP tool servers. */
    toolsDigest?: string;
    /** Expected APP_ID (OID 4.1) - the management app id as undashed
     *  lowercase hex (raw 16-byte UUID). */
    appId?: string;
    /** Optional friendly labels shown in the verification badge.
     *  Defaults to a generic "Matches expected value". */
    labels?: {
        workloadImageDigest?: string;
        modelDigest?: string;
        toolsDigest?: string;
        appId?: string;
    };
}
