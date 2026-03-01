
package lplex

import "github.com/sixfathoms/lplex/canbus"

// Type aliases so existing server code compiles unchanged.
type CANHeader = canbus.CANHeader

var (
	ParseCANID = canbus.ParseCANID
	BuildCANID = canbus.BuildCANID
)
