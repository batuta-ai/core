package classify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/batuta-ai/core/judge"
	"github.com/batuta-ai/core/routing"
)

const v3DecisionName = "classify_v3"

var v3QuestionOrder = []string{"contract", "security"}

var v3ContractCriteria = map[string]string{
	"exported_change": "the task text names, or directly describes changing, adding or removing, an identifier or flag in the packet, or a file format or protocol",
	"internal_only":   "the task text describes a change that leaves every identifier and flag in the packet as it is",
	"insufficient":    "the task text does not say enough to choose",
}

var v3SecurityCriteria = map[string]string{
	"security_behaviour": "the task text asks to change what is permitted, contained, redacted or kept secret in a listed path",
	"incidental":         "the task text touches a listed path without changing what is permitted, contained, redacted or kept secret",
	"insufficient":       "the task text does not say enough to choose",
}

var v3SecurityWords = []string{"permission", "sandbox", "secret", "redact", "auth", "grant", "contain"}

// ContractPacket extracts the present, parseable, non-test Go files in Scope.
// Resolved paths must remain below root, including after following symlinks.
func ContractPacket(root string, scope []string) ([]string, bool) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, false
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return nil, false
	}
	identifiers := make(map[string]struct{})
	visited := make(map[string]struct{})
	for _, entry := range scope {
		if !filepath.IsLocal(entry) {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, entry))
		if err != nil {
			continue
		}
		for _, match := range matches {
			if filepath.Ext(match) != ".go" || strings.HasSuffix(match, "_test.go") {
				continue
			}
			resolved, err := filepath.EvalSymlinks(match)
			if err != nil {
				continue
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil || !insideRoot(resolvedRoot, resolved) {
				continue
			}
			if _, ok := visited[resolved]; ok {
				continue
			}
			info, err := os.Stat(resolved)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			visited[resolved] = struct{}{}
			file, err := parser.ParseFile(token.NewFileSet(), resolved, nil, 0)
			if err != nil {
				continue
			}
			collectContractIdentifiers(file, identifiers)
		}
	}
	packet := make([]string, 0, len(identifiers))
	for name := range identifiers {
		packet = append(packet, name)
	}
	slices.Sort(packet)
	if len(packet) > 60 {
		packet = packet[:60]
	}
	return packet, len(packet) > 0
}

func insideRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func collectContractIdentifiers(file *ast.File, names map[string]struct{}) {
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if !declaration.Name.IsExported() {
				continue
			}
			name := declaration.Name.Name
			if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
				if receiver := receiverName(declaration.Recv.List[0].Type); receiver != "" {
					name = receiver + "." + name
				}
			}
			names[name] = struct{}{}
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				switch specification := specification.(type) {
				case *ast.TypeSpec:
					if specification.Name.IsExported() {
						names[specification.Name.Name] = struct{}{}
					}
				case *ast.ValueSpec:
					if declaration.Tok == token.VAR || declaration.Tok == token.CONST {
						for _, name := range specification.Names {
							if name.IsExported() {
								names[name.Name] = struct{}{}
							}
						}
					}
				}
			}
		}
	}
	collectFlagNames(file, names)
}

func receiverName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return receiverName(expression.X)
	case *ast.IndexExpr:
		return receiverName(expression.X)
	case *ast.IndexListExpr:
		return receiverName(expression.X)
	default:
		return ""
	}
}

func collectFlagNames(file *ast.File, names map[string]struct{}) {
	flagPackages := make(map[string]struct{})
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err == nil && path == "flag" {
			alias := "flag"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			flagPackages[alias] = struct{}{}
		}
	}
	flagSets := make(map[string]struct{})
	for changed := true; changed; {
		changed = false
		mark := func(name string) {
			if _, known := flagSets[name]; !known {
				flagSets[name] = struct{}{}
				changed = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.ValueSpec:
				if flagSetType(node.Type, flagPackages) {
					for _, name := range node.Names {
						mark(name.Name)
					}
				}
				for index, value := range node.Values {
					if index < len(node.Names) && flagSetValue(value, flagPackages, flagSets) {
						mark(node.Names[index].Name)
					}
				}
			case *ast.Field:
				if flagSetType(node.Type, flagPackages) {
					for _, name := range node.Names {
						mark(name.Name)
					}
				}
			case *ast.AssignStmt:
				for index, value := range node.Rhs {
					if index < len(node.Lhs) && flagSetValue(value, flagPackages, flagSets) {
						if name, ok := node.Lhs[index].(*ast.Ident); ok {
							mark(name.Name)
						}
					}
				}
			}
			return true
		})
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "String", "Bool", "Int", "Duration", "Var":
		default:
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, ok := flagPackages[receiver.Name]; !ok {
			if _, ok := flagSets[receiver.Name]; !ok {
				return true
			}
		}
		for _, argument := range call.Args {
			literal, ok := argument.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			name, err := strconv.Unquote(literal.Value)
			if err == nil {
				names["flag:"+name] = struct{}{}
			}
			break
		}
		return true
	})
}

