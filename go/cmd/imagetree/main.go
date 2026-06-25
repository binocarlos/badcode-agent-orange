// Command imagetree reads a JSON array of {name,parent} from stdin and prints
// either the topological build order (default) or, with -target, the ancestor
// chain (root-first, inclusive) for one node. One name per line.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/imagetree"
)

// Run is the testable entry point.
func Run(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("imagetree", flag.ContinueOnError)
	target := fs.String("target", "", "print the ancestor chain (root-first, inclusive) for this node instead of the full build order")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var nodes []imagetree.Node
	if err := json.Unmarshal(data, &nodes); err != nil {
		return fmt.Errorf("parse JSON nodes: %w", err)
	}
	var names []string
	if *target != "" {
		names, err = imagetree.Chain(nodes, *target)
	} else {
		names, err = imagetree.BuildOrder(nodes)
	}
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, strings.Join(names, "\n")+"\n")
	return err
}

func main() {
	if err := Run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "imagetree:", err)
		os.Exit(1)
	}
}
