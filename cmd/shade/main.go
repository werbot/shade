// Command shade sits between the chat client and the LLM: it replaces sensitive
// data in requests for stable placeholders and returns the real values
// back in the replies of the model.
package main

import "os"

func main() {
	os.Exit(Dispatch(os.Args[1:], IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
}
