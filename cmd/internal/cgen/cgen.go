// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

//go:generate go run gen.go

package cgen

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	a "github.com/google/puffs/lang/ast"
	t "github.com/google/puffs/lang/token"
)

type visibility uint32

const (
	bothPubPri visibility = iota
	pubOnly
	priOnly
)

type Generator struct {
	// Extension should be either 'c' or 'h'.
	Extension byte
}

func (g *Generator) Generate(pkgName string, m *t.IDMap, files []*a.File) ([]byte, error) {
	b := &bytes.Buffer{}

	includeGuard := "PUFFS_" + strings.ToUpper(pkgName) + "_H"
	fmt.Fprintf(b, "#ifndef %s\n#define %s\n\n", includeGuard, includeGuard)

	// Write preamble.
	fmt.Fprintf(b, "// Code generated by puffs-gen-%c. DO NOT EDIT.\n\n", g.Extension)
	b.WriteString(preamble)
	b.WriteString("\n#ifdef __cplusplus\nextern \"C\" {\n#endif\n\n")

	b.WriteString("// ---------------- Status Codes\n\n")
	b.WriteString("// Status codes are non-positive integers.\n")
	b.WriteString("//\n")
	b.WriteString("// The least significant bit indicates a non-recoverable status code: an error.\n")
	b.WriteString("typedef enum {\n")
	fmt.Fprintf(b, "puffs_%s_status_ok = 0,\n", pkgName)
	fmt.Fprintf(b, "puffs_%s_error_bad_version = -2 + 1,\n", pkgName)
	fmt.Fprintf(b, "puffs_%s_error_null_receiver = -4 + 1,\n", pkgName)
	fmt.Fprintf(b, "puffs_%s_error_constructor_not_called= -6 + 1,\n", pkgName)
	fmt.Fprintf(b, "puffs_%s_status_short_dst = -8,\n", pkgName)
	fmt.Fprintf(b, "puffs_%s_status_short_src = -10,\n", pkgName)
	fmt.Fprintf(b, "} puffs_%s_status;\n\n", pkgName)

	b.WriteString("// ---------------- Public Structs\n\n")
	if err := forEachStruct(b, pkgName, m, files, pubOnly, writeStruct); err != nil {
		return nil, err
	}

	b.WriteString("// ---------------- Public Constructor and Destructor Prototypes\n\n")
	if err := forEachStruct(b, pkgName, m, files, pubOnly, writeCtorPrototypesPub); err != nil {
		return nil, err
	}

	b.WriteString("// ---------------- Public Function Prototypes\n\n")
	if err := forEachFunc(b, pkgName, m, files, pubOnly, writeFuncPrototype); err != nil {
		return nil, err
	}

	// Finish up the header, which is also the first part of the .c file.
	b.WriteString("\n#ifdef __cplusplus\n}  // extern \"C\"\n#endif\n\n")
	fmt.Fprintf(b, "#endif  // %s\n\n", includeGuard)
	if g.Extension == 'h' {
		return format(b)
	}

	b.WriteString("// ---------------- Private Structs\n\n")
	if err := forEachStruct(b, pkgName, m, files, priOnly, writeStruct); err != nil {
		return nil, err
	}

	b.WriteString("// ---------------- Private Constructor and Destructor Prototypes\n\n")
	if err := forEachStruct(b, pkgName, m, files, priOnly, writeCtorPrototypesPri); err != nil {
		return nil, err
	}

	b.WriteString("// ---------------- Private Function Prototypes\n\n")
	if err := forEachFunc(b, pkgName, m, files, priOnly, writeFuncPrototype); err != nil {
		return nil, err
	}

	b.WriteString("// ---------------- Constructor and Destructor Implementations\n\n")
	b.WriteString("// PUFFS_MAGIC is a magic number to check that constructors are called. It's\n")
	b.WriteString("// not foolproof, given C doesn't automatically zero memory before use, but it\n")
	b.WriteString("// should catch 99.99% of cases.\n")
	b.WriteString("//\n")
	b.WriteString("// Its (non-zero) value is arbitrary, based on md5sum(\"puffs\").\n")
	b.WriteString("#define PUFFS_MAGIC (0xCB3699CCU)\n\n")
	b.WriteString("// PUFFS_ALREADY_ZEROED is passed from a container struct's constructor to a\n")
	b.WriteString("// containee struct's constructor when the container has already zeroed the\n")
	b.WriteString("// containee's memory.\n")
	b.WriteString("//\n")
	b.WriteString("// Its (non-zero) value is arbitrary, based on md5sum(\"zeroed\").\n")
	b.WriteString("#define PUFFS_ALREADY_ZEROED (0x68602EF1U)\n\n")
	if err := forEachStruct(b, pkgName, m, files, bothPubPri, writeCtorImpls); err != nil {
		return nil, err
	}

	b.WriteString("// ---------------- Function Implementations\n\n")
	if err := forEachFunc(b, pkgName, m, files, bothPubPri, writeFuncImpl); err != nil {
		return nil, err
	}

	return format(b)
}

