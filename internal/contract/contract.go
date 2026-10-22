// Package contract checks the pre-conditions, post-conditions and invariants
// the code states (Design by Contract). A violated contract is a bug in
// thetix, never bad input: it panics with a Violation, which the command line
// reports with its location and exits 70.
//
// Input from people, the environment, files and the network is validated
// with ordinary errors instead.
package contract

import (
	"fmt"
	"runtime"
)

// Violation is the panic value of a failed contract.
type Violation struct {
	Kind    string // "precondition", "postcondition" or "invariant"
	Message string
	Where   string // file:line of the failed check
}

func (v Violation) Error() string {
	return fmt.Sprintf("%s violated at %s: %s", v.Kind, v.Where, v.Message)
}

func check(kind string, ok bool, format string, args []any) {
	if ok {
		return
	}
	where := "unknown"
	if _, file, line, found := runtime.Caller(2); found {
		where = fmt.Sprintf("%s:%d", file, line)
	}
	panic(Violation{Kind: kind, Message: fmt.Sprintf(format, args...), Where: where})
}

// Require states what the caller must guarantee.
func Require(ok bool, format string, args ...any) { check("precondition", ok, format, args) }

// Ensure states what the callee guarantees in return.
func Ensure(ok bool, format string, args ...any) { check("postcondition", ok, format, args) }

// Invariant states what must always hold.
func Invariant(ok bool, format string, args ...any) { check("invariant", ok, format, args) }
