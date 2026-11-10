package routing

import (
	"errors"
	"fmt"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"
)

// Bounds applied to routing expressions (ADR 013). CEL is not Turing
// complete, but a comprehension over a large list can still be expensive:
// the cost limit makes every evaluation bounded whatever the event contains.
const (
	MaxExpressionBytes = 4096
	maxCost            = 100_000
	maxValueDepth      = 32
)

// newEnv declares the variables visible to expressions:
//
//	event  map(string, dyn)  context attributes and extensions
//	data   dyn               the decoded JSON payload (null when absent)
//
// Only the standard library, the string extensions and optional values are
// available: no function performs I/O or reads the clock. Optional values
// make missing fields explicit: data.?locale.orValue("fr").
func newEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("data", cel.DynType),
		ext.Strings(),
		cel.OptionalTypes(),
		cel.ParserExpressionSizeLimit(MaxExpressionBytes),
	)
}

// expr is a compiled expression.
type expr struct {
	source string
	prg    cel.Program
}

// compile parses and type-checks src. want lists the acceptable output types;
// dyn is always accepted since the event payload is untyped.
func compile(env *cel.Env, src string, want ...*cel.Type) (*expr, error) {
	if len(src) > MaxExpressionBytes {
		return nil, fmt.Errorf("expression exceeds %d bytes", MaxExpressionBytes)
	}
	ast, iss := env.Compile(src)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	out := ast.OutputType()
	// An optional result (data.?locale) is accepted wherever its value type
	// is; an empty optional means "not set".
	if out.TypeName() == types.OptionalType.TypeName() && len(out.Parameters()) == 1 {
		out = out.Parameters()[0]
	}
	ok := out.IsExactType(types.DynType)
	for _, w := range want {
		// w.IsAssignableType(out): can a value of type out be used as a w?
		ok = ok || w.IsAssignableType(out)
	}
	if !ok {
		return nil, fmt.Errorf("expression returns %s, expected %v", out, want)
	}
	prg, err := env.Program(ast, cel.CostLimit(maxCost), cel.EvalOptions(cel.OptOptimize))
	if err != nil {
		return nil, err
	}
	return &expr{source: src, prg: prg}, nil
}

// eval runs the expression and converts its result to plain Go values
// (nil, bool, int64, uint64, float64, string, []any, map[string]any).
func (e *expr) eval(vars map[string]any) (any, error) {
	out, _, err := e.prg.Eval(vars)
	if err != nil {
		return nil, err
	}
	return native(out, 0)
}

func native(v ref.Val, depth int) (any, error) {
	if depth > maxValueDepth {
		return nil, fmt.Errorf("value nested deeper than %d levels", maxValueDepth)
	}
	if o, ok := v.(*types.Optional); ok {
		if !o.HasValue() {
			return nil, nil
		}
		return native(o.GetValue(), depth+1)
	}
	switch v.Type() {
	case types.NullType:
		return nil, nil
	case types.BoolType, types.IntType, types.UintType, types.DoubleType, types.StringType:
		return v.Value(), nil
	case types.TimestampType:
		return v.Value().(time.Time).UTC().Format(time.RFC3339Nano), nil
	case types.DurationType:
		return v.Value().(time.Duration).String(), nil
	case types.ListType:
		l, ok := v.(traits.Lister)
		if !ok {
			return nil, errors.New("unsupported list value")
		}
		out := make([]any, 0, int(l.Size().(types.Int)))
		for it := l.Iterator(); it.HasNext() == types.True; {
			item, err := native(it.Next(), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case types.MapType:
		m, ok := v.(traits.Mapper)
		if !ok {
			return nil, errors.New("unsupported map value")
		}
		out := make(map[string]any, int(m.Size().(types.Int)))
		for it := m.Iterator(); it.HasNext() == types.True; {
			k := it.Next()
			key, ok := k.Value().(string)
			if !ok {
				return nil, fmt.Errorf("map keys must be strings, got %s", k.Type())
			}
			val, err := native(m.Get(k), depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported value type %s", v.Type())
	}
}