func forEachFunc(b *bytes.Buffer, pkgName string, m *t.IDMap, files []*a.File, v visibility,
	f func(*bytes.Buffer, string, *t.IDMap, *a.Func) error) error {

	for _, file := range files {
		for _, n := range file.TopLevelDecls() {
			if n.Kind() != a.KFunc ||
				(v == pubOnly && n.Raw().Flags()&a.FlagsPublic == 0) ||
				(v == priOnly && n.Raw().Flags()&a.FlagsPublic != 0) {
				continue
			}
			if err := f(b, pkgName, m, n.Func()); err != nil {
				return err
			}
		}
	}
	return nil
}

func forEachStruct(b *bytes.Buffer, pkgName string, m *t.IDMap, files []*a.File, v visibility,
	f func(*bytes.Buffer, string, *t.IDMap, *a.Struct) error) error {

	for _, file := range files {
		for _, n := range file.TopLevelDecls() {
			if n.Kind() != a.KStruct ||
				(v == pubOnly && n.Raw().Flags()&a.FlagsPublic == 0) ||
				(v == priOnly && n.Raw().Flags()&a.FlagsPublic != 0) {
				continue
			}
			if err := f(b, pkgName, m, n.Struct()); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeStruct(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Struct) error {
	structName := n.Name().String(m)
	fmt.Fprintf(b, "typedef struct {\n")
	if n.Suspendible() {
		fmt.Fprintf(b, "puffs_%s_status status;\n", pkgName)
		fmt.Fprintf(b, "uint32_t magic;\n")
	}
	for _, f := range n.Fields() {
		if err := writeField(b, m, f.Field()); err != nil {
			return err
		}
	}
	fmt.Fprintf(b, "} puffs_%s_%s;\n\n", pkgName, structName)
	return nil
}

func writeCtorSignature(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Struct, public bool, ctor bool) {
	structName := n.Name().String(m)
	ctorName := "destructor"
	if ctor {
		ctorName = "constructor"
		if public {
			fmt.Fprintf(b, "// puffs_%s_%s_%s is a constructor function.\n", pkgName, structName, ctorName)
			fmt.Fprintf(b, "//\n")
			fmt.Fprintf(b, "// It should be called before any other puffs_%s_%s_* function.\n",
				pkgName, structName)
			fmt.Fprintf(b, "//\n")
			fmt.Fprintf(b, "// Pass PUFFS_VERSION and 0 for puffs_version and for_internal_use_only.\n")
		}
	}
	fmt.Fprintf(b, "void puffs_%s_%s_%s(puffs_%s_%s *self", pkgName, structName, ctorName, pkgName, structName)
	if ctor {
		fmt.Fprintf(b, ", uint32_t puffs_version, uint32_t for_internal_use_only")
	}
	fmt.Fprintf(b, ")")
}

func writeCtorPrototypesPub(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Struct) error {
	return writeCtorPrototypes(b, pkgName, m, n, true)
}

func writeCtorPrototypesPri(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Struct) error {
	return writeCtorPrototypes(b, pkgName, m, n, false)
}

func writeCtorPrototypes(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Struct, public bool) error {
	if !n.Suspendible() {
		return nil
	}
	for _, ctor := range []bool{true, false} {
		writeCtorSignature(b, pkgName, m, n, public, ctor)
		b.WriteString(";\n\n")
	}
	return nil
}

func writeCtorImpls(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Struct) error {
	if !n.Suspendible() {
		return nil
	}
	for _, ctor := range []bool{true, false} {
		writeCtorSignature(b, pkgName, m, n, false, ctor)
		fmt.Fprintf(b, "{\n")
		fmt.Fprintf(b, "if (!self) { return; }\n")

		if ctor {
			fmt.Fprintf(b, "if (puffs_version != PUFFS_VERSION) {\n")
			fmt.Fprintf(b, "self->status = puffs_%s_error_bad_version;\n", pkgName)
			fmt.Fprintf(b, "return;\n")
			fmt.Fprintf(b, "}\n")

			b.WriteString("if (for_internal_use_only != PUFFS_ALREADY_ZEROED) {" +
				"memset(self, 0, sizeof(*self)); }\n")
			b.WriteString("self->magic = PUFFS_MAGIC;\n")

			for _, f := range n.Fields() {
				f := f.Field()
				if dv := f.DefaultValue(); dv != nil {
					// TODO: set default values for array types.
					fmt.Fprintf(b, "self->f_%s = %d;\n", f.Name().String(m), dv.ConstValue())
				}
			}
		}

		// TODO: call any ctor/dtors on sub-structures.
		b.WriteString("}\n\n")
	}
	return nil
}

func writeFuncSignature(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Func) {
	if n.Suspendible() {
		fmt.Fprintf(b, "puffs_%s_status", pkgName)
	} else {
		fmt.Fprintf(b, "void")
	}
	fmt.Fprintf(b, " puffs_%s", pkgName)
	if r := n.Receiver(); r != 0 {
		fmt.Fprintf(b, "_%s", r.String(m))
	}
	fmt.Fprintf(b, "_%s(", n.Name().String(m))
	if r := n.Receiver(); r != 0 {
		fmt.Fprintf(b, "puffs_%s_%s *self", pkgName, r.String(m))
	}
	// TODO: write n's args.
	fmt.Fprintf(b, ")")
}

func writeFuncPrototype(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Func) error {
	writeFuncSignature(b, pkgName, m, n)
	b.WriteString(";\n\n")
	return nil
}

func writeFuncImpl(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Func) error {
	writeFuncSignature(b, pkgName, m, n)
	b.WriteString("{\n")

	cleanup0 := false

	// Check the previous status and the args.
	if n.Public() {
		if n.Receiver() != 0 {
			fmt.Fprintf(b, "if (!self) { return puffs_%s_error_null_receiver; }\n", pkgName)
		}
	}
	if n.Suspendible() {
		fmt.Fprintf(b, "puffs_%s_status status = ", pkgName)
		if n.Receiver() != 0 {
			fmt.Fprintf(b, "self->status;\n")
			if n.Public() {
				fmt.Fprintf(b, "if (status & 1) { return status; }")
			}
		} else {
			fmt.Fprintf(b, "puffs_%s_status_ok;\n", pkgName)
		}
		if n.Public() {
			fmt.Fprintf(b, "if (self->magic != PUFFS_MAGIC) {"+
				"status = puffs_%s_error_constructor_not_called; goto cleanup0; }\n", pkgName)
			cleanup0 = true
		}
	} else if r := n.Receiver(); r != 0 {
		// TODO: fix this.
		return fmt.Errorf(`cannot convert Puffs function "%s.%s" to C`, r.String(m), n.Name().String(m))
	}
	// TODO: check the args.
	b.WriteString("\n")

	// Generate the local variables.
	if err := writeVars(b, pkgName, m, n.Node(), 0); err != nil {
		return err
	}
	b.WriteString("\n")

	// Generate the function body.
	for _, o := range n.Body() {
		if err := writeStatement(b, pkgName, m, o, 0); err != nil {
			return err
		}
	}
	b.WriteString("\n")

	if cleanup0 {
		fmt.Fprintf(b, "cleanup0: self->status = status;\n")
	}
	if n.Suspendible() {
		fmt.Fprintf(b, "return status;\n")
	}

	b.WriteString("}\n\n")
	return nil
}

func writeField(b *bytes.Buffer, m *t.IDMap, n *a.Field) error {
	convertible := true
	for x := n.XType(); x != nil; x = x.Inner() {
		if p := x.PackageOrDecorator(); p != 0 && p != t.IDOpenBracket {
			convertible = false
			break
		}
		if x.Inner() != nil {
			continue
		}
		if k := x.Name().Key(); k < t.Key(len(cTypeNames)) {
			if s := cTypeNames[k]; s != "" {
				b.WriteString(s)
				b.WriteByte(' ')
				continue
			}
		}
		convertible = false
		break
	}
	if !convertible {
		// TODO: fix this.
		return fmt.Errorf("cannot convert Puffs type %q to C", n.XType().String(m))
	}

	b.WriteString("f_")
	b.WriteString(n.Name().String(m))

	for x := n.XType(); x != nil; x = x.Inner() {
		if x.PackageOrDecorator() == t.IDOpenBracket {
			b.WriteByte('[')
			b.WriteString(x.ArrayLength().ConstValue().String())
			b.WriteByte(']')
		}
	}

	b.WriteString(";\n")
	return nil
}

func writeVars(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Node, depth uint32) error {
	if depth > a.MaxBodyDepth {
		return fmt.Errorf("cgen: body recursion depth too large")
	}
	depth++

	if n.Kind() == a.KVar {
		x := n.Var().XType()
		if k := x.Name().Key(); k < t.Key(len(cTypeNames)) {
			if s := cTypeNames[k]; s != "" {
				fmt.Fprintf(b, "%s v_%s;\n", s, n.Var().Name().String(m))
				return nil
			}
		}
		// TODO: fix this.
		return fmt.Errorf("cgen: cannot convert Puffs type %q to C", x.String(m))
	}

	for _, l := range n.Raw().SubLists() {
		for _, o := range l {
			if err := writeVars(b, pkgName, m, o, depth); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeStatement(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Node, depth uint32) error {
	if depth > a.MaxBodyDepth {
		return fmt.Errorf("cgen: body recursion depth too large")
	}
	depth++

	switch n.Kind() {
	case a.KAssert:
		// Assertions only apply at compile-time.
		return nil

	case a.KAssign:
		n := n.Assign()
		if err := writeExpr(b, pkgName, m, n.LHS(), depth); err != nil {
			return err
		}
		// TODO: does KeyAmpHatEq need special consideration?
		b.WriteString(cOpNames[0xFF&n.Operator().Key()])
		if err := writeExpr(b, pkgName, m, n.RHS(), depth); err != nil {
			return err
		}
		b.WriteString(";\n")
		return nil

	case a.KIf:
		// TODO.

	case a.KJump:
		// TODO.

	case a.KReturn:
		// TODO.

	case a.KVar:
		n := n.Var()
		fmt.Fprintf(b, "v_%s = ", n.Name().String(m))
		if v := n.Value(); v != nil {
			if err := writeExpr(b, pkgName, m, v, 0); err != nil {
				return err
			}
		} else {
			b.WriteByte('0')
		}
		b.WriteString(";\n")
		return nil

	case a.KWhile:
		n := n.While()
		b.WriteString("while (")
		if err := writeExpr(b, pkgName, m, n.Condition(), 0); err != nil {
			return err
		}
		b.WriteString(") {\n")
		for _, o := range n.Body() {
			if err := writeStatement(b, pkgName, m, o, depth); err != nil {
				return err
			}
		}
		b.WriteString("}\n")
		return nil
		// TODO.

	}
	return fmt.Errorf("cgen: unrecognized ast.Kind (%s) for writeStatement", n.Kind())
}

func writeExpr(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Expr, depth uint32) error {
	if depth > a.MaxExprDepth {
		return fmt.Errorf("cgen: expression recursion depth too large")
	}
	depth++

	if cv := n.ConstValue(); cv != nil {
		// TODO: write false/true instead of 0/1 if n.MType() is bool?
		b.WriteString(cv.String())
		return nil
	}

	switch n.ID0().Flags() & (t.FlagsUnaryOp | t.FlagsBinaryOp | t.FlagsAssociativeOp) {
	case 0:
		if err := writeExprOther(b, pkgName, m, n, depth); err != nil {
			return err
		}
	case t.FlagsUnaryOp:
		if err := writeExprUnaryOp(b, pkgName, m, n, depth); err != nil {
			return err
		}
	case t.FlagsBinaryOp:
		if err := writeExprBinaryOp(b, pkgName, m, n, depth); err != nil {
			return err
		}
	case t.FlagsAssociativeOp:
		if err := writeExprAssociativeOp(b, pkgName, m, n, depth); err != nil {
			return err
		}
	default:
		return fmt.Errorf("cgen: unrecognized token.Key (0x%X) for writeExpr", n.ID0().Key())
	}

	return nil
}

func writeExprOther(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Expr, depth uint32) error {
	switch n.ID0().Key() {
	case 0:
		if id1 := n.ID1(); id1.Key() == t.KeyThis {
			b.WriteString("self")
		} else {
			// TODO: don't assume that the v_ prefix is necessary.
			b.WriteString("v_")
			b.WriteString(id1.String(m))
		}
		return nil

	case t.KeyOpenParen:
	// n is a function call.
	// TODO.

	case t.KeyOpenBracket:
	// n is an index.
	// TODO.

	case t.KeyColon:
	// n is a slice.
	// TODO.

	case t.KeyDot:
		if err := writeExpr(b, pkgName, m, n.LHS().Expr(), depth); err != nil {
			return err
		}
		// TODO: choose between . vs -> operators.
		//
		// TODO: don't assume that the f_ prefix is necessary.
		b.WriteString("->f_")
		b.WriteString(n.ID1().String(m))
		return nil
	}
	return fmt.Errorf("cgen: unrecognized token.Key (0x%X) for writeExprOther", n.ID0().Key())
}

func writeExprUnaryOp(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Expr, depth uint32) error {
	// TODO.
	return nil
}

func writeExprBinaryOp(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Expr, depth uint32) error {
	op := n.ID0()
	if op.Key() == t.KeyXBinaryAs {
		// TODO.
		return nil
	}
	b.WriteByte('(')
	if err := writeExpr(b, pkgName, m, n.LHS().Expr(), depth); err != nil {
		return err
	}
	// TODO: does KeyXBinaryAmpHat need special consideration?
	b.WriteString(cOpNames[0xFF&op.Key()])
	if err := writeExpr(b, pkgName, m, n.RHS().Expr(), depth); err != nil {
		return err
	}
	b.WriteByte(')')
	return nil
}

func writeExprAssociativeOp(b *bytes.Buffer, pkgName string, m *t.IDMap, n *a.Expr, depth uint32) error {
	// TODO.
	return nil
}

func format(rawSource *bytes.Buffer) ([]byte, error) {
	stdout := &bytes.Buffer{}
	cmd := exec.Command("clang-format", "-style=Chromium")
	cmd.Stdin = rawSource
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

var cTypeNames = [...]string{
	t.KeyI8:    "int8_t",
	t.KeyI16:   "int16_t",
	t.KeyI32:   "int32_t",
	t.KeyI64:   "int64_t",
	t.KeyU8:    "uint8_t",
	t.KeyU16:   "uint16_t",
	t.KeyU32:   "uint32_t",
	t.KeyU64:   "uint64_t",
	t.KeyUsize: "size_t",
	t.KeyBool:  "bool",
}

var cOpNames = [256]string{
	t.KeyEq:       " = ",
	t.KeyPlusEq:   " += ",
	t.KeyMinusEq:  " -= ",
	t.KeyStarEq:   " *= ",
	t.KeySlashEq:  " /= ",
	t.KeyShiftLEq: " <<= ",
	t.KeyShiftREq: " >>= ",
	t.KeyAmpEq:    " &= ",
	t.KeyAmpHatEq: " no_such_amp_hat_C_operator ",
	t.KeyPipeEq:   " |= ",
	t.KeyHatEq:    " ^= ",

	t.KeyXUnaryPlus:  "+",
	t.KeyXUnaryMinus: "-",
	t.KeyXUnaryNot:   "!",

	t.KeyXBinaryPlus:        " + ",
	t.KeyXBinaryMinus:       " - ",
	t.KeyXBinaryStar:        " * ",
	t.KeyXBinarySlash:       " / ",
	t.KeyXBinaryShiftL:      " << ",
	t.KeyXBinaryShiftR:      " >> ",
	t.KeyXBinaryAmp:         " & ",
	t.KeyXBinaryAmpHat:      " no_such_amp_hat_C_operator ",
	t.KeyXBinaryPipe:        " | ",
	t.KeyXBinaryHat:         " ^ ",
	t.KeyXBinaryNotEq:       " != ",
	t.KeyXBinaryLessThan:    " < ",
	t.KeyXBinaryLessEq:      " <= ",
	t.KeyXBinaryEqEq:        " == ",
	t.KeyXBinaryGreaterEq:   " >= ",
	t.KeyXBinaryGreaterThan: " > ",
	t.KeyXBinaryAnd:         " && ",
	t.KeyXBinaryOr:          " || ",
	t.KeyXBinaryAs:          " no_such_as_C_operator ",

	t.KeyXAssociativePlus: " + ",
	t.KeyXAssociativeStar: " * ",
	t.KeyXAssociativeAmp:  " & ",
	t.KeyXAssociativePipe: " | ",
	t.KeyXAssociativeHat:  " ^ ",
	t.KeyXAssociativeAnd:  " && ",
	t.KeyXAssociativeOr:   " || ",
}