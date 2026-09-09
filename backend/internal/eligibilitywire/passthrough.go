package eligibilitywire

// The one place the "this is a copy, it introduces no vocabulary" judgement
// lives, shared by both scans in this package.
//
// Both discovery gates -- the eligibility_status scan in discover.go and the
// unit_code scan in unitcodes.go -- have to answer the same question about a
// value they cannot fold to a constant: is it a COPY of something this scan has
// already read, or is it a value from somewhere the scan has never looked? The
// first introduces nothing and is safely ignored. The second is exactly the gap
// the refusal path exists to prevent.
//
// Both files originally answered it by looking at the NAME on the right of the
// dot, and both were wrong the same way. Review found it on the unit-code side
// first: `m.UnitCode = zzelsewhere.WalletUnitCode` -- another package's
// variable, whose contents neither scan reads -- ends in the right name, so it
// was waved through as "a copy". The eligibility_status scan had the identical
// hole, and fixing only one of them would have left two nearly-identical
// functions that disagree, which is how the second one gets forgotten. Hence
// one helper, called from both.
//
// THE RULE: the base of the selector chain must resolve, in the package being
// scanned, to something that is not a package.
//
//   - `payload.UnitCode`, `*base.EligibilityStatus`, `unitCode` -- the base is a
//     variable, parameter or field of this package, so the value came from
//     somewhere this scan walked, or from a caller (which the contract gates
//     cover from the other direction, by going red when nothing emits a
//     declared value). Copy.
//   - `somepkg.WalletUnitCode` -- the base resolves to a *types.PkgName.
//     Not a copy; the caller refuses.
//   - `zzelsewhere.WalletUnitCode` where zzelsewhere is imported nowhere -- the
//     base resolves to nothing at all. Also not a copy: "I could not tell what
//     this is" must never share an answer with "this is fine".
//
// WHY IDENTIFIER RESOLUTION AND NOT THE BASE'S TYPE. Both scans type-check with
// stubImporter, which hands every import an empty package, so every type that
// comes through an import is invalid. A rule written against the base's type
// would therefore refuse `item.EligibilityStatus` and `item.UnitCode` for any
// `item` whose struct is declared in another package -- ordinary, correct code
// throughout application, postgresstore and httpapi. Identifier resolution
// survives the errors that cross-package type resolution does not: the checker
// still records what `item` IS (a variable) even when it cannot say what type
// it has.
//
// Callers must populate types.Info.Uses. A scan that forgets to gets `nil` for
// every base and refuses everything, which is loud rather than silent -- the
// safe direction for a mistake in a gate.

import (
	"go/ast"
	"go/types"
)

// isCopyOfAReadableValue reports whether expr reads a value through an
// identifier this scan can account for, as opposed to through a package
// qualifier or an unresolvable name. See the file comment for the reasoning;
// callers combine it with their own check that the expression names the field
// they care about.
func isCopyOfAReadableValue(info *types.Info, expr ast.Expr) bool {
	base, ok := leftmostIdent(expr)
	if !ok {
		// `f().UnitCode`, `<-ch.EligibilityStatus`: no identifier to ask about.
		return false
	}
	object, resolved := info.Uses[base]
	if !resolved || object == nil {
		return false
	}
	_, isPackage := object.(*types.PkgName)
	return !isPackage
}

// leftmostIdent walks down the left spine of a selector/deref/index chain to
// the identifier everything else hangs off. `*a.b[0].UnitCode` yields `a`.
func leftmostIdent(expr ast.Expr) (*ast.Ident, bool) {
	for {
		switch typed := ast.Unparen(expr).(type) {
		case *ast.Ident:
			return typed, true
		case *ast.SelectorExpr:
			expr = typed.X
		case *ast.StarExpr:
			expr = typed.X
		case *ast.IndexExpr:
			expr = typed.X
		default:
			return nil, false
		}
	}
}
