// Package auth provides isolated SUB/NEW user login, opaque local sessions,
// CSRF, source identity binding, and administrator role/MFA policy checks.
// Administrators enter through the trusted in-process staff resolver; identity
// provider redirects, bearer business APIs and signed console exchanges are retired.
// Historical identity keys and session data remain available for exact audit joins.
package auth
