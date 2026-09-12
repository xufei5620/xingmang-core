"""The two existing runtime gates, evaluated against explicit deployment policy.

RoleScopesFrom trims roles and unions scopes; invoice AdminPolicy requires an
exact raw role. Never infer that membership from email, a display name or scope.
"""
import hashlib
import importlib.util
import json
from pathlib import Path

# preflight loads this module by absolute path without adding audit to sys.path.
# Load the pinned sibling the same way, never an ambient same-named module.
_default_spec = importlib.util.spec_from_file_location('unified_default_role_scopes', Path(__file__).with_name('default_role_scopes.py'))
_defaults = importlib.util.module_from_spec(_default_spec)
_default_spec.loader.exec_module(_defaults)
DEFAULT_ROLE_SCOPES, SOURCE_PATH, SOURCE_SHA256 = _defaults.DEFAULT_ROLE_SCOPES, _defaults.SOURCE_PATH, _defaults.SOURCE_SHA256

GO_SPACE = '\u0009\u000a\u000b\u000c\u000d\u0020\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000'
SCOPE_PREFIXES = ('registry.', 'ops.', 'audit.', 'platform.', 'action.', 'connector.',
                  'request.', 'ui.', 'credential.', 'staff.', 'publishing.')
COUNTS = ('admin_role_total', 'invoice_admin_total', 'invoice_admin_totp_registered',
          'invoice_admin_login_ready_total')


class DefaultCoverageError(ValueError):
    """Only public default role/scope names, safe for the preflight report."""


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result: raise ValueError('ROLE_MAP_DUPLICATE_KEY')
        result[key] = value
    return result


def effective_map(value):
    if not isinstance(value, dict) or not value: raise ValueError('EXPLICIT_ROLE_MAP_REQUIRED')
    result = {}
    for raw, scopes in value.items():
        if not isinstance(raw, str) or not isinstance(scopes, list): raise ValueError('INVALID_ROLE_MAP')
        role = raw.strip(GO_SPACE)
        if not role or role.lower().startswith(SCOPE_PREFIXES) or role in result: raise ValueError('AMBIGUOUS_ROLE_MAP')
        if any(not isinstance(scope, str) for scope in scopes): raise ValueError('INVALID_ROLE_MAP')
        clean = sorted({scope.strip(GO_SPACE) for scope in scopes if scope.strip(GO_SPACE)})
        if not clean: raise ValueError('EMPTY_ROLE_SCOPES')
        result[role] = clean
    return result


def explicit_policy(admin_role, value):
    if not isinstance(admin_role, str) or not admin_role or admin_role.strip(GO_SPACE) != admin_role:
        raise ValueError('EXACT_ADMIN_ROLE_REQUIRED')
    roles = effective_map(value)
    if 'finance.read' not in roles.get(admin_role, []): raise ValueError('ADMIN_ROLE_MUST_HAVE_FINANCE_READ')
    return admin_role, roles


def require_default_coverage(roles):
    """Reject silent permission loss; never merge defaults into explicit policy."""
    defaults = effective_map(DEFAULT_ROLE_SCOPES)
    missing_roles = sorted(set(defaults) - set(roles))
    if missing_roles:
        raise DefaultCoverageError('DEFAULT_ROLE_KEYS_MISSING: ' + ','.join(missing_roles))
    missing_scopes = {role: sorted(set(scopes) - set(roles[role]))
                      for role, scopes in defaults.items() if set(scopes) - set(roles[role])}
    if missing_scopes:
        raise DefaultCoverageError('DEFAULT_ROLE_SCOPES_MISSING: ' + json.dumps(missing_scopes, sort_keys=True))
    return {'source_path': SOURCE_PATH, 'source_sha256': SOURCE_SHA256,
            'default_role_scope_map_sha256': hashlib.sha256(json.dumps(defaults, sort_keys=True,
                separators=(',', ':'), ensure_ascii=False).encode()).hexdigest(),
            'default_role_count': len(defaults), 'default_scope_count': sum(map(len, defaults.values())),
            'additional_roles': sorted(set(roles) - set(defaults))}


def census(admin_role, roles, staff):
    admin_role, roles = explicit_policy(admin_role, roles)
    exact = [p for p in staff if admin_role in p['roles']]
    joint = [p for p in exact if any('finance.read' in roles.get(r.strip(GO_SPACE), []) for r in p['roles'])]
    ready = [p for p in joint if p['totp_registered'] and not any(
        p[flag] for flag in ('disabled', 'locked', 'must_change_password', 'must_enroll_totp'))]
    return {'admin_role': admin_role, 'role_scope_map': roles,
            'admin_role_total': len(exact), 'invoice_admin_total': len(joint),
            'invoice_admin_totp_registered': sum(p['totp_registered'] for p in joint),
            'invoice_admin_login_ready_total': len(ready)}