func flagSetType(expression ast.Expr, packages map[string]struct{}) bool {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "FlagSet" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = packages[identifier.Name]
	return ok
}

func flagSetValue(expression ast.Expr, packages, sets map[string]struct{}) bool {
	if address, ok := expression.(*ast.UnaryExpr); ok && address.Op == token.AND {
		return flagSetValue(address.X, packages, sets)
	}
	if identifier, ok := expression.(*ast.Ident); ok {
		_, known := sets[identifier.Name]
		return known
	}
	if literal, ok := expression.(*ast.CompositeLit); ok {
		return flagSetType(literal.Type, packages)
	}
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "NewFlagSet" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = packages[identifier.Name]
	return ok
}

// SecurityPacket returns Scope entries whose paths contain a frozen security word.
func SecurityPacket(scope []string) ([]string, bool) {
	packet := make([]string, 0)
	for _, entry := range scope {
		lower := strings.ToLower(entry)
		for _, word := range v3SecurityWords {
			if strings.Contains(lower, word) {
				packet = append(packet, entry)
				break
			}
		}
	}
	return packet, len(packet) > 0
}

type requestStateV3 struct {
	Task           taskState `json:"task"`
	Context        string    `json:"context"`
	Note           string    `json:"note"`
	ContractPacket []string  `json:"contract_packet,omitempty"`
	SecurityPacket []string  `json:"security_packet,omitempty"`
}

// BuildRequestV3 asks only questions backed by a nonempty code-built packet.
func BuildRequestV3(root string, task routing.PlanTask, context string) (judge.Request, bool) {
	request := BuildRequest(task, context)
	base := request.State.(requestState)
	contract, contractFound := ContractPacket(root, task.Scope)
	security, securityFound := SecurityPacket(task.Scope)
	request.Decision = v3DecisionName
	request.State = requestStateV3{
		Task: base.Task, Context: base.Context, Note: base.Note,
		ContractPacket: contract, SecurityPacket: security,
	}
	request.Questions = make(map[string]judge.Question)
	if contractFound {
		request.Questions["contract"] = judge.Question{
			Type:         judge.QuestionChoice,
			Instructions: "Does the task text name, or directly describe changing, adding or removing, an identifier or flag in the packet, or a file format or protocol?",
			Criteria:     v3ContractCriteria,
		}
	}
	if securityFound {
		request.Questions["security"] = judge.Question{
			Type:         judge.QuestionChoice,
			Instructions: "Does the task text ask to change what is permitted, contained, redacted or kept secret in a listed path?",
			Criteria:     v3SecurityCriteria,
		}
	}
	return request, contractFound || securityFound
}

type DecisionV3 struct {
	Complexity routing.Complexity
	Status     map[string]string
}

// DecideV3 applies section 14's one-lane adjustment to the code lane.
func DecideV3(codeLane routing.Complexity, asked map[string]bool, answers map[string]judge.Answer, threshold float64) DecisionV3 {
	decision := DecisionV3{Complexity: codeLane, Status: make(map[string]string, len(v3QuestionOrder))}
	allInternal := true
	askedCount := 0
	raise := false
	for _, key := range v3QuestionOrder {
		if !asked[key] {
			decision.Status[key] = "not_asked"
			continue
		}
		askedCount++
		answer, exists := answers[key]
		if !exists || answer.Type != judge.QuestionChoice || !knownV3Choice(key, answer.Choice) || math.IsNaN(answer.Confidence) || math.IsInf(answer.Confidence, 0) {
			decision.Status[key] = "unavailable"
			allInternal = false
			continue
		}
		if answer.Choice == "insufficient" {
			decision.Status[key] = "insufficient"
			allInternal = false
			continue
		}
		if answer.Confidence < threshold {
			decision.Status[key] = "below_threshold"
			allInternal = false
			continue
		}
		decision.Status[key] = "firm"
		if answer.Choice == "exported_change" || answer.Choice == "security_behaviour" {
			raise = true
			allInternal = false
		}
	}
	if codeLane == routing.ComplexityCritical {
		return decision
	}
	if raise {
		switch codeLane {
		case routing.ComplexityLow:
			decision.Complexity = routing.ComplexityMedium
		case routing.ComplexityMedium:
			decision.Complexity = routing.ComplexityHigh
		}
	} else if askedCount > 0 && allInternal {
		switch codeLane {
		case routing.ComplexityHigh:
			decision.Complexity = routing.ComplexityMedium
		case routing.ComplexityMedium:
			decision.Complexity = routing.ComplexityLow
		}
	}
	return decision
}

func knownV3Choice(question, choice string) bool {
	switch question {
	case "contract":
		_, ok := v3ContractCriteria[choice]
		return ok
	case "security":
		_, ok := v3SecurityCriteria[choice]
		return ok
	default:
		return false
	}
}
