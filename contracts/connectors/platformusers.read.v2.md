# platformusers read contract v2

The v2 core adds a structured `UserRef { platform, id }`, the canonical `u-` + lowercase UTF-8
hex path codec, and an exact `UserDetailReader`. IDs are opaque, lossless, and limited to 512
UTF-8 bytes; username, email, and token prefixes are never association keys. `ErrNotFound` means
the source was completely exhausted, while `ErrLookupIncomplete` means the lookup could not prove
absence. With `DAILY_USAGE_APPROVAL` and `KEY_SCOPE_APPROVAL`, this slice includes Fake/core
daily usage (`platformusers.user.daily_usage_read`) and metadata-only key inventory
(`platformusers.user.keys_metadata_read`, scope `platform.user_keys.read`); full keys and real
platform readers remain separately evidence-gated. Reqlog, payment, and invoice remain separate
capability/approval slices.
