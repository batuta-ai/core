package fixture

import "flag"

type Exported struct{}
type hidden struct{}

const PublicConstant = 1
const privateConstant = 2

var PublicVariable = 1
var privateVariable = 2

func PublicFunction()           {}
func privateFunction()          {}
func (Exported) PublicMethod()  {}
func (Exported) privateMethod() {}

func RegisterFlags() {
	flag.String("global-name", "", "")
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.Bool("local-name", false, "")
	set.Int("count", 0, "")
	set.Duration("delay", 0, "")
	set.Var(nil, "ignored-not-first", "")
	var direct flag.FlagSet
	direct.String("direct", "", "")
	composite := &flag.FlagSet{}
	composite.Bool("composite", false, "")
	alias := composite
	alias.String("alias", "", "")
}
