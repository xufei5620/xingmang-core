# NewAPI channel-directory envelope v2

NewAPI v2 embeds the v1 read interface and keeps the existing `ChannelStatus` row semantics. Its
only new contract is an independent directory-completeness envelope carrying complete, truncated,
reported count (nullable), fetched count, and evidence. `coverage_partial` remains separate and may
be true when error-rate coverage is incomplete while the identity directory is complete.

`reported_count: null` means the upstream did not report a total; `0` means it explicitly reported
zero. The v2 envelope adds no write method, credential field, model-name guarantee, or assurance
claim.
