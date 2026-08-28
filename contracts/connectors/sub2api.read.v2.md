# Sub2API read contract v2

v2 embeds the complete v1 read interface and adds `ChannelDirectory`. Every `/api/v1/admin/accounts`
row with a nonblank ID appears exactly once. `balance_minor_units` is nullable: null means the
account has no balance concept; numeric zero means a known exhausted balance.

The envelope carries independent `inventory_completeness` (complete/truncated/reported/fetched/
evidence) and `coverage_partial`. Binding decisions use only completeness, freshness, and source;
missing balances never make the identity directory incomplete. The legacy balance capability and
metric remain a pure projection that omits null-balance rows and marks that projection partial.

This contract adds no write method and no credential-shaped field.

