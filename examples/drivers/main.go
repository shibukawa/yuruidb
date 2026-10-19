// Run with go run -tags aws ./examples/drivers to inspect the statically linked
// drivers. No driver configuration or cloud credentials are loaded.
package main

import (
	"fmt"
	"github.com/shibukawa/yuruidb"
	_ "github.com/shibukawa/yuruidb/backends"
	"strings"
)

func main() { fmt.Println(strings.Join(yuruidb.Drivers(), ",")) }
