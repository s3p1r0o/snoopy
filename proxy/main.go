// Command proxy exposes the upstream LLM behind Rill Cloud's AI service
// (admin.rilldata.com, gRPC rill.admin.v1.AIService/Complete) as an
// OpenAI-compatible HTTP endpoint.
//

package main

import "os"

func main() {
	runServe(os.Args[1:], os.Stdout, os.Stderr)
}
