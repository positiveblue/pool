package matching

import "fmt"

var (
	// ErrNoMarketPossible is returned by MaybeClear if it isn't possible
	// to make a market base don the current set of pending orders.
	ErrNoMarketPossible = fmt.Errorf("a market cannot be made")
)
