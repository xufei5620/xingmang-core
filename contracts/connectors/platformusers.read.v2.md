# platformusers read contract v2

The v2 core adds a structured `UserRef { platform, id }`, the canonical `u-` + lowercase UTF-8
hex path codec, and an exact `UserDetailReader`. IDs are opaque, lossless, and limited to 512
UTF-8 bytes; username, email, and token prefixes are never association keys. `ErrNotFound` means
the source was completely exhausted, while `ErrLookupIncomplete` means the lookup could not prove
absence. Daily usage, key metadata, reqlog, payment, and invoice remain separate capability and
approval slices; no real platform reader is implied by this core contract.

