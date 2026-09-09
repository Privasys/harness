// @ts-nocheck -- vendored AS SOURCE from websites/libs/attestation-view (the canonical
// shared attestation view; keep in sync on change). ts-nocheck: compiled here under
// dsh's stricter tsc flags which the canonical copy does not target; runtime is identical.
'use client';

import type { AttestationActions, AttestationState } from '../use-attestation';

// Connect form. Renders a challenge (context) input and an
// "Inspect Certificate" button. Hides itself once a result is present
// (the parent renders <AttestationResultView> instead).
export function AttestationConnect({
    state,
    actions,
    title = 'Remote Attestation',
    description = 'Connect via RA-TLS and inspect the x.509 certificate, attestation quote, and all custom attestation extensions.'
}: {
    state: AttestationState;
    actions: AttestationActions;
    title?: string;
    description?: string;
}) {
    return (
        <section className='rounded-xl border border-black/10 dark:border-white/10 p-5'>
            <div className='mb-5 text-center'>
                <h2 className='text-lg font-semibold'>{title}</h2>
                <p className='mx-auto mt-1 max-w-lg text-sm text-black/50 dark:text-white/50'>
                    {description}
                </p>
            </div>

            <div className='mx-auto mb-5 max-w-lg'>
                <div className='mb-1.5 flex items-center justify-between'>
                    <label className='text-xs font-medium' htmlFor='attestation-challenge'>
                        Challenge (hex)
                    </label>
                    <button
                        type='button'
                        onClick={actions.regenerateChallenge}
                        className='text-[11px] text-black/40 dark:text-white/40 hover:text-black/70 dark:hover:text-white/70'
                    >
                        Regenerate
                    </button>
                </div>
                <input
                    id='attestation-challenge'
                    type='text'
                    value={state.challenge}
                    onChange={(e) => actions.setChallenge(e.target.value)}
                    placeholder='32-128 hex characters'
                    maxLength={128}
                    spellCheck={false}
                    autoComplete='off'
                    className='w-full rounded-lg border border-black/10 dark:border-white/10 bg-black/5 dark:bg-white/5 px-3 py-2 font-mono text-xs focus:outline-none focus:ring-1 focus:ring-black/20 dark:focus:ring-white/20'
                />
                <p className='mt-1.5 text-[11px] text-black/35 dark:text-white/35'>
                    A random challenge becomes the context of the evidence exchange: the enclave binds it, together with the verifying service&rsquo;s TLS connection, into the quote. Edit the field to replay a specific challenge.
                </p>
            </div>

            <div className='text-center'>
                <button
                    type='button'
                    onClick={() => void actions.inspect()}
                    disabled={state.loading}
                    className='rounded-lg bg-black px-5 py-2.5 text-sm font-medium text-white hover:opacity-80 disabled:opacity-40 dark:bg-white dark:text-black'
                >
                    {state.loading ? 'Connecting...' : 'Inspect Certificate'}
                </button>
            </div>

            {state.error && (
                <div className='mt-4 rounded-lg bg-red-50 dark:bg-red-900/20 p-3 text-sm text-red-700 dark:text-red-300'>
                    {state.error}
                </div>
            )}
        </section>
    );
}