def validate_census(snapshot):
    required = {'schema', 'database', 'transaction_read_only', 'role_scope_map', 'admin_role', *COUNTS,
                'finance_read_total', 'finance_read_totp_registered', 'finance_read_enabled_total',
                'finance_read_enabled_totp_registered', 'staff'}
    if not isinstance(snapshot, dict) or set(snapshot) - required - {'captured_at'} or not required.issubset(snapshot):
        raise ValueError('JOINT_ROLE_SNAPSHOT_REQUIRED')
    if snapshot['schema'] != 'xingmang.identity-audit.platform/v2' or snapshot['transaction_read_only'] != 'on':
        raise ValueError('READONLY_ROLE_SNAPSHOT_REQUIRED')
    roles = effective_map(snapshot['role_scope_map'])
    people = snapshot['staff']
    if not isinstance(people, list): raise ValueError('INVALID_STAFF_CENSUS')
    seen = set()
    for p in people:
        if not isinstance(p, dict) or set(p) != {'id', 'roles', 'finance_read', 'totp_registered', 'disabled', 'locked', 'must_change_password', 'must_enroll_totp'}:
            raise ValueError('INVALID_STAFF_CENSUS')
        if not isinstance(p['id'], str) or not p['id'] or p['id'] in seen: raise ValueError('DUPLICATE_STAFF_CENSUS')
        seen.add(p['id'])
        if not isinstance(p['roles'], list) or any(not isinstance(r, str) for r in p['roles']): raise ValueError('INVALID_STAFF_ROLES')
        if any(type(p[k]) is not bool for k in ('finance_read', 'totp_registered', 'disabled', 'locked', 'must_change_password', 'must_enroll_totp')):
            raise ValueError('INVALID_STAFF_FLAGS')
        if p['finance_read'] != any('finance.read' in roles.get(r.strip(GO_SPACE), []) for r in p['roles']):
            raise ValueError('EFFECTIVE_SCOPE_MISMATCH')
    joint = census(snapshot['admin_role'], roles, people)
    counts = {k: joint[k] for k in COUNTS}
    counts.update(finance_read_total=sum(p['finance_read'] for p in people),
                  finance_read_totp_registered=sum(p['finance_read'] and p['totp_registered'] for p in people),
                  finance_read_enabled_total=sum(p['finance_read'] and not p['disabled'] for p in people),
                  finance_read_enabled_totp_registered=sum(p['finance_read'] and not p['disabled'] and p['totp_registered'] for p in people))
    if any(type(snapshot[k]) is not int or snapshot[k] != n for k, n in counts.items()):
        raise ValueError('CENSUS_COUNT_MISMATCH')
    return joint


def validate_resolved(env, proof):
    raw = env.get('XM_AUTH_ROLE_SCOPES')
    if not isinstance(raw, str) or not raw.strip(GO_SPACE): raise ValueError('EXPLICIT_ROLE_MAP_REQUIRED')
    admin_role, roles = explicit_policy(env.get('ADMIN_ROLE'), json.loads(raw, object_pairs_hook=unique))
    default_coverage = require_default_coverage(roles)
    if not isinstance(proof, dict) or set(proof) != {'admin_role', 'role_scope_map', *COUNTS}:
        raise ValueError('JOINT_ROLE_CENSUS_REQUIRED')
    if proof['admin_role'] != admin_role or proof['role_scope_map'] != roles:
        raise ValueError('ROLE_CENSUS_CONFIGURATION_MISMATCH')
    if any(type(proof[k]) is not int or proof[k] <= 0 for k in COUNTS): raise ValueError('NO_READY_INVOICE_ADMIN')
    if not (proof['invoice_admin_login_ready_total'] <= proof['invoice_admin_totp_registered']
            == proof['invoice_admin_total'] <= proof['admin_role_total']):
        raise ValueError('INVOICE_ADMIN_TOTP_COVERAGE_REQUIRED')
    # Only public policy metadata is emitted, never the surrounding environment.
    return {'admin_role': admin_role, **{k: proof[k] for k in COUNTS},
            'default_coverage': default_coverage,
            'role_scope_map_sha256': hashlib.sha256(json.dumps(roles, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()}
